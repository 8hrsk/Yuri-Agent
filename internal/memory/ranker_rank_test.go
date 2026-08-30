package memory

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// rankGoldenNow is the fixed clock for the Rank fixtures. It is deliberately
// separate from benchNow so a change to the recall corpus cannot move the
// ranker fixture underneath it.
var rankGoldenNow = time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)

// rankGoldenCandidates is a small corpus built to exercise every branch of
// Rank at once: exact score ties between distinct ids (which must fall through
// to the id tie-break), duplicate ids carrying different scores (highest must
// win), a duplicate id carrying an identical score (first must win), an empty
// id (must be dropped), a dormant record, and out-of-range signals that must be
// clamped. A map-to-slice change is exactly where an unstable order hides, so
// the fixture pins the full order and every per-signal score, not just the set.
func rankGoldenCandidates() []RankCandidate {
	memoryAt := func(id string, kind Kind, salience, confidence, valence float64, age time.Duration, state LifecycleState) Memory {
		return domain.Memory{
			ID: domain.ID(id), AgentID: domain.ID("agent-1"), Version: 1,
			Scope: domain.MemoryScopeAgentPrivate, Kind: kind, Nature: domain.MemoryNatureFact,
			Content: "запись " + id, Confidence: confidence, Salience: salience, Valence: valence,
			Sensitivity: domain.MemorySensitivityPrivate, Retention: domain.MemoryRetentionDecay,
			Lifecycle: state,
			CreatedAt: rankGoldenNow.Add(-age), UpdatedAt: rankGoldenNow.Add(-age),
		}
	}
	return []RankCandidate{
		// Two distinct ids with identical inputs: the score tie must be broken
		// by id, ascending, regardless of the order they arrive in.
		{Memory: memoryAt("tie-b", KindSemantic, 0.5, 0.8, 0.1, 48*time.Hour, StateActive), LexicalScore: 0.5, VectorScore: 0.5, AffectiveRelevance: 0.5},
		{Memory: memoryAt("tie-a", KindSemantic, 0.5, 0.8, 0.1, 48*time.Hour, StateActive), LexicalScore: 0.5, VectorScore: 0.5, AffectiveRelevance: 0.5},
		{Memory: memoryAt("tie-c", KindSemantic, 0.5, 0.8, 0.1, 48*time.Hour, StateActive), LexicalScore: 0.5, VectorScore: 0.5, AffectiveRelevance: 0.5},
		// A duplicate id whose second occurrence scores higher: the higher one
		// must win, and it must appear exactly once.
		{Memory: memoryAt("dup-higher", KindSemantic, 0.2, 0.5, 0, 72*time.Hour, StateActive), LexicalScore: 0.1, VectorScore: 0.1},
		{Memory: memoryAt("dup-higher", KindSemantic, 0.9, 0.9, 0.4, 1*time.Hour, StateActive), LexicalScore: 0.95, VectorScore: 0.9, AffectiveRelevance: 0.8, Evidence: SourceEvidence{Snippet: "выигравший фрагмент"}},
		// A duplicate id whose second occurrence scores lower: the first must
		// survive, together with its evidence.
		{Memory: memoryAt("dup-lower", KindEpisodic, 0.8, 0.9, -0.6, 2*time.Hour, StateActive), LexicalScore: 0.7, VectorScore: 0.6, AffectiveRelevance: 0.5, Evidence: SourceEvidence{Snippet: "первый фрагмент"}},
		{Memory: memoryAt("dup-lower", KindEpisodic, 0.1, 0.2, 0, 900*time.Hour, StateActive), LexicalScore: 0.05},
		// A duplicate id scoring exactly the same twice: first occurrence wins,
		// so its evidence is what survives.
		{Memory: memoryAt("dup-equal", KindProcedural, 0.4, 0.6, 0.2, 24*time.Hour, StateActive), LexicalScore: 0.3, VectorScore: 0.3, AffectiveRelevance: 0.2, Evidence: SourceEvidence{Snippet: "первое равное"}},
		{Memory: memoryAt("dup-equal", KindProcedural, 0.4, 0.6, 0.2, 24*time.Hour, StateActive), LexicalScore: 0.3, VectorScore: 0.3, AffectiveRelevance: 0.2, Evidence: SourceEvidence{Snippet: "второе равное"}},
		// Empty and whitespace-only ids are not rankable and must be dropped.
		{Memory: memoryAt("", KindSemantic, 0.9, 0.9, 0, time.Hour, StateActive), LexicalScore: 0.99},
		{Memory: memoryAt("   ", KindSemantic, 0.9, 0.9, 0, time.Hour, StateActive), LexicalScore: 0.99},
		// A dormant record must still rank, flagged as dormant.
		{Memory: memoryAt("dormant-1", KindEpisodic, 0.6, 0.7, 0.3, 200*time.Hour, StateDormant), LexicalScore: 0.4, VectorScore: 0.2, AffectiveRelevance: 0.3},
		// Out-of-range inputs must be clamped rather than propagated.
		{Memory: memoryAt("clamped-high", KindCore, 1.5, 1.4, 2.2, 0, StateActive), LexicalScore: 4.2, VectorScore: 1.9, AffectiveRelevance: 3.3},
		{Memory: memoryAt("clamped-low", KindCore, -0.4, -0.9, -3.1, 5000*time.Hour, StateActive), LexicalScore: -2, VectorScore: -1, AffectiveRelevance: -7},
		// A record whose freshness anchor is in the future saturates recency.
		{Memory: memoryAt("future-1", KindSemantic, 0.3, 0.5, 0, -10*time.Hour, StateActive), LexicalScore: 0.25, VectorScore: 0.25},
	}
}

