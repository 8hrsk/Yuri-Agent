package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// EffectiveSalience is exported for decay previews and tests.
func EffectiveSalience(memory domain.Memory, now time.Time, policy DecayPolicy) float64 {
	policy = policy.normalize(memory.Kind)
	anchor := activityTime(memory)
	if now.Before(anchor) || policy.HalfLife <= 0 {
		return clamp01(memory.Salience * memory.Confidence)
	}
	age := now.Sub(anchor)
	decay := math.Exp(-age.Hours() / policy.HalfLife.Hours() * math.Ln2)
	accessBoost := math.Min(0.25, math.Log1p(float64(memory.AccessCount))*0.03)
	return clamp01(memory.Salience*memory.Confidence*decay + accessBoost)
}

func activityTime(memory domain.Memory) time.Time {
	anchor := memory.UpdatedAt
	for _, candidate := range []time.Time{memory.LastAccessedAt, memory.LastRecalledAt, memory.CreatedAt} {
		if candidate.After(anchor) {
			anchor = candidate
		}
	}
	return anchor
}

// affectiveRelevance scores one record against one query. Recall does not use
// it directly: it decides affectiveQuery once and calls affectiveRelevanceFor,
// because this signature forces the query to be re-tokenized for every stored
// record. The two are pinned to each other by
// TestAffectiveQueryMatchesAffectiveRelevance.
func affectiveRelevance(query string, memory domain.Memory) float64 {
	return affectiveRelevanceFor(affectiveQuery(query), memory)
}

// affectiveQueryStems are matched as prefixes, not as whole tokens.
//
// tokenize only lowercases and splits on non-alphanumerics — it does no
// stemming. Comparing a token for equality against a truncated stem therefore
// made the Russian entries unreachable: an inflected form such as "чувства" or
// "отношения" never equals its own stem, so affective relevance silently
// scored 0 for every realistic Russian query. Only a user who literally typed
// the truncated stem could trigger it. The English entries happened to work
// because they are whole words, which hid the defect in English testing.
//
// Prefix matching fixes the inflected forms and leaves the whole-word entries
// behaving as before, since every word is a prefix of itself.
var affectiveQueryStems = []string{
	"чувств",
	"эмоц",
	"отнош",
	"настроен",
	"feel",
	"emotion",
	"mood",
	"relationship",
}

func canonicalKey(memory domain.Memory) string {
	return string(memory.Kind) + ":" + string(memory.Nature) + ":" + strings.Join(tokenize(memory.Content), " ")
}

func sameContent(left, right string) bool {
	return canonicalText(left) == canonicalText(right)
}

func canonicalText(value string) string { return strings.Join(tokenize(value), " ") }

func hashExcerpt(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func mergeMemory(existing, candidate domain.Memory, now time.Time) domain.Memory {
	merged := existing
	if strings.TrimSpace(candidate.Content) != "" {
		merged.Content = strings.TrimSpace(candidate.Content)
	}
	if candidate.ContentJSON != "" {
		merged.ContentJSON = candidate.ContentJSON
	}
	if candidate.Summary != "" {
		merged.Summary = candidate.Summary
	}
	if candidate.Kind.Valid() {
		merged.Kind = candidate.Kind
	}
	if candidate.Nature.Valid() {
		merged.Nature = candidate.Nature
	}
	if candidate.Sensitivity.Valid() {
		merged.Sensitivity = candidate.Sensitivity
	}
	if candidate.Retention.Valid() {
		merged.Retention = candidate.Retention
	}
	if candidate.Confidence > merged.Confidence {
		merged.Confidence = candidate.Confidence
	}
	if candidate.Salience > merged.Salience {
		merged.Salience = candidate.Salience
	}
	if candidate.Valence != 0 {
		merged.Valence = candidate.Valence
	}
	if candidate.CanonicalKey != "" {
		merged.CanonicalKey = candidate.CanonicalKey
	}
	if candidate.Pinned {
		merged.Pinned = true
	}
	if candidate.HiddenFromCore {
		merged.HiddenFromCore = true
	}
	if candidate.Lifecycle == domain.MemoryLifecycleDormant {
		merged.Lifecycle = candidate.Lifecycle
		merged.DormantAt = candidate.DormantAt
	}
	merged.UpdatedAt = now.UTC()
	return merged
}

// ConservativeConsolidator is safe to use before a model-assisted conflict
// resolver exists. Equal facts are merged; a higher-confidence replacement is
// accepted, while a lower-confidence conflicting claim is ignored.
type ConservativeConsolidator struct{}

func (ConservativeConsolidator) Consolidate(_ context.Context, existing domain.Memory, candidate Candidate) (Consolidation, error) {
	if candidate.Operation == CandidateForget {
		return Consolidation{Operation: OperationForget, Memory: existing}, nil
	}
	if sameContent(existing.Content, candidate.Memory.Content) && candidate.Memory.Content != "" {
		// Preserve the canonical human-facing text for duplicates; punctuation
		// and casing differences should not create needless revisions.
		merged := existing
		if candidate.Memory.Confidence > merged.Confidence {
			merged.Confidence = candidate.Memory.Confidence
		}
		if candidate.Memory.Salience > merged.Salience {
			merged.Salience = candidate.Memory.Salience
		}
		if candidate.Memory.ContentJSON != "" {
			merged.ContentJSON = candidate.Memory.ContentJSON
		}
		if merged.Confidence == existing.Confidence && merged.Salience == existing.Salience && merged.ContentJSON == existing.ContentJSON {
			return Consolidation{Operation: OperationTouch, Memory: existing, Noop: true, Reason: "duplicate fact"}, nil
		}
		return Consolidation{Operation: OperationMerge, Memory: merged, Reason: "duplicate evidence consolidated"}, nil
	}
	if candidate.Operation == CandidateUpdate || candidate.MatchID != "" {
		return Consolidation{Operation: OperationUpdate, Memory: candidate.Memory, Reason: "explicit memory update"}, nil
	}
	if candidate.Memory.Confidence > existing.Confidence {
		return Consolidation{Operation: OperationMerge, Memory: candidate.Memory, Reason: "higher-confidence evidence replaced conflicting claim"}, nil
	}
	return Consolidation{Operation: OperationTouch, Memory: existing, Noop: true, Reason: "lower-confidence conflicting claim ignored"}, nil
}
