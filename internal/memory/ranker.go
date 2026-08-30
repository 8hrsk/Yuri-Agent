package memory

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// RankWeights make the retrieval policy explicit and tunable.  All input
// features are normalized to [0, 1], so changing one weight cannot silently
// change the meaning of another feature.
type RankWeights struct {
	Lexical   float64
	Vector    float64
	Recency   float64
	Salience  float64
	Affective float64
}

func DefaultRankWeights() RankWeights {
	return RankWeights{Lexical: 0.30, Vector: 0.30, Recency: 0.15, Salience: 0.20, Affective: 0.05}
}

func (w RankWeights) normalize() RankWeights {
	if w.Lexical < 0 {
		w.Lexical = 0
	}
	if w.Vector < 0 {
		w.Vector = 0
	}
	if w.Recency < 0 {
		w.Recency = 0
	}
	if w.Salience < 0 {
		w.Salience = 0
	}
	if w.Affective < 0 {
		w.Affective = 0
	}
	total := w.Lexical + w.Vector + w.Recency + w.Salience + w.Affective
	if total == 0 {
		return DefaultRankWeights()
	}
	w.Lexical /= total
	w.Vector /= total
	w.Recency /= total
	w.Salience /= total
	w.Affective /= total
	return w
}

// HybridRanker fuses lexical FTS, semantic vectors, freshness, salience and
// affective relevance.  It does not decide whether a record is eligible;
// Engine applies lifecycle and sensitivity policy before calling Rank.
type HybridRanker struct {
	Weights RankWeights
	Now     Clock
}

type RankCandidate struct {
	Memory             Memory
	LexicalScore       float64
	VectorScore        float64
	AffectiveRelevance float64
	Evidence           SourceEvidence
}

func (r HybridRanker) Rank(candidates []RankCandidate, now time.Time) []RecallResult {
	if now.IsZero() {
		if r.Now != nil {
			now = r.Now()
		} else {
			now = time.Now()
		}
	}
	weights := r.Weights.normalize()
	// Results are accumulated in the slice that is returned, and the map holds
	// only the index of the winning result for an id.  Keeping RecallResult as
	// the map's value type made every insert a heap allocation: the struct is
	// far larger than the 128-byte threshold above which Go stores map values
	// indirectly, so ranking N candidates cost N allocations of a full result
	// before the result slice had been built at all.
	//
	// The map is still a deduplication, not an accounting detail.  Both live
	// callers already guarantee one candidate per memory id — Recall keys its
	// candidates through candidateAt, and CoreSnapshot appends one candidate
	// per Store.ListMemories row, which the memory_heads primary key makes
	// unique — but Rank is exported and must not silently return the same
	// memory twice for a caller that has no such guarantee.  Highest score
	// wins, and the first occurrence wins a tie, exactly as before.
	results := make([]RecallResult, 0, len(candidates))
	indexByID := make(map[domain.ID]int, len(candidates))
	for _, candidate := range candidates {
		memory := candidate.Memory
		if memory.ID.Empty() {
			continue
		}
		lexical := clamp01(candidate.LexicalScore)
		vector := clamp01(candidate.VectorScore)
		recency := recencyScore(memory, now)
		salience := clamp01(memory.Salience * memory.Confidence)
		affective := clamp01(candidate.AffectiveRelevance*0.7 + math.Abs(memory.Valence)*0.3)
		score := weights.Lexical*lexical + weights.Vector*vector + weights.Recency*recency +
			weights.Salience*salience + weights.Affective*affective
		result := RecallResult{
			Memory: memory, Score: clamp01(score), LexicalScore: lexical,
			VectorScore: vector, RecencyScore: recency, SalienceScore: salience,
			AffectiveScore: affective, Dormant: memory.Lifecycle == StateDormant,
			Evidence: candidate.Evidence,
		}
		if at, exists := indexByID[memory.ID]; exists {
			if result.Score > results[at].Score {
				results[at] = result
			}
			continue
		}
		indexByID[memory.ID] = len(results)
		results = append(results, result)
	}
	sort.Slice(results, func(a, b int) bool { return rankLess(results[a], results[b]) })
	return results
}

// rankLess is the ranking order: score descending, ties broken by memory id
// ascending.  Ids are unique here — the loop above deduplicates by id — so the
// relation is a strict total order and Rank's output depends only on the set of
// candidates, never on the order they arrived in.
//
// N-17.  This used to treat scores within 1e-12 of each other as tied.  That
// tolerance is not an equivalence relation: with a≈b and b≈c but a≉c the
// comparator admits a 3-cycle (a<b by id, b<c by id, c<a by score), and a
// non-transitive comparator lets sort.Slice return whatever its input order
// happens to produce — so recall results could be reordered by an unrelated
// change to how candidates are gathered.
//
// Dropping the tolerance costs nothing it was buying.  Every score is the same
// fixed five-term expression, so two candidates that are conceptually tied
// produce bit-identical results and still fall through to the id tie-break;
// float addition is commutative, so even a tie reached through a different mix
// of the same signals lands on the same bits.  A difference the tolerance used
// to swallow was therefore a real difference in the inputs, and ordering by it
// is more faithful than ordering by id.
//
// NaN is ranked last rather than compared, because a NaN score would otherwise
// be neither greater nor less than anything and reintroduce exactly the
// intransitivity this function exists to remove.  Rank clamps its own signals
// but cannot stop an exported caller from handing one in.
func rankLess(a, b RecallResult) bool {
	aNaN, bNaN := math.IsNaN(a.Score), math.IsNaN(b.Score)
	switch {
	case aNaN != bNaN:
		return bNaN
	case !aNaN && a.Score != b.Score:
		return a.Score > b.Score
	default:
		return a.Memory.ID.String() < b.Memory.ID.String()
	}
}

func recencyScore(memory Memory, now time.Time) float64 {
	anchor := memory.UpdatedAt
	if memory.LastRecalledAt.After(anchor) {
		anchor = memory.LastRecalledAt
	}
	if anchor.IsZero() {
		anchor = memory.CreatedAt
	}
	age := now.Sub(anchor)
	if age <= 0 {
		return 1
	}
	halfLife := DefaultDecayPolicy(memory.Kind).HalfLife
	return clamp01(math.Exp(-age.Hours() / halfLife.Hours() * math.Ln2))
}

// LexicalScore is a small portable fallback for adapters without FTS5.  It
// rewards token coverage and exact phrase matches while remaining bounded.
//
// Scoring a whole store against one query goes through lexicalQuery instead,
// which prepares the query side once; this entry point keeps the one-shot
// signature for adapters and tests. Both share a single implementation so the
// two can never drift apart.
func LexicalScore(content, query string) float64 {
	return newLexicalQuery(query).score(content)
}

func tokenize(value string) []string {
	var tokens []string
	var builder strings.Builder
	flush := func() {
		if builder.Len() > 0 {
			tokens = append(tokens, strings.ToLower(builder.String()))
			builder.Reset()
		}
	}
	for _, runeValue := range value {
		if isTokenRune(runeValue) {
			builder.WriteRune(runeValue)
		} else {
			flush()
		}
	}
	flush()
	return tokens
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
