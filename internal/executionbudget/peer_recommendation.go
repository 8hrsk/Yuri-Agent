package executionbudget

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

const peerRecommendationHistoryLimit = 8

const (
	PeerRecommendationPurposeOnly    = domain.PeerBudgetRecommendationPurposeOnly
	PeerRecommendationPairHistory    = domain.PeerBudgetRecommendationPairHistory
	PeerRecommendationSimilarHistory = domain.PeerBudgetRecommendationSimilarHistory
)

// PeerHistorySample is the small, content-minimized projection used to tune a
// recommendation. The caller is responsible for supplying only dialogues
// visible to the active participant and, normally, only the selected pair.
type PeerHistorySample struct {
	Purpose         string
	Turns           int
	Tokens          int64
	DurationSeconds int
	HitHardLimit    bool
}

type PeerRecommendation struct {
	Budget      domain.PeerDialogueBudget
	Basis       domain.PeerBudgetRecommendationBasis
	SampleCount int
}

// RecommendPeer returns a deterministic starting point inside an already
// resolved hard ceiling. It never mutates the ceiling and cannot expand any
// turns, token, duration, or cooldown limit. Historical samples only influence
// resource hints; the owner still decides whether to apply the result.
func RecommendPeer(ceiling domain.PeerDialogueBudget, purpose string, history []PeerHistorySample) (PeerRecommendation, error) {
	if !ceiling.Valid() {
		return PeerRecommendation{}, fmt.Errorf("%w: peer recommendation ceiling is invalid", domain.ErrInvalidArgument)
	}
	purpose = strings.TrimSpace(purpose)
	if purpose == "" || utf8.RuneCountInString(purpose) > domain.PeerDialoguePurposeMaxRunes || strings.ContainsRune(purpose, '\x00') {
		return PeerRecommendation{}, fmt.Errorf("%w: peer recommendation purpose is invalid", domain.ErrInvalidArgument)
	}

	recommended := purposeBudget(ceiling, purpose)
	samples, basis := recommendationSamples(purpose, history)
	if len(samples) > 0 {
		recommended.MaxTurns = max(recommended.MaxTurns, historicalTurns(samples))
		recommended.MaxTokens = max(recommended.MaxTokens, historicalTokens(samples))
		recommended.MaxDurationSeconds = max(recommended.MaxDurationSeconds, historicalDuration(samples))
	}
	recommended = NarrowPeer(ceiling, PeerOverride{
		MaxTurns: recommended.MaxTurns, MaxTokens: recommended.MaxTokens, MaxDurationSeconds: recommended.MaxDurationSeconds,
	})
	return PeerRecommendation{Budget: recommended, Basis: basis, SampleCount: len(samples)}, nil
}

func purposeBudget(ceiling domain.PeerDialogueBudget, purpose string) domain.PeerDialogueBudget {
	termCount := len(purposeTerms(purpose))
	runeCount := utf8.RuneCountInString(purpose)
	structure := strings.Count(purpose, ",") + strings.Count(purpose, ";") + strings.Count(purpose, ":") + strings.Count(purpose, "\n")
	complexity := 0
	if runeCount > 55 || termCount > 8 {
		complexity++
	}
	if runeCount > 140 || termCount > 18 || structure > 2 {
		complexity++
	}

	turns := ceiling.MinTurns
	if complexity == 1 {
		turns++
	} else if complexity >= 2 {
		turns = ceiling.MaxTurns
	}
	turns = min(turns, ceiling.MaxTurns)
	tokens := min(ceiling.MaxTokens, int64(turns*2_000+1_000))
	duration := min(ceiling.MaxDurationSeconds, turns*20+10)
	return NarrowPeer(ceiling, PeerOverride{MaxTurns: turns, MaxTokens: tokens, MaxDurationSeconds: duration})
}

func recommendationSamples(purpose string, history []PeerHistorySample) ([]PeerHistorySample, domain.PeerBudgetRecommendationBasis) {
	valid := make([]PeerHistorySample, 0, min(len(history), peerRecommendationHistoryLimit))
	similar := make([]PeerHistorySample, 0, min(len(history), peerRecommendationHistoryLimit))
	for _, sample := range history {
		if sample.Turns <= 0 || sample.Tokens < 0 || sample.DurationSeconds < 0 || strings.TrimSpace(sample.Purpose) == "" {
			continue
		}
		if len(valid) < peerRecommendationHistoryLimit {
			valid = append(valid, sample)
		}
		if len(similar) < peerRecommendationHistoryLimit && purposeSimilarity(purpose, sample.Purpose) >= 0.5 {
			similar = append(similar, sample)
		}
	}
	if len(similar) > 0 {
		return similar, PeerRecommendationSimilarHistory
	}
	if len(valid) > 0 {
		return valid, PeerRecommendationPairHistory
	}
	return nil, PeerRecommendationPurposeOnly
}

func historicalTurns(samples []PeerHistorySample) int {
	values := make([]int, 0, len(samples))
	hitBoundary := false
	for _, sample := range samples {
		values = append(values, sample.Turns)
		hitBoundary = hitBoundary || sample.HitHardLimit
	}
	result := percentile75(values)
	if hitBoundary {
		result++
	}
	return result
}

func historicalTokens(samples []PeerHistorySample) int64 {
	values := make([]int64, 0, len(samples))
	for _, sample := range samples {
		if sample.Tokens > 0 {
			values = append(values, sample.Tokens)
		}
	}
	if len(values) == 0 {
		return 0
	}
	return percentile75(values)*5/4 + 500
}

func historicalDuration(samples []PeerHistorySample) int {
	values := make([]int, 0, len(samples))
	for _, sample := range samples {
		if sample.DurationSeconds > 0 {
			values = append(values, sample.DurationSeconds)
		}
	}
	if len(values) == 0 {
		return 0
	}
	return percentile75(values)*5/4 + 5
}

func percentile75[T ~int | ~int64](values []T) T {
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	return values[(len(values)*3-1)/4]
}

func purposeSimilarity(left, right string) float64 {
	leftTerms, rightTerms := purposeTerms(left), purposeTerms(right)
	if len(leftTerms) == 0 || len(rightTerms) == 0 {
		return 0
	}
	intersection := 0
	for term := range leftTerms {
		if _, ok := rightTerms[term]; ok {
			intersection++
		}
	}
	return float64(intersection) / float64(min(len(leftTerms), len(rightTerms)))
}

func purposeTerms(value string) map[string]struct{} {
	terms := make(map[string]struct{})
	for _, term := range strings.FieldsFunc(strings.ToLower(value), func(value rune) bool {
		return !unicode.IsLetter(value) && !unicode.IsDigit(value)
	}) {
		if utf8.RuneCountInString(term) >= 3 {
			terms[term] = struct{}{}
		}
	}
	return terms
}
