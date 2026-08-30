package sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// searchSeed describes one memory to index for the search N+1 regressions.
// Padding controls how many runes the content carries without changing how
// many tokens FTS5 indexes: "beacon" plus one padding word is always two
// tokens, so every seeded row scores the same bm25 rank and the deliberate
// salience ladder alone decides the result order. That separates the two
// things these tests need to control independently - result order and the
// approximate token cost that the MaxTokens budget spends.
type searchSeed struct {
	id       string
	padding  int
	salience float64
	sources  int
}

func searchSeedContent(padding int) string {
	return "beacon " + strings.Repeat("z", padding)
}

// seedSearchMemories writes the seeds in order and returns them unchanged.
// Sources are attached with ascending created_at so their stored order is
// unambiguous.
func seedSearchMemories(t *testing.T, repository *MemoryRepository, ctx context.Context, at time.Time, seeds []searchSeed) {
	t.Helper()
	for _, seed := range seeds {
		memory := domain.Memory{
			ID: domain.ID(seed.id), AgentID: "owner", Scope: domain.MemoryScopeAgentPrivate,
			Kind: domain.MemoryKindSemantic, Nature: domain.MemoryNatureFact,
			Content: searchSeedContent(seed.padding), Summary: "",
			Confidence: 0.7, Salience: seed.salience, Valence: 0.2,
			Sensitivity: domain.MemorySensitivityPrivate, Retention: domain.MemoryRetentionDecay,
			Lifecycle: domain.MemoryLifecycleActive, CanonicalKey: seed.id + "-key",
			CreatedAt: at, UpdatedAt: at,
		}
		sources := make([]domain.MemorySource, 0, seed.sources)
		for index := 0; index < seed.sources; index++ {
			sources = append(sources, domain.MemorySource{
				ID:          domain.ID(fmt.Sprintf("%s-source-%d", seed.id, index)),
				MemoryID:    memory.ID,
				SourceType:  "message",
				SourceID:    domain.ID(fmt.Sprintf("%s-evidence-%d", seed.id, index)),
				ExcerptHash: fmt.Sprintf("sha256:%s-%d", seed.id, index),
				CreatedAt:   at.Add(time.Duration(index) * time.Minute),
			})
		}
		if err := repository.Create(ctx, memory, sources); err != nil {
			t.Fatalf("create %s: %v", seed.id, err)
		}
	}
}

func assertSeededHit(t *testing.T, index int, hit MemorySearchHit, seed searchSeed, at time.Time) {
	t.Helper()
	item := hit.Memory
	if string(item.ID) != seed.id {
		t.Fatalf("hit %d id = %q, want %q", index, item.ID, seed.id)
	}
	if item.AgentID != "owner" || item.Scope != domain.MemoryScopeAgentPrivate || item.Version != 1 {
		t.Fatalf("hit %d identity = %q/%q/v%d", index, item.AgentID, item.Scope, item.Version)
	}
	if item.Kind != domain.MemoryKindSemantic || item.Nature != domain.MemoryNatureFact {
		t.Fatalf("hit %d classification = %q/%q", index, item.Kind, item.Nature)
	}
	if item.Content != searchSeedContent(seed.padding) || item.Summary != "" || item.ContentJSON != "" {
		t.Fatalf("hit %d content = %q / summary %q / json %q", index, item.Content, item.Summary, item.ContentJSON)
	}
	if item.Confidence != 0.7 || item.Salience != seed.salience || item.Valence != 0.2 {
		t.Fatalf("hit %d scores = %v/%v/%v", index, item.Confidence, item.Salience, item.Valence)
	}
	if item.Sensitivity != domain.MemorySensitivityPrivate || item.Retention != domain.MemoryRetentionDecay ||
		item.Lifecycle != domain.MemoryLifecycleActive {
		t.Fatalf("hit %d policy = %q/%q/%q", index, item.Sensitivity, item.Retention, item.Lifecycle)
	}
	if item.Pinned || item.HiddenFromCore || item.CanonicalKey != seed.id+"-key" {
		t.Fatalf("hit %d flags = %v/%v/%q", index, item.Pinned, item.HiddenFromCore, item.CanonicalKey)
	}
	if item.AccessCount != 0 || !item.LastAccessedAt.IsZero() || !item.LastRecalledAt.IsZero() {
		t.Fatalf("hit %d recall counters = %d/%v/%v", index, item.AccessCount, item.LastAccessedAt, item.LastRecalledAt)
	}
	if !item.CreatedAt.Equal(at) || !item.UpdatedAt.Equal(at) {
		t.Fatalf("hit %d timestamps = %v/%v, want %v", index, item.CreatedAt, item.UpdatedAt, at)
	}
	if !item.DormantAt.IsZero() || !item.DeletedAt.IsZero() {
		t.Fatalf("hit %d lifecycle timestamps = %v/%v", index, item.DormantAt, item.DeletedAt)
	}
	if !strings.Contains(hit.Snippet, "[beacon]") {
		t.Fatalf("hit %d snippet = %q, want the matched term marked", index, hit.Snippet)
	}
	// Score is the negated bm25 rank, and FTS5 bm25 is negative for a match,
	// so a real hit always scores positive under this sign convention.
	if hit.Score <= 0 {
		t.Fatalf("hit %d score = %v, want a positive negated bm25 rank", index, hit.Score)
	}
	if hit.Sources == nil {
		t.Fatalf("hit %d sources are nil, want an empty non-nil slice", index)
	}
	if len(hit.Sources) != seed.sources {
		t.Fatalf("hit %d has %d sources, want %d", index, len(hit.Sources), seed.sources)
	}
	for position, source := range hit.Sources {
		if string(source.ID) != fmt.Sprintf("%s-source-%d", seed.id, position) {
			t.Fatalf("hit %d source %d id = %q", index, position, source.ID)
		}
		if source.MemoryID != item.ID || source.MemoryVersion != 1 {
			t.Fatalf("hit %d source %d revision = %q v%d, want %q v1", index, position, source.MemoryID, source.MemoryVersion, item.ID)
		}
		if source.SourceType != "message" || string(source.SourceID) != fmt.Sprintf("%s-evidence-%d", seed.id, position) {
			t.Fatalf("hit %d source %d = %q/%q", index, position, source.SourceType, source.SourceID)
		}
		if source.ExcerptHash != fmt.Sprintf("sha256:%s-%d", seed.id, position) {
			t.Fatalf("hit %d source %d excerpt = %q", index, position, source.ExcerptHash)
		}
		if !source.RunID.Empty() || !source.ConversationID.Empty() || !source.MessageID.Empty() {
			t.Fatalf("hit %d source %d carries unexpected links %q/%q/%q", index, position, source.RunID, source.ConversationID, source.MessageID)
		}
		if want := at.Add(time.Duration(position) * time.Minute); !source.CreatedAt.Equal(want) {
			t.Fatalf("hit %d source %d created_at = %v, want %v", index, position, source.CreatedAt, want)
		}
	}
}

