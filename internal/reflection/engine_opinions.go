package reflection

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func (e *Engine) applyOpinionDeltas(existing []SubjectiveOpinion, deltas []OpinionDelta, now time.Time) ([]SubjectiveOpinion, error) {
	if len(deltas) == 0 {
		return append([]SubjectiveOpinion(nil), existing...), nil
	}
	result := make([]SubjectiveOpinion, len(existing))
	for index, opinion := range existing {
		result[index] = cloneOpinion(opinion)
	}
	for _, delta := range deltas {
		key := opinionKey(delta.Subject, delta.Topic, delta.Label)
		index := -1
		for candidate := range result {
			if result[candidate].Key() == key {
				index = candidate
				break
			}
		}
		if index < 0 && !delta.ID.Empty() {
			for candidate := range result {
				if result[candidate].ID == delta.ID {
					index = candidate
					break
				}
			}
		}
		var next SubjectiveOpinion
		if index >= 0 {
			next = result[index]
		} else {
			next.ID = delta.ID
			if next.ID.Empty() {
				next.ID = deterministicOpinionID(key)
			}
			next.CreatedAt = now
		}
		next.Subject = strings.TrimSpace(delta.Subject)
		next.Topic = strings.TrimSpace(delta.Topic)
		next.Claim = strings.TrimSpace(delta.Claim)
		next.Label = delta.Label
		next.Confidence = delta.Confidence
		next.Reason = strings.TrimSpace(delta.Reason)
		next.EvidenceIDs = sortedOpinionEvidence(delta.EvidenceIDs, delta.Evidence)
		next.Evidence = nil
		next.UpdatedAt = now
		if next.CreatedAt.IsZero() {
			next.CreatedAt = now
		}
		if index >= 0 {
			result[index] = next
		} else {
			result = append(result, next)
		}
		result = deduplicateOpinions(result)
	}
	if len(result) > e.config.MaxOpinions {
		return nil, fmt.Errorf("%w: relationship has %d opinions, maximum is %d", ErrOpinionLimit, len(result), e.config.MaxOpinions)
	}
	sortOpinions(result)
	return result, nil
}

func deterministicOpinionID(key string) domain.ID {
	digest := sha256.Sum256([]byte(key))
	return domain.ID("opinion-" + hex.EncodeToString(digest[:12]))
}

func sortedOpinionEvidence(first, second []domain.ID) []domain.ID {
	ids := make([]domain.ID, 0, len(first)+len(second))
	ids = append(ids, first...)
	ids = append(ids, second...)
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	return ids
}

func deduplicateOpinions(values []SubjectiveOpinion) []SubjectiveOpinion {
	seenKeys := make(map[string]struct{}, len(values))
	seenIDs := make(map[domain.ID]struct{}, len(values))
	// Walk backwards so the latest delta wins for both canonical key and
	// explicit ID, then restore the original order for deterministic sorting.
	reversed := make([]SubjectiveOpinion, 0, len(values))
	for index := len(values) - 1; index >= 0; index-- {
		value := values[index]
		if _, exists := seenKeys[value.Key()]; exists {
			continue
		}
		if !value.ID.Empty() {
			if _, exists := seenIDs[value.ID]; exists {
				continue
			}
			seenIDs[value.ID] = struct{}{}
		}
		seenKeys[value.Key()] = struct{}{}
		reversed = append(reversed, value)
	}
	result := make([]SubjectiveOpinion, len(reversed))
	for index := range reversed {
		result[len(reversed)-1-index] = reversed[index]
	}
	return result
}

func sortOpinions(values []SubjectiveOpinion) {
	sort.SliceStable(values, func(left, right int) bool {
		leftKey, rightKey := values[left].Key(), values[right].Key()
		if leftKey == rightKey {
			return values[left].ID < values[right].ID
		}
		return leftKey < rightKey
	})
}
