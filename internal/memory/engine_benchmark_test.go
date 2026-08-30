package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// benchNow is a fixed instant. Nothing in this file may derive a fixture from
// time.Now: two tests in this repository were found failing deterministically
// for a fixed window each day because their fixtures drifted with the clock.
var benchNow = time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)

var (
	corpusVerbs      = []string{"любит", "предпочитает", "избегает", "планирует", "закончил", "начал", "обсуждал", "отложил"}
	corpusQualifiers = []string{"зелёный", "крепкий", "срочный", "долгий", "новый", "старый", "важный", "личный", "утренний"}
	corpusNouns      = []string{"чай", "кофе", "проект", "релиз", "тест", "код", "отпуск", "книга", "музыка", "спорт", "дом", "друг", "работа", "здоровье"}
	corpusKinds      = []Kind{KindCore, KindUserProfile, KindSemantic, KindProcedural, KindRelationship, KindEpisodic}
	corpusNatures    = []ContentType{ContentFact, ContentOpinion, ContentEmotion, ContentInference}
)

// corpusMemory builds one deterministic record. Content, salience, valence and
// kind all vary with the index so that ranking has something to separate.
func corpusMemory(index int, now time.Time) domain.Memory {
	content := fmt.Sprintf("Пользователь %s %s %s в контексте задачи номер %d",
		corpusVerbs[index%len(corpusVerbs)],
		corpusQualifiers[(index/3)%len(corpusQualifiers)],
		corpusNouns[(index/7)%len(corpusNouns)],
		index)
	age := time.Duration(index) * time.Hour
	memory := domain.Memory{
		ID: domain.ID(fmt.Sprintf("memory-%05d", index)), Version: 1,
		Scope: domain.MemoryScopeAgentPrivate,
		Kind:  corpusKinds[index%len(corpusKinds)], Nature: corpusNatures[index%len(corpusNatures)],
		Content: content, Summary: "",
		Confidence: 0.6 + float64(index%40)/100, Salience: 0.2 + float64(index%79)/100,
		Valence:     float64((index%21)-10) / 10,
		Sensitivity: domain.MemorySensitivityPrivate, Retention: domain.MemoryRetentionDecay,
		Lifecycle: domain.MemoryLifecycleActive,
		CreatedAt: now.Add(-age), UpdatedAt: now.Add(-age),
	}
	if memory.Salience > 1 {
		memory.Salience = 1
	}
	if memory.Confidence > 1 {
		memory.Confidence = 1
	}
	switch {
	case index%211 == 0:
		memory.Sensitivity = domain.MemorySensitivityHighlySensitive
	case index%97 == 0:
		memory.HiddenFromCore = true
	case index%149 == 0:
		memory.Pinned = true
	}
	memory.CanonicalKey = canonicalKey(memory)
	return memory
}

// newCorpusStore fills a store fake with size records plus two provenance
// sources each, so that per-result source loading is a real cost.
func newCorpusStore(size int, now time.Time) *memoryTestStore {
	store := newMemoryTestStore()
	for index := range size {
		memory := corpusMemory(index, now)
		store.memories[memory.ID] = memory
		store.sources[memory.ID] = []domain.MemorySource{
			{ID: domain.ID(fmt.Sprintf("source-%05d-a", index)), MemoryID: memory.ID, MemoryVersion: 1, SourceType: "message", SourceID: domain.ID(fmt.Sprintf("message-%05d", index)), MessageID: domain.ID(fmt.Sprintf("message-%05d", index)), CreatedAt: now},
			{ID: domain.ID(fmt.Sprintf("source-%05d-b", index)), MemoryID: memory.ID, MemoryVersion: 1, SourceType: "turn", SourceID: domain.ID(fmt.Sprintf("run-%05d", index)), RunID: domain.ID(fmt.Sprintf("run-%05d", index)), CreatedAt: now},
		}
	}
	return store
}

// indexedLexicalStore stands in for a real FTS5 projection: the hit set is
// precomputed, so a Recall benchmark measures the engine rather than the fake.
// memoryTestStore's own SearchMemoryLexical is itself a full scan and would
// otherwise dominate every measurement.
type indexedLexicalStore struct {
	*memoryTestStore
	postings map[string][]domain.ID
}

func newIndexedLexicalStore(size int, now time.Time) *indexedLexicalStore {
	store := &indexedLexicalStore{memoryTestStore: newCorpusStore(size, now), postings: make(map[string][]domain.ID)}
	ids := make([]domain.ID, 0, len(store.memories))
	for id := range store.memories {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		for _, token := range tokenize(store.memories[id].Content) {
			store.postings[token] = append(store.postings[token], id)
		}
	}
	return store
}

func (s *indexedLexicalStore) SearchMemoryLexical(_ context.Context, query string, filter MemoryFilter, limit int) ([]LexicalHit, error) {
	seen := make(map[domain.ID]struct{})
	hits := make([]LexicalHit, 0, limit)
	for _, token := range tokenize(query) {
		for _, id := range s.postings[token] {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			memory := s.memories[id]
			if !eligible(memory, filter) {
				continue
			}
			hits = append(hits, LexicalHit{MemoryID: id, Score: LexicalScore(memory.Content, query), Snippet: memory.Content})
			if limit > 0 && len(hits) >= limit {
				return hits, nil
			}
		}
	}
	return hits, nil
}

