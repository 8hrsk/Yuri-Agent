package memory

import (
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
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
	byID := make(map[string]RecallResult, len(candidates))
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
		key := memory.ID.String()
		if previous, exists := byID[key]; !exists || result.Score > previous.Score {
			byID[key] = result
		}
	}
	results := make([]RecallResult, 0, len(byID))
	for _, result := range byID {
		results = append(results, result)
	}
	sort.Slice(results, func(a, b int) bool {
		if math.Abs(results[a].Score-results[b].Score) > 1e-12 {
			return results[a].Score > results[b].Score
		}
		return results[a].Memory.ID.String() < results[b].Memory.ID.String()
	})
	return results
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
func LexicalScore(content, query string) float64 {
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
		if unicode.IsLetter(runeValue) || unicode.IsDigit(runeValue) || runeValue == '_' {
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