func formatRank(results []RecallResult) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "results=%d\n", len(results))
	for _, result := range results {
		fmt.Fprintf(&builder,
			"%s score=%.17g lexical=%.17g vector=%.17g recency=%.17g salience=%.17g affective=%.17g dormant=%t snippet=%q sources=%d\n",
			result.Memory.ID, result.Score, result.LexicalScore, result.VectorScore, result.RecencyScore,
			result.SalienceScore, result.AffectiveScore, result.Dormant, result.Evidence.Snippet, len(result.Evidence.Sources))
	}
	return builder.String()
}

// TestRankGolden pins Rank's own output — the full order, every per-signal
// score at full float precision, tie-breaking, and which duplicate survives.
// Recall and CoreSnapshot goldens pin Rank only through a budget that truncates
// most of its output; this fixture sees all of it.
func TestRankGolden(t *testing.T) {
	ranker := HybridRanker{Weights: DefaultRankWeights()}
	assertGolden(t, "rank.txt", formatRank(ranker.Rank(rankGoldenCandidates(), rankGoldenNow)))
}

// TestRankDeduplicatesByID states the dedup contract directly, so a change that
// drops deduplication or flips which duplicate wins fails with a readable
// message rather than only as a golden diff.
func TestRankDeduplicatesByID(t *testing.T) {
	ranker := HybridRanker{Weights: DefaultRankWeights()}
	results := ranker.Rank(rankGoldenCandidates(), rankGoldenNow)
	seen := make(map[domain.ID]RecallResult, len(results))
	for _, result := range results {
		if previous, exists := seen[result.Memory.ID]; exists {
			t.Fatalf("id %s ranked twice: scores %g and %g", result.Memory.ID, previous.Score, result.Score)
		}
		seen[result.Memory.ID] = result
	}
	if _, exists := seen[domain.ID("")]; exists {
		t.Fatal("empty id was ranked")
	}
	// Highest score wins: the second dup-higher candidate carries the winning
	// snippet and a much larger salience.
	if got := seen[domain.ID("dup-higher")].Evidence.Snippet; got != "выигравший фрагмент" {
		t.Errorf("dup-higher kept the losing duplicate: snippet %q", got)
	}
	if got := seen[domain.ID("dup-higher")].Memory.Salience; got != 0.9 {
		t.Errorf("dup-higher kept the losing duplicate: salience %g", got)
	}
	// Lower-scoring later duplicate must not overwrite the winner.
	if got := seen[domain.ID("dup-lower")].Evidence.Snippet; got != "первый фрагмент" {
		t.Errorf("dup-lower was overwritten by the lower-scoring duplicate: snippet %q", got)
	}
	// An exact tie between duplicates keeps the first occurrence.
	if got := seen[domain.ID("dup-equal")].Evidence.Snippet; got != "первое равное" {
		t.Errorf("dup-equal did not keep the first occurrence: snippet %q", got)
	}
}