// TestMemorySearchReadsSourcesInOneQueryAndKeepsOrder is the H-16 safety
// property for memory search: Search resolved provenance with one
// ListSources call per surviving hit, so N hits cost N+1 round-trips on a
// pool that is deliberately a single connection. The whole result must cost
// one search query plus one provenance query no matter how many hits it
// carries, and it must still return the same order, the same fields and the
// same per-hit sources.
func TestMemorySearchReadsSourcesInOneQueryAndKeepsOrder(t *testing.T) {
	database, ctx := countingDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	const hits = 12
	seeds := make([]searchSeed, 0, hits)
	for index := 0; index < hits; index++ {
		seeds = append(seeds, searchSeed{
			id:       fmt.Sprintf("search-hit-%02d", index),
			padding:  9,
			salience: 0.9 - float64(index)/100,
			// 0, 1 and 2 sources in rotation: a hit with no provenance must
			// keep an empty non-nil slice, exactly as ListSources returns.
			sources: index % 3,
		})
	}
	seedSearchMemories(t, repositories.Memories, ctx, at, seeds)

	var found []MemorySearchHit
	queries := countQueries(func() {
		found, err = repositories.Memories.Search(ctx, "beacon", MemorySearchOptions{Limit: 50})
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries != 2 {
		t.Fatalf("Search over %d hits issued %d queries, want 2 (one search, one provenance read)", hits, queries)
	}
	if len(found) != hits {
		t.Fatalf("search returned %d hits, want %d", len(found), hits)
	}
	// Equal bm25 rank for every row, so the salience ladder decides the order.
	for index, hit := range found {
		assertSeededHit(t, index, hit, seeds[index], at)
	}

	// Half the corpus must cost the same two queries: the count is a constant,
	// not a function of the hit count.
	var half []MemorySearchHit
	halfQueries := countQueries(func() {
		half, err = repositories.Memories.Search(ctx, "beacon", MemorySearchOptions{Limit: hits / 2})
	})
	if err != nil {
		t.Fatal(err)
	}
	if halfQueries != 2 {
		t.Fatalf("Search over %d hits issued %d queries, want 2", hits/2, halfQueries)
	}
	if len(half) != hits/2 {
		t.Fatalf("limited search returned %d hits, want %d", len(half), hits/2)
	}
	for index, hit := range half {
		assertSeededHit(t, index, hit, seeds[index], at)
	}
}

// TestMemorySearchTokenBudgetSkipsHitsWithoutExtraQueries pins the MaxTokens
// path. The budget decides the final hit set in Go, after the rows are read,
// so provenance must be resolved for exactly that set - the skipped hits stay
// out of the result, the scan keeps going past a hit it could not afford, and
// the query count does not move.
func TestMemorySearchTokenBudgetSkipsHitsWithoutExtraQueries(t *testing.T) {
	database, ctx := countingDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC)
	// approximateTokens rounds up per field: content of 16 runes costs 4 and
	// content of 80 runes costs 20, while the empty summary costs nothing.
	// Padding does not change the indexed token count, so the salience ladder
	// still fixes the order and the expensive rows sit in the middle of it.
	seeds := []searchSeed{
		{id: "budget-0", padding: 6, salience: 0.90, sources: 2},
		{id: "budget-1", padding: 6, salience: 0.89, sources: 1},
		{id: "budget-2", padding: 70, salience: 0.88, sources: 2},
		{id: "budget-3", padding: 6, salience: 0.87, sources: 0},
		{id: "budget-4", padding: 70, salience: 0.86, sources: 1},
		{id: "budget-5", padding: 6, salience: 0.85, sources: 2},
	}
	seedSearchMemories(t, repositories.Memories, ctx, at, seeds)

	var full []MemorySearchHit
	fullQueries := countQueries(func() {
		full, err = repositories.Memories.Search(ctx, "beacon", MemorySearchOptions{Limit: 50})
	})
	if err != nil {
		t.Fatal(err)
	}
	if fullQueries != 2 {
		t.Fatalf("unbudgeted search issued %d queries, want 2", fullQueries)
	}
	if len(full) != len(seeds) {
		t.Fatalf("unbudgeted search returned %d hits, want %d", len(full), len(seeds))
	}
	for index, hit := range full {
		assertSeededHit(t, index, hit, seeds[index], at)
	}

	// A 16 token budget buys 4 + 4, cannot afford budget-2 at 20, keeps
	// scanning, buys budget-3, cannot afford budget-4, and still buys
	// budget-5 exactly at the limit.
	var budgeted []MemorySearchHit
	budgetQueries := countQueries(func() {
		budgeted, err = repositories.Memories.Search(ctx, "beacon", MemorySearchOptions{Limit: 50, MaxTokens: 16})
	})
	if err != nil {
		t.Fatal(err)
	}
	if budgetQueries != fullQueries {
		t.Fatalf("budgeted search issued %d queries, want %d", budgetQueries, fullQueries)
	}
	wantOrder := []int{0, 1, 3, 5}
	if len(budgeted) != len(wantOrder) {
		got := make([]string, 0, len(budgeted))
		for _, hit := range budgeted {
			got = append(got, string(hit.Memory.ID))
		}
		t.Fatalf("budgeted search returned %v, want the affordable hits only", got)
	}
	for position, seedIndex := range wantOrder {
		assertSeededHit(t, position, budgeted[position], seeds[seedIndex], at)
	}
	for _, hit := range budgeted {
		if hit.Memory.ID == "budget-2" || hit.Memory.ID == "budget-4" {
			t.Fatalf("over-budget hit %q survived the token filter", hit.Memory.ID)
		}
	}
}

// TestMemorySearchChunksLargeSourceLookups covers the bound on a single
// provenance lookup: a result set wider than one chunk pays one extra
// round-trip per chunk, never one per hit.
func TestMemorySearchChunksLargeSourceLookups(t *testing.T) {
	if testing.Short() {
		t.Skip("seeding more than one provenance chunk is slow")
	}
	database, ctx := countingDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	total := maxSourceLookupPairs + 5
	seeds := make([]searchSeed, 0, total)
	for index := 0; index < total; index++ {
		seeds = append(seeds, searchSeed{
			id:       fmt.Sprintf("wide-hit-%04d", index),
			padding:  9,
			salience: 0.9,
			sources:  index % 2,
		})
	}
	seedSearchMemories(t, repositories.Memories, ctx, at, seeds)

	var found []MemorySearchHit
	queries := countQueries(func() {
		found, err = repositories.Memories.Search(ctx, "beacon", MemorySearchOptions{Limit: total})
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != total {
		t.Fatalf("wide search returned %d hits, want %d", len(found), total)
	}
	if queries != 3 {
		t.Fatalf("wide search issued %d queries, want 3 (one search, two provenance chunks)", queries)
	}
	sourced := 0
	for _, hit := range found {
		if hit.Sources == nil {
			t.Fatalf("hit %q sources are nil, want an empty non-nil slice", hit.Memory.ID)
		}
		sourced += len(hit.Sources)
		for _, source := range hit.Sources {
			if source.MemoryID != hit.Memory.ID || source.MemoryVersion != hit.Memory.Version {
				t.Fatalf("hit %q carries a source for %q v%d", hit.Memory.ID, source.MemoryID, source.MemoryVersion)
			}
		}
	}
	if sourced != total/2 {
		t.Fatalf("wide search attached %d sources, want %d", sourced, total/2)
	}
}
