package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func backstoryTestSeed(t *testing.T, now time.Time) domain.PersonalizationSeed {
	t.Helper()
	profile, err := domain.NewAgentProfileWithBackstory(
		"agent-emily", "Emily", 21, "female", "Любит старые книги.", "", now,
	)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := domain.NewPersonalizationSeed(profile, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	seed.Backstory.Episodes = []domain.BackstoryEpisode{
		{ID: "late-library", Title: "Последний вечер в библиотеке", Content: "Я закрывала старую библиотеку одна.", Kind: "turning_point", People: []string{"Мария"}, Place: "старая библиотека", EmotionalValence: -0.7, Sequence: 2},
		{ID: "first-book", Title: "Первая книга", Content: "В детстве я нашла книгу с фиолетовой обложкой.", Kind: "childhood", Place: "дом", EmotionalValence: 0.6, Sequence: 1},
	}
	if err := seed.Validate(); err != nil {
		t.Fatal(err)
	}
	return seed
}

func newBackstoryTestEngine(t *testing.T, store *memoryTestStore, clock *testClock) *Engine {
	t.Helper()
	engine, err := NewEngine(Config{
		AgentID: "agent-emily", Store: store, Now: clock.Now, IDs: &testIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func TestHydrateBackstoryCreatesTypedFictionalMemoriesInSequence(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	store := newMemoryTestStore()
	engine := newBackstoryTestEngine(t, store, &testClock{current: now})
	seed := backstoryTestSeed(t, now)

	results, err := engine.HydrateBackstory(context.Background(), seed)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || !results[0].Created || !results[1].Created {
		t.Fatalf("results = %#v", results)
	}
	firstPayload, err := ParseBackstoryMemoryPayload(results[0].Memory.ContentJSON)
	if err != nil {
		t.Fatal(err)
	}
	if firstPayload.EpisodeID != "first-book" || firstPayload.EpistemicStatus != "fictional" || firstPayload.Provenance != "identity_seed" {
		t.Fatalf("payload = %#v", firstPayload)
	}
	for _, result := range results {
		item := result.Memory
		if item.AgentID != seed.AgentID || item.Kind != domain.MemoryKindEpisodic || item.Nature != domain.MemoryNatureFiction ||
			item.Scope != domain.MemoryScopeAgentPrivate || item.Retention != domain.MemoryRetentionPermanent || item.Confidence != 1 {
			t.Fatalf("memory = %#v", item)
		}
		sources, err := store.ListMemorySources(context.Background(), item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(sources) != 1 || sources[0].SourceType != BackstorySourceIdentitySeed || sources[0].SourceID != seed.RevisionID {
			t.Fatalf("sources = %#v", sources)
		}
	}
}

func TestHydrateBackstoryIsIdempotentAndOnlyVersionsChangedEpisode(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	clock := &testClock{current: now}
	store := newMemoryTestStore()
	engine := newBackstoryTestEngine(t, store, clock)
	seed := backstoryTestSeed(t, now)
	created, err := engine.HydrateBackstory(context.Background(), seed)
	if err != nil {
		t.Fatal(err)
	}

	repeated, err := engine.HydrateBackstory(context.Background(), seed)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range repeated {
		if result.Changed || result.Memory.Version != 1 {
			t.Fatalf("repeat result = %#v", result)
		}
		versions, _ := store.ListMemoryVersions(context.Background(), result.Memory.ID, 0)
		if len(versions) != 1 {
			t.Fatalf("versions after no-op = %#v", versions)
		}
	}

	clock.current = now.Add(time.Hour)
	updatedSeed := seed
	updatedSeed.Version = 2
	updatedSeed.RevisionID = "agent-emily:personalization:v2"
	updatedSeed.ParentID = seed.RevisionID
	updatedSeed.ParentVersion = seed.Version
	updatedSeed.Operation = domain.PersonalizationOperationUpdate
	updatedSeed.Reason = "owner edited one episode"
	updatedSeed.UpdatedAt = clock.current
	updatedSeed.Backstory.Episodes = append([]domain.BackstoryEpisode(nil), seed.Backstory.Episodes...)
	updatedSeed.Backstory.Episodes[0].Content = "Я закрывала старую библиотеку и впервые решила начать новую жизнь."
	updatedSeed.Backstory.Episodes[0].EmotionalValence = 0
	if err := updatedSeed.Validate(); err != nil {
		t.Fatal(err)
	}
	updated, err := engine.HydrateBackstory(context.Background(), updatedSeed)
	if err != nil {
		t.Fatal(err)
	}
	if updated[0].Changed || !updated[1].Changed || updated[1].Created || updated[1].Memory.Version != 2 || updated[1].Memory.Valence != 0 {
		t.Fatalf("updated results = %#v", updated)
	}
	unchangedVersions, _ := store.ListMemoryVersions(context.Background(), created[0].Memory.ID, 0)
	changedVersions, _ := store.ListMemoryVersions(context.Background(), created[1].Memory.ID, 0)
	if len(unchangedVersions) != 1 || len(changedVersions) != 2 {
		t.Fatalf("version counts = %d, %d", len(unchangedVersions), len(changedVersions))
	}
	changedSources, _ := store.ListMemorySources(context.Background(), created[1].Memory.ID)
	if len(changedSources) != 2 || changedSources[1].SourceID != updatedSeed.RevisionID {
		t.Fatalf("changed sources = %#v", changedSources)
	}
}

func TestHydrateBackstoryRespectsDeletedMemoryAndAgentBoundary(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	clock := &testClock{current: now}
	store := newMemoryTestStore()
	engine := newBackstoryTestEngine(t, store, clock)
	seed := backstoryTestSeed(t, now)
	created, err := engine.HydrateBackstory(context.Background(), seed)
	if err != nil {
		t.Fatal(err)
	}
	clock.current = now.Add(time.Minute)
	forgotten, err := engine.Remember(context.Background(), Candidate{Operation: CandidateForget, MatchID: created[0].Memory.ID})
	if err != nil {
		t.Fatal(err)
	}
	results, err := engine.HydrateBackstory(context.Background(), seed)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Changed || results[0].Memory.Lifecycle != domain.MemoryLifecycleDeleted || results[0].Memory.Version != forgotten.Memory.Version {
		t.Fatalf("deleted result = %#v", results[0])
	}

	other := seed
	other.AgentID = "agent-other"
	if _, err := engine.HydrateBackstory(context.Background(), other); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("agent mismatch error = %v", err)
	}
}

func TestMemoryNatureFictionIsAValidDomainNature(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	memory := baseMemory("Я помню вымышленный сад.", now)
	memory.ID = "fiction-1"
	memory.AgentID = "agent-emily"
	memory.Nature = domain.MemoryNatureFiction
	if err := memory.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestBackstoryDisableRequiresExplicitRehydrateFromOwnerSeed(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	clock := &testClock{current: now}
	store := newMemoryTestStore()
	engine := newBackstoryTestEngine(t, store, clock)
	seed := backstoryTestSeed(t, now)
	created, err := engine.HydrateBackstory(context.Background(), seed)
	if err != nil {
		t.Fatal(err)
	}
	target := created[0].Memory
	clock.current = now.Add(time.Minute)
	disabled, err := engine.DisableBackstoryMemory(context.Background(), target.ID)
	if err != nil || !disabled.Changed || disabled.Memory.Lifecycle != domain.MemoryLifecycleDeleted {
		t.Fatalf("disable = %#v, %v", disabled, err)
	}
	clock.current = now.Add(2 * time.Minute)
	automatic, err := engine.HydrateBackstory(context.Background(), seed)
	if err != nil || automatic[0].Changed || automatic[0].Memory.Lifecycle != domain.MemoryLifecycleDeleted {
		t.Fatalf("automatic hydration resurrected owner-disabled memory: %#v, %v", automatic[0], err)
	}
	clock.current = now.Add(3 * time.Minute)
	restored, err := engine.RehydrateBackstoryEpisode(context.Background(), seed, "first-book")
	if err != nil || !restored.Changed || restored.Operation != OperationRestore || restored.Memory.Lifecycle != domain.MemoryLifecycleActive || restored.Memory.Content != target.Content {
		t.Fatalf("explicit rehydrate = %#v, %v", restored, err)
	}
	versions, _ := store.ListMemoryVersions(context.Background(), target.ID, 0)
	if len(versions) != 3 || versions[1].Operation != OperationForget || versions[2].Operation != OperationRestore {
		t.Fatalf("curation history = %#v", versions)
	}
}

func TestProcessTurnCreatesSeparateValidatedFictionInterpretation(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	clock := &testClock{current: now}
	store := newMemoryTestStore()
	seedEngine := newBackstoryTestEngine(t, store, clock)
	seed := backstoryTestSeed(t, now)
	created, err := seedEngine.HydrateBackstory(context.Background(), seed)
	if err != nil {
		t.Fatal(err)
	}
	source := created[0].Memory
	interpreter, err := NewEngine(Config{
		AgentID: seed.AgentID, Store: store, Now: clock.Now, IDs: &testIDs{},
		Extractor: testExtractor{candidates: []Candidate{{
			Memory:         domain.Memory{Content: "Теперь я понимаю тот вечер как первый самостоятельный выбор.", Confidence: .78, Salience: .7},
			Interpretation: &FictionInterpretationCandidate{SourceMemoryID: source.ID, Status: FictionProvenanceInterpreted},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	turn := Turn{
		RunID: "run-interpret", AgentID: seed.AgentID, ConversationID: "conversation-interpret", Now: now.Add(time.Minute),
		RecalledMemories: []RecalledMemory{{ID: source.ID, Version: source.Version, Nature: source.Nature, Content: source.Content, Provenance: FictionProvenanceOwnerSeed}},
	}
	results, err := interpreter.ProcessTurn(context.Background(), turn)
	if err != nil || len(results) != 1 {
		t.Fatalf("ProcessTurn() = %#v, %v", results, err)
	}
	derived := results[0].Memory
	if derived.ID == source.ID || derived.Nature != domain.MemoryNatureFiction || derived.Content == source.Content || source.Content != created[0].Memory.Content {
		t.Fatalf("source was not preserved or derivative is invalid: source=%#v derived=%#v", source, derived)
	}
	payload, err := ParseBackstoryInterpretationPayload(derived.ContentJSON)
	if err != nil || payload.Provenance != FictionProvenanceInterpreted || payload.SourceMemoryID != source.ID || payload.SourceVersion != source.Version || payload.OwnerAuthored {
		t.Fatalf("interpretation payload = %#v, %v", payload, err)
	}
	sources, _ := store.ListMemorySources(context.Background(), derived.ID)
	if len(sources) == 0 || sources[0].SourceType != BackstorySourceInterpretation || sources[0].SourceID != source.ID {
		t.Fatalf("interpretation sources = %#v", sources)
	}
}

func TestProcessTurnRejectsUnrecalledFictionInterpretation(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	store := newMemoryTestStore()
	engine := newBackstoryTestEngine(t, store, &testClock{current: now})
	seed := backstoryTestSeed(t, now)
	created, err := engine.HydrateBackstory(context.Background(), seed)
	if err != nil {
		t.Fatal(err)
	}
	interpreter, err := NewEngine(Config{
		AgentID: seed.AgentID, Store: store, IDs: &testIDs{},
		Extractor: testExtractor{candidates: []Candidate{{
			Memory:         domain.Memory{Content: "Поддельная интерпретация"},
			Interpretation: &FictionInterpretationCandidate{SourceMemoryID: created[0].Memory.ID, Status: FictionProvenanceUncertain},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = interpreter.ProcessTurn(context.Background(), Turn{AgentID: seed.AgentID, ConversationID: "conversation", Now: now})
	if !errors.Is(err, ErrCandidateRejected) {
		t.Fatalf("unrecalled interpretation error = %v", err)
	}
}