// rankAllocCandidates builds count distinct, uniquely scored candidates.
func rankAllocCandidates(count int) []RankCandidate {
	candidates := make([]RankCandidate, count)
	for index := range candidates {
		candidates[index] = RankCandidate{
			Memory: domain.Memory{
				ID: domain.ID(fmt.Sprintf("memory-%06d", index)), AgentID: domain.ID("agent-1"), Version: 1,
				Scope: domain.MemoryScopeAgentPrivate, Kind: domain.MemoryKindSemantic, Nature: domain.MemoryNatureFact,
				Content: "проектный факт", Confidence: 0.85, Salience: float64(index%100) / 100,
				Sensitivity: domain.MemorySensitivityPrivate, Retention: domain.MemoryRetentionDecay,
				Lifecycle: domain.MemoryLifecycleActive,
				CreatedAt: rankGoldenNow.Add(-time.Duration(index) * time.Hour),
				UpdatedAt: rankGoldenNow.Add(-time.Duration(index) * time.Hour),
			},
			LexicalScore: float64(index%10) / 10, VectorScore: float64(index%17) / 17,
			AffectiveRelevance: float64(index%7) / 7,
		}
	}
	return candidates
}

// TestRankAllocationsDoNotScaleWithCandidateCount is the allocation-bound
// control for M-23. Rank must allocate a bounded number of blocks — the result
// slice, the index map and the sort — no matter how many candidates it is
// handed. The previous shape stored RecallResult (560 bytes, well past the
// 128-byte threshold at which Go stores map values indirectly) as a map value,
// which cost one heap allocation per candidate; that shape fails here, because
// the allocation count then grows by ~1800 between the two sizes.
func TestRankAllocationsDoNotScaleWithCandidateCount(t *testing.T) {
	ranker := HybridRanker{Weights: DefaultRankWeights()}
	measure := func(count int) float64 {
		candidates := rankAllocCandidates(count)
		return testing.AllocsPerRun(5, func() {
			if got := len(ranker.Rank(candidates, rankGoldenNow)); got != count {
				t.Fatalf("ranked %d of %d candidates", got, count)
			}
		})
	}
	small, large := measure(200), measure(2_000)
	t.Logf("allocs: 200 candidates = %.0f, 2000 candidates = %.0f", small, large)
	// A per-candidate allocation shows up here as a difference of ~1800. Room
	// is left for map growth and sort internals, which are logarithmic at
	// worst, but not for anything linear in the candidate count.
	if large-small > 32 {
		t.Errorf("Rank allocations scale with candidate count: %.0f allocs for 200 candidates, %.0f for 2000 (delta %.0f)", small, large, large-small)
	}
	if large > 64 {
		t.Errorf("Rank made %.0f allocations for 2000 candidates; expected a bounded handful", large)
	}
}

// TestRankOrderIsDeterministicUnderNearTies guards a hazard that the map-based
// shape carried: the comparator treats scores within 1e-12 as equal, which
// makes it non-transitive across a chain of near-equal scores, and a
// non-transitive comparator lets sort.Slice depend on the order of its input.
// Draining a Go map supplies that order at random. Building results in
// candidate order makes the input deterministic, so equal inputs must now rank
// equally on every run.
func TestRankOrderIsDeterministicUnderNearTies(t *testing.T) {
	// Lexical-only weights make the score exactly the lexical input, so the
	// chain below is separated by less than the comparator's 1e-12 epsilon
	// pairwise, but by more than it end to end.
	ranker := HybridRanker{Weights: RankWeights{Lexical: 1}}
	base := 0.5
	candidates := []RankCandidate{
		{Memory: domain.Memory{ID: domain.ID("near-a"), Version: 1}, LexicalScore: base},
		{Memory: domain.Memory{ID: domain.ID("near-b"), Version: 1}, LexicalScore: base + 6e-13},
		{Memory: domain.Memory{ID: domain.ID("near-c"), Version: 1}, LexicalScore: base + 1.2e-12},
	}
	order := func() string {
		ids := make([]string, 0, len(candidates))
		for _, result := range ranker.Rank(candidates, rankGoldenNow) {
			ids = append(ids, result.Memory.ID.String())
		}
		return strings.Join(ids, ",")
	}
	first := order()
	for attempt := range 200 {
		if got := order(); got != first {
			t.Fatalf("Rank returned a different order on run %d: %q then %q", attempt, first, got)
		}
	}
}
