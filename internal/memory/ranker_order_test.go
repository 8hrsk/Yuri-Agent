package memory

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// nearTieCandidates builds the corpus that the old epsilon comparator could not
// order consistently: a chain of scores whose neighbours are closer together
// than 1e-12 but whose endpoints are further apart than that, so "tied" is not
// an equivalence relation over it. Ids run opposite to scores, because the
// 3-cycle only appears when the id tie-break disagrees with the score order.
// Exact duplicates of some scores are included so the genuine-tie path — the
// one the id tie-break exists for — is exercised too.
func nearTieCandidates() []RankCandidate {
	const base = 0.5
	// A step well under the old 1e-12 epsilon: neighbours were "tied", but the
	// ends of the chain were not.
	const step = 2e-13
	candidates := make([]RankCandidate, 0, 24)
	for index := range 12 {
		score := base + float64(index)*step
		// Two ids per score, named so that ascending id order runs *with*
		// ascending score order and therefore against the score-descending
		// rank. That disagreement is what closes the epsilon comparator's
		// 3-cycle: a<b and b<c by id because each neighbouring pair is "tied",
		// while c<a by score because the ends of the chain are not.
		for _, suffix := range []string{"x", "y"} {
			id := fmt.Sprintf("near-%02d%s", index, suffix)
			candidates = append(candidates, RankCandidate{
				Memory:       domain.Memory{ID: domain.ID(id), Version: 1},
				LexicalScore: score,
			})
		}
	}
	return candidates
}

// nearTieRanker weights lexical alone, so the score is exactly the lexical
// input and the corpus above means what it says.
func nearTieRanker() HybridRanker {
	return HybridRanker{Weights: RankWeights{Lexical: 1}}
}

func rankedIDs(results []RecallResult) string {
	ids := make([]string, 0, len(results))
	for _, result := range results {
		ids = append(ids, result.Memory.ID.String())
	}
	return strings.Join(ids, ",")
}

// N-17. The defect was a property of the comparator, so it is tested as one.
// rankLess must be a strict total order: irreflexive, asymmetric, total over
// distinct results, and transitive. The epsilon comparator satisfied the first
// three and failed the fourth, which is what let sort.Slice depend on input
// order.
func TestRankLessIsAStrictTotalOrder(t *testing.T) {
	results := nearTieRanker().Rank(nearTieCandidates(), rankGoldenNow)
	// A NaN score cannot arrive from Rank's own clamping, but an exported
	// caller can supply one, and it is the other way a comparator stops being
	// a total order. Cover it in the same sweep.
	results = append(results, RecallResult{
		Memory: domain.Memory{ID: domain.ID("nan-1"), Version: 1}, Score: math.NaN(),
	}, RecallResult{
		Memory: domain.Memory{ID: domain.ID("nan-2"), Version: 1}, Score: math.NaN(),
	})
	if len(results) < 20 {
		t.Fatalf("corpus too small to be meaningful: %d results", len(results))
	}

	for i := range results {
		if rankLess(results[i], results[i]) {
			t.Fatalf("irreflexivity: rankLess(%s, %s) = true", results[i].Memory.ID, results[i].Memory.ID)
		}
		for j := range results {
			if i == j {
				continue
			}
			ij, ji := rankLess(results[i], results[j]), rankLess(results[j], results[i])
			if ij && ji {
				t.Fatalf("asymmetry: %s and %s are each less than the other", results[i].Memory.ID, results[j].Memory.ID)
			}
			if !ij && !ji {
				t.Fatalf("totality: %s and %s are incomparable", results[i].Memory.ID, results[j].Memory.ID)
			}
		}
	}

	for i := range results {
		for j := range results {
			if !rankLess(results[i], results[j]) {
				continue
			}
			for k := range results {
				if !rankLess(results[j], results[k]) {
					continue
				}
				if !rankLess(results[i], results[k]) {
					t.Fatalf("transitivity: %s < %s < %s but not %s < %s (scores %.17g, %.17g, %.17g)",
						results[i].Memory.ID, results[j].Memory.ID, results[k].Memory.ID,
						results[i].Memory.ID, results[k].Memory.ID,
						results[i].Score, results[j].Score, results[k].Score)
				}
			}
		}
	}
}

// N-17. Transitivity is what makes sort.Slice's output a function of the
// candidate set alone. Permuting the input must therefore not move a single
// result. This is the observable consequence of the property above, and the
// reason the defect mattered: M-23 removed the randomized input that exposed
// it, but any future change to how Recall gathers candidates would have
// reintroduced it.
func TestRankOrderIsIndependentOfCandidateOrder(t *testing.T) {
	ranker := nearTieRanker()
	base := nearTieCandidates()
	want := rankedIDs(ranker.Rank(base, rankGoldenNow))

	// Deterministic permutations: a linear congruential walk over the indices,
	// with a plain reversal and a rotation as the obvious adversarial cases.
	permutations := [][]RankCandidate{reversed(base)}
	for _, stride := range []int{3, 5, 7, 11, 13, 17, 19, 23} {
		permutations = append(permutations, strided(base, stride))
	}
	for index, permuted := range permutations {
		if got := rankedIDs(ranker.Rank(permuted, rankGoldenNow)); got != want {
			t.Fatalf("permutation %d reordered the results\n want: %s\n  got: %s", index, want, got)
		}
	}
}

func reversed(candidates []RankCandidate) []RankCandidate {
	out := make([]RankCandidate, len(candidates))
	for index, candidate := range candidates {
		out[len(candidates)-1-index] = candidate
	}
	return out
}

// strided walks the input with a stride coprime to its length, which visits
// every element exactly once in an order unrelated to the original.
func strided(candidates []RankCandidate, stride int) []RankCandidate {
	out := make([]RankCandidate, 0, len(candidates))
	seen := make([]bool, len(candidates))
	at := 0
	for range candidates {
		for seen[at] {
			at = (at + 1) % len(candidates)
		}
		seen[at] = true
		out = append(out, candidates[at])
		at = (at + stride) % len(candidates)
	}
	return out
}

// N-17. The order rankLess produces, stated directly: strictly descending by
// score, and ascending by id wherever scores are exactly equal. A golden diff
// would catch a change here too, but only as an opaque reshuffle.
func TestRankOrderIsScoreDescendingThenIDAscending(t *testing.T) {
	results := nearTieRanker().Rank(nearTieCandidates(), rankGoldenNow)
	for index := 1; index < len(results); index++ {
		previous, current := results[index-1], results[index]
		switch {
		case previous.Score > current.Score:
		case previous.Score == current.Score:
			if previous.Memory.ID.String() >= current.Memory.ID.String() {
				t.Fatalf("position %d: equal scores %.17g but ids %s then %s are not ascending",
					index, previous.Score, previous.Memory.ID, current.Memory.ID)
			}
		default:
			t.Fatalf("position %d: score rose from %.17g (%s) to %.17g (%s)",
				index, previous.Score, previous.Memory.ID, current.Score, current.Memory.ID)
		}
	}
}
