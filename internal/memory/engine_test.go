package memory

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

type testClock struct{ current time.Time }

func (c *testClock) Now() time.Time { return c.current }

type testIDs struct{ next int }

func (g *testIDs) NewID(prefix string) (domain.ID, error) {
	g.next++
	return domain.ID(prefix + "_" + string(rune('a'+g.next-1))), nil
}

type memoryTestStore struct {
	mu       sync.Mutex
	memories map[domain.ID]domain.Memory
	sources  map[domain.ID][]domain.MemorySource
	versions map[domain.ID][]MemoryRevision
	archive  []ArchiveHit
}

func newMemoryTestStore() *memoryTestStore {
	return &memoryTestStore{
		memories: make(map[domain.ID]domain.Memory),
		sources:  make(map[domain.ID][]domain.MemorySource),
		versions: make(map[domain.ID][]MemoryRevision),
	}
}

func (s *memoryTestStore) GetMemory(_ context.Context, id domain.ID) (domain.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	memory, ok := s.memories[id]
	if !ok {
		return domain.Memory{}, domain.ErrNotFound
	}
	return memory, nil
}

func (s *memoryTestStore) ListMemories(_ context.Context, filter MemoryFilter) ([]domain.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]domain.Memory, 0, len(s.memories))
	for _, memory := range s.memories {
		if memory.Lifecycle == domain.MemoryLifecycleDeleted && !filter.IncludeDeleted {
			continue
		}
		if memory.Lifecycle == domain.MemoryLifecycleDormant && !filter.IncludeDormant {
			continue
		}
		if memory.HiddenFromCore && !filter.IncludeHidden {
			continue
		}
		if len(filter.Kinds) > 0 && !containsKind(filter.Kinds, memory.Kind) {
			continue
		}
		if len(filter.States) > 0 && !containsState(filter.States, memory.Lifecycle) {
			continue
		}
		result = append(result, memory)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
}

func (s *memoryTestStore) ApplyMemoryChange(_ context.Context, change MemoryChange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := change.Memory.Validate(); err != nil {
		return err
	}
	if previous, exists := s.memories[change.Memory.ID]; exists && change.Memory.Version <= previous.Version {
		return domain.ErrConflict
	}
	if change.Revision == nil {
		return errors.New("revision is required")
	}
	if err := change.Revision.Valid(); err != nil {
		return err
	}
	if previous, exists := s.memories[change.Memory.ID]; exists && change.Revision.ParentVersion != previous.Version {
		return domain.ErrConflict
	}
	s.memories[change.Memory.ID] = change.Memory
	s.versions[change.Memory.ID] = append(s.versions[change.Memory.ID], *change.Revision)
	for _, source := range change.Sources {
		if err := source.Validate(); err != nil {
			return err
		}
		s.sources[change.Memory.ID] = append(s.sources[change.Memory.ID], source)
	}
	return nil
}

func (s *memoryTestStore) TouchMemory(_ context.Context, id domain.ID, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	memory, ok := s.memories[id]
	if !ok {
		return domain.ErrNotFound
	}
	memory.AccessCount++
	memory.LastAccessedAt = at.UTC()
	memory.LastRecalledAt = at.UTC()
	s.memories[id] = memory
	return nil
}

func (s *memoryTestStore) ListMemorySources(_ context.Context, id domain.ID) ([]domain.MemorySource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.MemorySource(nil), s.sources[id]...), nil
}

func (s *memoryTestStore) ListMemoryVersions(_ context.Context, id domain.ID, limit int) ([]MemoryRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := append([]MemoryRevision(nil), s.versions[id]...)
	if limit > 0 && len(items) > limit {
		items = items[len(items)-limit:]
	}
	return items, nil
}