func newBenchEngine(tb testing.TB, store Store, now time.Time) *Engine {
	tb.Helper()
	engine, err := NewEngine(Config{
		Store: store, Now: func() time.Time { return now }, IDs: &testIDs{},
		Ranker:       HybridRanker{Weights: RankWeights{Lexical: .45, Recency: .2, Salience: .3, Affective: .05}},
		CoreBudget:   Budget{MaxItems: 24, MaxChars: 6_000},
		RecallBudget: Budget{MaxItems: 12, MaxChars: 6_000},
	})
	if err != nil {
		tb.Fatal(err)
	}
	return engine
}

const benchCorpusSize = 5_000

// BenchmarkRecall5000 measures one automatic Recall against a 5k-record store
// with a lexical (FTS-equivalent) projection configured, which is exactly the
// production wiring in internal/desktop/memory_runtime.go.
func BenchmarkRecall5000(b *testing.B) {
	store := newIndexedLexicalStore(benchCorpusSize, benchNow)
	engine := newBenchEngine(b, store, benchNow)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		results, err := engine.Recall(ctx, "зелёный чай проект", RecallOptions{Mode: RecallAutomatic, Now: benchNow, Limit: 12})
		if err != nil {
			b.Fatal(err)
		}
		if len(results) == 0 {
			b.Fatal("expected at least one recall hit")
		}
	}
}

// BenchmarkCoreSnapshot5000 measures one context assembly over the same store.
func BenchmarkCoreSnapshot5000(b *testing.B) {
	store := newIndexedLexicalStore(benchCorpusSize, benchNow)
	engine := newBenchEngine(b, store, benchNow)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		snapshot, err := engine.CoreSnapshot(ctx, Budget{})
		if err != nil {
			b.Fatal(err)
		}
		if len(snapshot.Entries) == 0 {
			b.Fatal("expected a non-empty core snapshot")
		}
	}
}

// BenchmarkResolveExisting5000 measures the deduplication scan that runs once
// per written candidate, for a candidate that matches nothing — the worst and
// most common case, since a genuinely new fact scans the whole store.
func BenchmarkResolveExisting5000(b *testing.B) {
	store := newCorpusStore(benchCorpusSize, benchNow)
	engine := newBenchEngine(b, store, benchNow)
	ctx := context.Background()
	candidate := Candidate{Memory: baseMemory("Совершенно новый факт, которого нет в корпусе", benchNow)}
	candidate.Memory.ID = "memory-candidate"
	normalized, err := Normalize(candidate.Memory, benchNow)
	if err != nil {
		b.Fatal(err)
	}
	normalized.CanonicalKey = canonicalKey(normalized)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, found, err := engine.resolveExisting(ctx, candidate, normalized)
		if err != nil {
			b.Fatal(err)
		}
		if found {
			b.Fatal("candidate must not match the corpus")
		}
	}
}

// BenchmarkProcessTurn5000 measures a whole post-turn pass with three
// candidates, which today repeats the deduplication scan once per candidate.
// The extractor returns duplicates of existing records so nothing is created
// and the store size stays constant across iterations.
func BenchmarkProcessTurn5000(b *testing.B) {
	store := newCorpusStore(benchCorpusSize, benchNow)
	candidates := make([]Candidate, 0, 3)
	for _, index := range []int{4_998, 4_999, 5_000} {
		memory := corpusMemory(index, benchNow)
		candidates = append(candidates, Candidate{Memory: domain.Memory{
			Kind: memory.Kind, Nature: memory.Nature, Content: memory.Content,
			Confidence: 0.1, Salience: 0.1, Sensitivity: domain.MemorySensitivityPrivate,
			Retention: domain.MemoryRetentionDecay, Lifecycle: domain.MemoryLifecycleActive,
			CreatedAt: benchNow, UpdatedAt: benchNow,
		}})
	}
	engine, err := NewEngine(Config{
		Store: store, Extractor: testExtractor{candidates: candidates},
		Now: func() time.Time { return benchNow }, IDs: &testIDs{},
	})
	if err != nil {
		b.Fatal(err)
	}
	turn := Turn{RunID: "run_bench", ConversationID: "conversation_bench", Now: benchNow}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := engine.ProcessTurn(ctx, turn); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLexicalScoreCorpus measures the per-record lexical scoring cost the
// review quantified as "5.73 µs and 1.7 KB per record".
func BenchmarkLexicalScoreCorpus(b *testing.B) {
	contents := make([]string, 512)
	for index := range contents {
		contents[index] = corpusMemory(index, benchNow).Content
	}
	query := "зелёный чай проект"
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		_ = LexicalScore(contents[i%len(contents)], query)
	}
}

// storeOnly hides any optional port a store fake also implements, so a test
// can exercise the no-FTS fallback path.
type storeOnly struct{ Store }

func mustRecall(tb testing.TB, engine *Engine, query string, options RecallOptions) []RecallResult {
	tb.Helper()
	results, err := engine.Recall(context.Background(), query, options)
	if err != nil {
		tb.Fatal(err)
	}
	return results
}

func formatRecall(results []RecallResult) string {
	var builder strings.Builder
	for _, result := range results {
		fmt.Fprintf(&builder, "%s score=%.6f lex=%.6f vec=%.6f rec=%.6f sal=%.6f aff=%.6f dormant=%t sources=%d snippet=%q\n",
			result.Memory.ID, result.Score, result.LexicalScore, result.VectorScore,
			result.RecencyScore, result.SalienceScore, result.AffectiveScore,
			result.Dormant, len(result.Evidence.Sources), result.Evidence.Snippet)
	}
	return builder.String()
}
