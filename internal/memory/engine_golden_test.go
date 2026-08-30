package memory

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// updateGolden rewrites the fixtures under testdata. It exists so the golden
// files can be regenerated deliberately; a behaviour-preserving change must
// never need it. Run: go test ./internal/memory/ -run Golden -update-golden
var updateGolden = flag.Bool("update-golden", false, "rewrite memory golden fixtures")

// goldenCorpusSize is small enough to keep the fixtures readable and large
// enough that the recall budget actually bites and ordering is non-trivial.
const goldenCorpusSize = 400

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (regenerate with -update-golden)", path, err)
	}
	if string(want) != got {
		t.Errorf("golden %s mismatch\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}

// goldenQueries deliberately mixes a plain lexical query, a query whose terms
// appear in most records, an affective query (which routes through the
// prefix-matched stems pinned by engine_scoring_test.go), and a query that
// matches nothing.
var goldenQueries = []struct {
	name  string
	query string
	mode  RecallMode
}{
	{"lexical", "зелёный чай проект", RecallAutomatic},
	{"broad", "пользователь задачи", RecallAutomatic},
	{"affective", "какие у меня чувства про зелёный чай", RecallAutomatic},
	{"affective_no_overlap", "какие у меня чувства к этой работе", RecallAutomatic},
	{"phrase", "в контексте задачи номер 42", RecallAutomatic},
	{"unmatched", "квантовая хромодинамика", RecallAutomatic},
	{"deliberate", "старый отпуск", RecallDeliberate},
}

// TestRecallGoldenOrdering pins the exact result set, order, per-signal scores,
// snippets and provenance count of Recall over a fixed corpus. A performance
// change that quietly surfaced different memories, or reordered them, fails
// here rather than silently shipping.
func TestRecallGoldenOrdering(t *testing.T) {
	for _, testCase := range goldenQueries {
		t.Run(testCase.name, func(t *testing.T) {
			// A fresh store per query: Recall touches its hits, which moves
			// LastRecalledAt and therefore the recency score.
			store := newCorpusStore(goldenCorpusSize, benchNow)
			engine := newBenchEngine(t, store, benchNow)
			results := mustRecall(t, engine, testCase.query, RecallOptions{Mode: testCase.mode, Now: benchNow, Limit: 12})
			assertGolden(t, "recall_"+testCase.name+".txt", formatRecall(results))
		})
	}
}

// TestRecallGoldenWithoutLexicalSearcher pins the fallback path taken by an
// adapter that has no FTS projection, where every score comes from the
// in-process LexicalScore.
func TestRecallGoldenWithoutLexicalSearcher(t *testing.T) {
	for _, testCase := range goldenQueries {
		t.Run(testCase.name, func(t *testing.T) {
			store := newCorpusStore(goldenCorpusSize, benchNow)
			engine := newBenchEngine(t, storeOnly{Store: store}, benchNow)
			results := mustRecall(t, engine, testCase.query, RecallOptions{Mode: testCase.mode, Now: benchNow, Limit: 12})
			assertGolden(t, "recall_nofts_"+testCase.name+".txt", formatRecall(results))
		})
	}
}

// TestCoreSnapshotGolden pins the rendered context block: which memories are
// selected, in what order, with which provenance labels and budget accounting.
func TestCoreSnapshotGolden(t *testing.T) {
	store := newCorpusStore(goldenCorpusSize, benchNow)
	engine := newBenchEngine(t, store, benchNow)
	snapshot, err := engine.CoreSnapshot(context.Background(), Budget{})
	if err != nil {
		t.Fatal(err)
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "created_at=%s chars=%d tokens=%d entries=%d\n",
		snapshot.CreatedAt.Format(time.RFC3339), snapshot.Chars, snapshot.Tokens, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		fmt.Fprintf(&builder, "%s score=%.6f sources=%d content=%q\n",
			entry.Memory.ID, entry.Score, len(entry.Evidence.Sources), entry.Memory.Content)
	}
	builder.WriteString("--- text ---\n")
	builder.WriteString(snapshot.Text)
	builder.WriteString("\n")
	assertGolden(t, "core_snapshot.txt", builder.String())
}

// customKeyRecord is a stored record whose CanonicalKey was supplied by an
// extractor rather than derived from its content. It is what forces the
// deduplication scan through its content-comparison branch: the canonical-key
// branch cannot match it.
const customKeyRecordIndex = 99

func newResolveStore(now time.Time) *memoryTestStore {
	store := newCorpusStore(goldenCorpusSize, now)
	record := store.memories[corpusMemory(customKeyRecordIndex, now).ID]
	record.CanonicalKey = "extractor:supplied:key"
	store.memories[record.ID] = record
	return store
}

// candidateLike builds a write candidate that carries the same kind and nature
// as a stored record, which is what the deduplication scan requires before it
// will even compare content.
func candidateLike(index int, content string, now time.Time) Candidate {
	stored := corpusMemory(index, now)
	memory := baseMemory(content, now)
	memory.Kind = stored.Kind
	memory.Nature = stored.Nature
	return Candidate{Memory: memory}
}

// goldenResolveCases cover every branch of the deduplication scan: an exact
// content duplicate, a duplicate that differs only in punctuation and casing,
// a duplicate whose stored record has an extractor-supplied canonical key (so
// only the content comparison can find it), same content under a different
// kind (which must not match), an explicit dedup key, an explicit match id,
// and a genuinely new fact.
func goldenResolveCases(now time.Time) []struct {
	name      string
	candidate Candidate
} {
	existing := corpusMemory(42, now)
	customKey := corpusMemory(customKeyRecordIndex, now)
	differentKind := candidateLike(42, existing.Content, now)
	differentKind.Memory.Kind = KindEpisodic
	if differentKind.Memory.Kind == existing.Kind {
		differentKind.Memory.Kind = KindProcedural
	}
	return []struct {
		name      string
		candidate Candidate
	}{
		{"exact_duplicate", candidateLike(42, existing.Content, now)},
		{"punctuation_duplicate", candidateLike(42, "  "+strings.ToUpper(existing.Content)+"!!  ", now)},
		{"content_duplicate_custom_key", candidateLike(customKeyRecordIndex, customKey.Content, now)},
		{"different_kind_same_content", differentKind},
		{"dedup_key", func() Candidate {
			candidate := candidateLike(42, "Совсем другой текст", now)
			candidate.DedupKey = existing.CanonicalKey
			return candidate
		}()},
		{"new_fact", candidateLike(42, "Совершенно новый факт, которого нет в корпусе", now)},
		{"explicit_match", func() Candidate {
			candidate := candidateLike(7, "Обновлённый текст", now)
			candidate.MatchID = corpusMemory(7, now).ID
			return candidate
		}()},
	}
}

// TestResolveExistingGolden pins which stored record each write candidate
// resolves to. A deduplication shortcut that matched a different record — or
// stopped matching — would create duplicate or wrongly merged memories, so it
// must fail here.
func TestResolveExistingGolden(t *testing.T) {
	var builder strings.Builder
	for _, testCase := range goldenResolveCases(benchNow) {
		store := newResolveStore(benchNow)
		engine := newBenchEngine(t, store, benchNow)
		candidate := testCase.candidate
		candidate.Memory.ID = "memory-candidate"
		normalized, err := Normalize(candidate.Memory, benchNow)
		if err != nil {
			t.Fatal(err)
		}
		if candidate.DedupKey != "" {
			normalized.CanonicalKey = strings.TrimSpace(candidate.DedupKey)
		}
		if normalized.CanonicalKey == "" {
			normalized.CanonicalKey = canonicalKey(normalized)
		}
		existing, found, err := engine.resolveExisting(context.Background(), candidate, normalized)
		if err != nil {
			t.Fatalf("%s: %v", testCase.name, err)
		}
		fmt.Fprintf(&builder, "%s found=%t id=%s content=%q key=%q\n",
			testCase.name, found, existing.ID, existing.Content, existing.CanonicalKey)
	}
	assertGolden(t, "resolve_existing.txt", builder.String())
}

// TestRememberGoldenOutcomes pins the end-to-end write decision, not just the
// match: operation, whether anything changed, and the resulting version.
func TestRememberGoldenOutcomes(t *testing.T) {
	var builder strings.Builder
	for _, testCase := range goldenResolveCases(benchNow) {
		store := newResolveStore(benchNow)
		engine := newBenchEngine(t, store, benchNow)
		result, err := engine.Remember(context.Background(), testCase.candidate)
		if err != nil {
			fmt.Fprintf(&builder, "%s error=%v\n", testCase.name, err)
			continue
		}
		fmt.Fprintf(&builder, "%s op=%s changed=%t created=%t id=%s version=%d reason=%q content=%q\n",
			testCase.name, result.Operation, result.Changed, result.Created,
			result.Memory.ID, result.Memory.Version, result.Reason, result.Memory.Content)
	}
	assertGolden(t, "remember_outcomes.txt", builder.String())
}

// TestProcessTurnGoldenDeduplicatesWithinOneTurn pins a property that any
// per-turn caching of the deduplication scan would break: two identical
// candidates extracted from the same turn must collapse onto one record, which
// requires the second candidate to observe the first candidate's write.
func TestProcessTurnGoldenDeduplicatesWithinOneTurn(t *testing.T) {
	store := newMemoryTestStore()
	content := "Пользователь переезжает в другой город весной"
	engine, err := NewEngine(Config{
		Store: store,
		Extractor: testExtractor{candidates: []Candidate{
			{Memory: baseMemory(content, benchNow)},
			{Memory: baseMemory(content+".", benchNow)},
		}},
		Now: func() time.Time { return benchNow }, IDs: &testIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	results, err := engine.ProcessTurn(context.Background(), Turn{
		RunID: "run_1", ConversationID: "conversation_1", Now: benchNow,
		Messages: []TranscriptMessage{{ID: "message_1", ConversationID: "conversation_1", Role: "user", Content: content, CreatedAt: benchNow}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected two write results, got %d", len(results))
	}
	if !results[0].Created {
		t.Fatalf("first candidate should create: %#v", results[0])
	}
	if results[1].Created {
		t.Fatalf("second candidate must not create a duplicate record: %#v", results[1])
	}
	if results[0].Memory.ID != results[1].Memory.ID {
		t.Fatalf("second candidate resolved to a different record: %s vs %s", results[0].Memory.ID, results[1].Memory.ID)
	}
	if len(store.memories) != 1 {
		t.Fatalf("expected exactly one stored memory, got %d", len(store.memories))
	}
}

// referenceLexicalScore is the original, obviously-correct implementation of
// LexicalScore, kept verbatim so the optimized path can be held to it. It is
// the oracle for TestLexicalScoreEquivalence; do not "simplify" it to call the
// production code, which would make the test vacuous.
func referenceLexicalScore(content, query string) float64 {
	query = strings.TrimSpace(query)
	if query == "" || strings.TrimSpace(content) == "" {
		return 0
	}
	queryTokens := tokenize(query)
	contentTokens := tokenize(content)
	if len(queryTokens) == 0 || len(contentTokens) == 0 {
		return 0
	}
	contentSet := make(map[string]struct{}, len(contentTokens))
	for _, token := range contentTokens {
		contentSet[token] = struct{}{}
	}
	matched := 0
	for _, token := range queryTokens {
		if _, ok := contentSet[token]; ok {
			matched++
		}
	}
	coverage := float64(matched) / float64(len(queryTokens))
	phrase := 0.0
	if strings.Contains(strings.ToLower(content), strings.ToLower(query)) {
		phrase = 0.25
	}
	return clamp01(coverage*0.75 + phrase)
}

// lexicalEquivalenceCorpus mixes ordinary records with the awkward cases:
// empty and whitespace-only content, content with no tokens at all, a query
// that matches as a phrase but shares no token with the content
// (tokenization splits "a-b" differently from "xa-by"), repeated query tokens,
// and mixed case in both directions.
func lexicalEquivalenceCorpus() (queries, contents []string) {
	queries = []string{
		"зелёный чай проект", "пользователь задачи", "квантовая хромодинамика",
		"в контексте задачи номер 42", "ЧАЙ", "чай чай", "  ", "", "-", "a-b",
		"номер", "Пользователь", "ПОЛЬЗОВАТЕЛЬ ЛЮБИТ", "42", "_", "ё", "Ё",
		// Wider than the bitmask fast path, so the counted fallback is covered.
		strings.Repeat("чай проект слово ", 30),
	}
	for index := range 120 {
		contents = append(contents, corpusMemory(index, benchNow).Content)
	}
	contents = append(contents,
		"", "   ", "---", "!!!", "xa-by", "a-b", "A-B punctuation edge",
		"ЧАЙ ЗЕЛЁНЫЙ", "чай", "Чай, зелёный: проект!", "under_score token",
		"Ёлка", "ёлка", "42", "no_tokens_here", "\xff\xfe invalid utf8 \xff",
	)
	return queries, contents
}

// TestLexicalScoreEquivalence keeps the optimized lexical path bit-identical
// to the original algorithm. Recall's ordering depends on this score, so any
// divergence is a behaviour change disguised as a speedup.
func TestLexicalScoreEquivalence(t *testing.T) {
	queries, contents := lexicalEquivalenceCorpus()
	for _, query := range queries {
		lexical := newLexicalQuery(query)
		for _, content := range contents {
			want := referenceLexicalScore(content, query)
			if got := lexical.score(content); want != got {
				t.Fatalf("lexicalQuery.score diverged for query %q content %q: got=%v want=%v", query, content, got, want)
			}
			if got := LexicalScore(content, query); want != got {
				t.Fatalf("LexicalScore diverged for query %q content %q: got=%v want=%v", query, content, got, want)
			}
		}
	}
}

// TestLexicalQueryScoreDoesNotAllocatePerRecord pins the property the M-24 fix
// exists for: scoring one record against a prepared query must not re-tokenize
// it. One allocation is permitted for the lowercased content that the phrase
// check needs; the original implementation used 44.
func TestLexicalQueryScoreDoesNotAllocatePerRecord(t *testing.T) {
	lexical := newLexicalQuery("зелёный чай проект")
	contents := make([]string, 256)
	for index := range contents {
		contents[index] = corpusMemory(index, benchNow).Content
	}
	allocs := testing.AllocsPerRun(200, func() {
		for _, content := range contents {
			_ = lexical.score(content)
		}
	})
	if perRecord := allocs / float64(len(contents)); perRecord > 1.01 {
		t.Fatalf("lexicalQuery.score allocates %.2f times per record, want at most 1", perRecord)
	}
}

// TestCanonicalTextEqualsMatchesSameContent holds the streaming comparison
// used by the deduplication scan to the materializing implementation. A false
// negative here silently creates duplicate memories; a false positive merges
// two unrelated facts.
func TestCanonicalTextEqualsMatchesSameContent(t *testing.T) {
	_, contents := lexicalEquivalenceCorpus()
	contents = append(contents, "Пользователь любит зелёный чай", " пользователь ЛЮБИТ, зелёный: чай! ")
	for _, left := range contents {
		canonical := canonicalText(left)
		for _, right := range contents {
			want := sameContent(left, right)
			if got := canonicalTextEquals(right, canonical); got != want {
				t.Fatalf("canonicalTextEquals(%q, canonicalText(%q)) = %t, want %t", right, left, got, want)
			}
		}
	}
}

// TestCanonicalTextEqualsDoesNotAllocate pins the reason the streaming
// comparison exists: the dedup scan runs it once per stored record.
func TestCanonicalTextEqualsDoesNotAllocate(t *testing.T) {
	canonical := canonicalText(corpusMemory(42, benchNow).Content)
	contents := make([]string, 256)
	for index := range contents {
		contents[index] = corpusMemory(index, benchNow).Content
	}
	allocs := testing.AllocsPerRun(200, func() {
		for _, content := range contents {
			_ = canonicalTextEquals(content, canonical)
		}
	})
	if allocs != 0 {
		t.Fatalf("canonicalTextEquals allocated %.2f times over %d records, want 0", allocs, len(contents))
	}
}

// TestAffectiveQueryMatchesAffectiveRelevance keeps the hoisted per-query
// affective decision identical to the per-record helper that
// engine_scoring_test.go pins, including its nature/kind early return.
func TestAffectiveQueryMatchesAffectiveRelevance(t *testing.T) {
	queries := []string{
		"какие у меня чувства к этой работе", "расскажи про мои эмоции вчера",
		"как развиваются наши отношения", "какое у меня было настроение",
		"how do I feel about this", "describe our relationship",
		"когда я последний раз коммитил", "what is the database schema", "",
	}
	memories := []domain.Memory{
		{Kind: domain.MemoryKindSemantic, Nature: domain.MemoryNatureFact, Valence: -0.75},
		{Kind: domain.MemoryKindSemantic, Nature: domain.MemoryNatureEmotion, Valence: 0.5},
		{Kind: domain.MemoryKindRelationship, Nature: domain.MemoryNatureFact, Valence: -0.25},
		{Kind: domain.MemoryKindEpisodic, Nature: domain.MemoryNatureOpinion, Valence: 0},
	}
	for _, query := range queries {
		affective := affectiveQuery(query)
		for _, memory := range memories {
			want := affectiveRelevance(query, memory)
			if got := affectiveRelevanceFor(affective, memory); got != want {
				t.Fatalf("affectiveRelevanceFor(%q, %+v) = %v, want %v", query, memory, got, want)
			}
		}
	}
}

// hashEmbedder is a deterministic, provider-free semantic projection: a
// token-hash bag of words. It exists so the vector overlay in Recall has test
// coverage at all — production wires a BruteForceIndex but no Embedder, so
// that branch is otherwise unreachable.
type hashEmbedder struct{ dims int }

func (h hashEmbedder) Embed(_ context.Context, inputs []string) ([][]float64, error) {
	result := make([][]float64, 0, len(inputs))
	for _, input := range inputs {
		vector := make([]float64, h.dims)
		for _, token := range tokenize(input) {
			digest := sha256.Sum256([]byte(token))
			vector[int(digest[0])%h.dims] += 1
		}
		// Keep every vector non-zero; validateVector rejects a zero vector.
		vector[0] += 0.5
		result = append(result, vector)
	}
	return result, nil
}

func (hashEmbedder) Version() string { return "test-hash-v1" }

// TestRecallGoldenWithVectorIndex pins the hybrid path and the vector-only
// path. Recall now builds its candidate set lazily from the records that
// actually carry a signal, so a record the lexical scorer scored 0 must still
// surface when the vector overlay matches it.
func TestRecallGoldenWithVectorIndex(t *testing.T) {
	cases := []struct {
		name           string
		query          string
		wantVectorOnly bool
	}{
		// Shares tokens with the corpus: lexical and vector signals mix.
		{"hybrid", "зелёный чай проект", false},
		// Shares no token with any record, so every surviving candidate was
		// created by the vector overlay alone.
		{"vector_only", "заварка напиток бариста", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store := newCorpusStore(goldenCorpusSize, benchNow)
			engine, err := NewEngine(Config{
				Store: store, Now: func() time.Time { return benchNow }, IDs: &testIDs{},
				Embedder: hashEmbedder{dims: 24}, Vectors: NewBruteForceIndex(),
				Ranker:       HybridRanker{Weights: RankWeights{Lexical: .35, Vector: .25, Recency: .15, Salience: .2, Affective: .05}},
				RecallBudget: Budget{MaxItems: 12, MaxChars: 6_000},
			})
			if err != nil {
				t.Fatal(err)
			}
			indexed, err := engine.RebuildVectorIndex(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if indexed == 0 {
				t.Fatal("expected the rebuild to index the corpus")
			}
			results := mustRecall(t, engine, testCase.query, RecallOptions{Mode: RecallAutomatic, Now: benchNow, Limit: 12})
			if len(results) == 0 {
				t.Fatal("expected the vector overlay to produce hits")
			}
			vectorOnly := 0
			for _, result := range results {
				if result.LexicalScore == 0 && result.VectorScore > 0 {
					vectorOnly++
				}
			}
			if testCase.wantVectorOnly && vectorOnly != len(results) {
				t.Fatalf("fixture no longer isolates the vector-only path: %d of %d results", vectorOnly, len(results))
			}
			assertGolden(t, "recall_vectors_"+testCase.name+".txt", formatRecall(results))
		})
	}
}

// countingStore counts store round trips so the M-24/M-25 claims can be
// asserted directly, rather than inferred from an allocation profile.
type countingStore struct {
	*memoryTestStore
	listMemories int
	listSources  int
	touches      int
}

func (s *countingStore) ListMemories(ctx context.Context, filter MemoryFilter) ([]domain.Memory, error) {
	s.listMemories++
	return s.memoryTestStore.ListMemories(ctx, filter)
}

func (s *countingStore) ListMemorySources(ctx context.Context, id domain.ID) ([]domain.MemorySource, error) {
	s.listSources++
	return s.memoryTestStore.ListMemorySources(ctx, id)
}

func (s *countingStore) TouchMemory(ctx context.Context, id domain.ID, at time.Time) error {
	s.touches++
	return s.memoryTestStore.TouchMemory(ctx, id, at)
}

// TestCoreSnapshotLoadsProvenanceOnlyForSelectedEntries is the M-25 assertion.
// Provenance used to be fetched for every ranked record and then thrown away
// for all but the budgeted prefix: one store query per stored memory, on every
// context assembly of every new run.
func TestCoreSnapshotLoadsProvenanceOnlyForSelectedEntries(t *testing.T) {
	store := &countingStore{memoryTestStore: newCorpusStore(goldenCorpusSize, benchNow)}
	engine := newBenchEngine(t, store, benchNow)
	snapshot, err := engine.CoreSnapshot(context.Background(), Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) == 0 {
		t.Fatal("expected a non-empty snapshot")
	}
	if store.listSources != len(snapshot.Entries) {
		t.Fatalf("loaded provenance %d times for %d entries over a %d-record store",
			store.listSources, len(snapshot.Entries), goldenCorpusSize)
	}
	// Every selected entry must still carry its provenance: the point is to
	// load less, not to render "unknown" sources into the prompt.
	for _, entry := range snapshot.Entries {
		if len(entry.Evidence.Sources) == 0 {
			t.Fatalf("entry %s lost its provenance", entry.Memory.ID)
		}
	}
	if strings.Contains(snapshot.Text, "source=\"unknown\"") {
		t.Fatalf("snapshot rendered an unknown source: %s", snapshot.Text)
	}
}

// TestRecallLoadsProvenanceOnlyForReturnedResults pins the same bound on the
// read path, which already had it, so a future change cannot reintroduce the
// unbounded load there either.
func TestRecallLoadsProvenanceOnlyForReturnedResults(t *testing.T) {
	store := &countingStore{memoryTestStore: newCorpusStore(goldenCorpusSize, benchNow)}
	engine := newBenchEngine(t, store, benchNow)
	results := mustRecall(t, engine, "зелёный чай проект", RecallOptions{Mode: RecallAutomatic, Now: benchNow, Limit: 12})
	if len(results) == 0 {
		t.Fatal("expected recall hits")
	}
	if store.listSources != len(results) || store.touches != len(results) {
		t.Fatalf("sources=%d touches=%d for %d results", store.listSources, store.touches, len(results))
	}
	if store.listMemories != 1 {
		t.Fatalf("expected one store listing per recall, got %d", store.listMemories)
	}
}

// TestProcessTurnScansOncePerCandidate documents the remaining M-26 cost that
// was deliberately not removed: the deduplication scan still reads the store
// once per written candidate, because a later candidate in the same turn must
// observe an earlier candidate's write (see
// TestProcessTurnGoldenDeduplicatesWithinOneTurn). Only the per-record
// tokenization was removed. If this ever becomes one scan per turn, that test
// is the one that must still hold.
func TestProcessTurnScansOncePerCandidate(t *testing.T) {
	store := &countingStore{memoryTestStore: newMemoryTestStore()}
	engine, err := NewEngine(Config{
		Store: store,
		Extractor: testExtractor{candidates: []Candidate{
			{Memory: baseMemory("Первый новый факт", benchNow)},
			{Memory: baseMemory("Второй новый факт", benchNow)},
			{Memory: baseMemory("Третий новый факт", benchNow)},
		}},
		Now: func() time.Time { return benchNow }, IDs: &testIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ProcessTurn(context.Background(), Turn{
		RunID: "run_1", ConversationID: "conversation_1", Now: benchNow,
		Messages: []TranscriptMessage{{ID: "message_1", ConversationID: "conversation_1", Role: "user", Content: "текст", CreatedAt: benchNow}},
	}); err != nil {
		t.Fatal(err)
	}
	if store.listMemories != 3 {
		t.Fatalf("expected one deduplication scan per candidate, got %d", store.listMemories)
	}
}

// TestRecallDoesNotTokenizePerStoredRecord is the M-24 assertion. Recall used
// to call LexicalScore(item.Content, query) for every record in the store,
// which re-tokenized the query and the record's immutable content on each
// call: 44 allocations per record, so ~210k allocations for a 5k-record store.
// The query side is now prepared once and the content is streamed.
func TestRecallDoesNotTokenizePerStoredRecord(t *testing.T) {
	store := newCorpusStore(goldenCorpusSize, benchNow)
	engine := newBenchEngine(t, storeOnly{Store: store}, benchNow)
	ctx := context.Background()
	allocs := testing.AllocsPerRun(20, func() {
		if _, err := engine.Recall(ctx, "зелёный чай проект", RecallOptions{Mode: RecallAutomatic, Now: benchNow, Limit: 12}); err != nil {
			t.Fatal(err)
		}
	})
	if perRecord := allocs / float64(goldenCorpusSize); perRecord > 3 {
		t.Fatalf("Recall allocates %.1f times per stored record (%.0f total over %d records), want at most 3",
			perRecord, allocs, goldenCorpusSize)
	}
}

// TestResolveExistingAllocationsDoNotScaleWithStoreSize is the M-26
// assertion. The deduplication scan used to canonicalize the candidate's
// content once per stored record and materialize each record's canonical form
// only to compare and discard it. Its allocation count is now independent of
// how much the owner has remembered.
func TestResolveExistingAllocationsDoNotScaleWithStoreSize(t *testing.T) {
	ctx := context.Background()
	measure := func(size int) float64 {
		store := newCorpusStore(size, benchNow)
		engine := newBenchEngine(t, store, benchNow)
		candidate := candidateLike(0, "Совершенно новый факт, которого нет в корпусе", benchNow)
		candidate.Memory.ID = "memory-candidate"
		normalized, err := Normalize(candidate.Memory, benchNow)
		if err != nil {
			t.Fatal(err)
		}
		normalized.CanonicalKey = canonicalKey(normalized)
		return testing.AllocsPerRun(50, func() {
			if _, found, err := engine.resolveExisting(ctx, candidate, normalized); err != nil {
				t.Fatal(err)
			} else if found {
				t.Fatal("candidate must not match the corpus")
			}
		})
	}
	small, large := measure(100), measure(goldenCorpusSize)
	if large > small+8 {
		t.Fatalf("resolveExisting allocations scale with store size: %.0f at 100 records, %.0f at %d records",
			small, large, goldenCorpusSize)
	}
}