func (s *memoryTestStore) SearchMemoryLexical(_ context.Context, query string, filter MemoryFilter, limit int) ([]LexicalHit, error) {
	items, _ := s.ListMemories(context.Background(), filter)
	hits := make([]LexicalHit, 0)
	for _, item := range items {
		score := LexicalScore(item.Content, query)
		if score > 0 {
			hits = append(hits, LexicalHit{MemoryID: item.ID, Score: score, Snippet: item.Content})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func (s *memoryTestStore) SearchArchive(_ context.Context, query string, options ArchiveSearchOptions) ([]ArchiveHit, error) {
	result := make([]ArchiveHit, 0)
	for _, hit := range s.archive {
		if LexicalScore(hit.Content, query) > 0 {
			hit.Score = LexicalScore(hit.Content, query)
			result = append(result, hit)
		}
	}
	if options.Limit > 0 && len(result) > options.Limit {
		result = result[:options.Limit]
	}
	return result, nil
}

type testExtractor struct{ candidates []Candidate }

func (e testExtractor) Extract(context.Context, Turn) ([]Candidate, error) {
	return append([]Candidate(nil), e.candidates...), nil
}

func baseMemory(content string, now time.Time) domain.Memory {
	return domain.Memory{
		Version: 1,
		Kind:    domain.MemoryKindSemantic, Nature: domain.MemoryNatureFact,
		Content: content, Confidence: 0.9, Salience: 0.8,
		Sensitivity: domain.MemorySensitivityPrivate, Retention: domain.MemoryRetentionDecay,
		Lifecycle: domain.MemoryLifecycleActive, CreatedAt: now, UpdatedAt: now,
	}
}

func TestProcessTurnAutonomouslyStoresCrossSessionMemoryWithProvenance(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	store := newMemoryTestStore()
	engine, err := NewEngine(Config{
		Store: store, Extractor: testExtractor{candidates: []Candidate{{Memory: baseMemory("Пользователь любит зелёный чай", now)}}},
		Now: (&testClock{current: now}).Now, IDs: &testIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	results, err := engine.ProcessTurn(context.Background(), Turn{
		RunID: "run_1", ConversationID: "conversation_1", Now: now,
		Messages: []TranscriptMessage{{ID: "message_1", ConversationID: "conversation_1", Role: "user", Content: "Я люблю зелёный чай", CreatedAt: now}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Created || !results[0].Changed {
		t.Fatalf("unexpected write result: %#v", results)
	}
	recalled, err := engine.Recall(context.Background(), "зелёный чай", RecallOptions{Mode: RecallAutomatic, Now: now, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(recalled) != 1 || recalled[0].Evidence.Sources[0].MessageID != "message_1" {
		t.Fatalf("cross-session recall/provenance failed: %#v", recalled)
	}
	if recalled[0].Memory.SourceConversationID != "" {
		t.Fatalf("source conversation should remain evidence, not a namespace: %#v", recalled[0].Memory)
	}
}

func TestDormantMemoryRequiresDeliberateRecallAndCanBeRestored(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	store := newMemoryTestStore()
	old := baseMemory("Редкий факт из прошлого диалога", now)
	old.ID = "memory_old"
	old.CreatedAt = now.Add(-30 * 24 * time.Hour)
	old.UpdatedAt = old.CreatedAt
	old.Salience = 0.08
	if err := old.Validate(); err != nil {
		t.Fatal(err)
	}
	store.memories[old.ID] = old
	engine, err := NewEngine(Config{Store: store, Now: (&testClock{current: now}).Now, IDs: &testIDs{}, DecayPolicy: func(domain.Memory) DecayPolicy {
		return DecayPolicy{HalfLife: 24 * time.Hour, DormantThreshold: 0.2}
	}})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := engine.ApplyDecay(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0].Memory.Lifecycle != domain.MemoryLifecycleDormant {
		t.Fatalf("expected dormant transition: %#v", changed)
	}
	if automatic, err := engine.Recall(context.Background(), "редкий факт прошлого", RecallOptions{Mode: RecallAutomatic, Now: now}); err != nil {
		t.Fatal(err)
	} else if len(automatic) != 0 {
		t.Fatalf("dormant memory leaked into automatic retrieval: %#v", automatic)
	}
	deliberate, err := engine.Recall(context.Background(), "редкий факт прошлого", RecallOptions{Mode: RecallDeliberate, RestoreDormant: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(deliberate) != 1 || deliberate[0].Dormant || deliberate[0].Memory.Lifecycle != domain.MemoryLifecycleActive {
		t.Fatalf("deliberate recall did not restore memory: %#v", deliberate)
	}
	if store.memories[old.ID].Version != 3 {
		t.Fatalf("expected decay + restore versions, got %d", store.memories[old.ID].Version)
	}
}

func TestDuplicateAndLowerConfidenceConflictAreVersionedConservatively(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	store := newMemoryTestStore()
	engine, err := NewEngine(Config{Store: store, Now: (&testClock{current: now}).Now, IDs: &testIDs{}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := engine.Remember(context.Background(), Candidate{Memory: baseMemory("Пользователь работает в Go", now)})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := engine.Remember(context.Background(), Candidate{Memory: baseMemory(" Пользователь работает в Go. ", now)})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Changed || duplicate.Memory.ID != first.Memory.ID {
		t.Fatalf("duplicate should be a no-op: %#v", duplicate)
	}
	conflict := baseMemory("Пользователь работает в Rust", now)
	conflict.CanonicalKey = first.Memory.CanonicalKey
	conflict.Confidence = 0.2
	result, err := engine.Remember(context.Background(), Candidate{Memory: conflict, DedupKey: first.Memory.CanonicalKey})
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed || store.memories[first.Memory.ID].Content != "Пользователь работает в Go" {
		t.Fatalf("lower-confidence conflict should not replace fact: %#v", result)
	}
	if len(store.versions[first.Memory.ID]) != 1 {
		t.Fatalf("no-op conflict should not append a fake revision: %#v", store.versions[first.Memory.ID])
	}
	withEvidence, err := engine.Remember(context.Background(), Candidate{
		Memory:  baseMemory("Пользователь работает в Go", now),
		Sources: []domain.MemorySource{{SourceType: "message", SourceID: "message-later", MessageID: "message-later", CreatedAt: now}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !withEvidence.Changed || withEvidence.Memory.Version != 2 || len(store.sources[first.Memory.ID]) != 1 {
		t.Fatalf("duplicate evidence should append a source revision: result=%#v sources=%#v", withEvidence, store.sources[first.Memory.ID])
	}
}

func TestCoreSnapshotIsBoundedAndMarksMemoryAsData(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	store := newMemoryTestStore()
	for index, content := range []string{
		"Первый важный факт", "Второй важный факт", "Третий важный факт",
	} {
		memory := baseMemory(content, now)
		memory.ID = domain.ID("memory_" + string(rune('a'+index)))
		store.memories[memory.ID] = memory
	}
	highlySensitive := baseMemory("Секрет, который нельзя помещать в prompt", now)
	highlySensitive.ID = "memory_sensitive"
	highlySensitive.Sensitivity = domain.MemorySensitivityHighlySensitive
	highlySensitive.Salience = 1
	store.memories[highlySensitive.ID] = highlySensitive
	engine, err := NewEngine(Config{Store: store, Now: (&testClock{current: now}).Now, CoreBudget: Budget{MaxItems: 2, MaxChars: 24, MaxTokens: 6}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := engine.CoreSnapshot(context.Background(), Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 2 || snapshot.Chars > 24 || snapshot.Tokens > 6 {
		t.Fatalf("snapshot exceeded bound: %#v", snapshot)
	}
	if !strings.Contains(snapshot.Text, "provenance=\"bounded-core-snapshot\"") || strings.Contains(snapshot.Text, "Первый важный факт") && strings.Contains(snapshot.Text, "<system>") {
		t.Fatalf("snapshot is not clearly data-marked: %s", snapshot.Text)
	}
	if strings.Contains(snapshot.Text, highlySensitive.Content) {
		t.Fatalf("highly sensitive memory leaked into core snapshot: %s", snapshot.Text)
	}
}

func containsKind(items []Kind, target Kind) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func containsState(items []LifecycleState, target LifecycleState) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func TestBruteForceIndexCosineAndDimensionSafety(t *testing.T) {
	index := NewBruteForceIndex()
	ctx := context.Background()
	if err := index.Upsert(ctx, VectorDocument{ID: "a", Vector: []float64{1, 0}}); err != nil {
		t.Fatal(err)
	}
	if err := index.Upsert(ctx, VectorDocument{ID: "b", Vector: []float64{0, 1}}); err != nil {
		t.Fatal(err)
	}
	if err := index.Upsert(ctx, VectorDocument{ID: "bad", Vector: []float64{math.NaN()}}); err == nil {
		t.Fatal("expected non-finite vector rejection")
	}
	matches, err := index.Search(ctx, []float64{0.9, 0.1}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].ID != "a" || matches[0].Score <= matches[1].Score {
		t.Fatalf("unexpected cosine ranking: %#v", matches)
	}
}
