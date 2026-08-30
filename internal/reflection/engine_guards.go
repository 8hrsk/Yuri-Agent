package reflection

import (
	"fmt"
	"math"
	"strings"
)

func (e *Engine) validateScalarDelta(delta, current map[string]float64, ranges map[string]ValueRange, target string, fallback ValueRange) error {
	if err := e.validateScalarDeltaLimit(delta, target); err != nil {
		return err
	}
	return e.validateScalarDeltaValue(delta, current, ranges, target, fallback)
}

func (e *Engine) validateScalarDeltaLimit(delta map[string]float64, target string) error {
	for _, name := range sortedKeys(delta) {
		value := delta[name]
		limit := e.config.MaxDelta
		if override, ok := e.config.MaxDeltaByTrait[strings.ToLower(name)]; ok {
			limit = override
		}
		if math.Abs(value) > limit {
			return fmt.Errorf("%w: %s %q delta %.6f exceeds %.6f", ErrDeltaExceeded, target, name, value, limit)
		}
	}
	return nil
}

func (e *Engine) validateScalarDeltaValue(delta, current map[string]float64, ranges map[string]ValueRange, target string, fallback ValueRange) error {
	for _, name := range sortedKeys(delta) {
		value := delta[name]
		base := current[name]
		bounds := lookupRange(ranges, name, fallback)
		if target == "relationship" {
			if _, configured := ranges[strings.ToLower(name)]; !configured {
				bounds = defaultRelationshipRange(name)
			}
		}
		if !bounds.Contains(base + value) {
			return fmt.Errorf("%w: %s %q value %.6f is outside [%.6f,%.6f]", ErrOutOfRange, target, name, base+value, bounds.Min, bounds.Max)
		}
	}
	return nil
}

func (e *Engine) validatePersonaDelta(current MutablePersona, delta *PersonaDelta) error {
	if delta == nil {
		return nil
	}
	pinned := make(map[string]bool, len(current.PinnedTraits)+len(e.config.PinnedTraits))
	for _, name := range current.PinnedTraits {
		pinned[strings.ToLower(name)] = true
	}
	for name, value := range e.config.PinnedTraits {
		if value {
			pinned[strings.ToLower(name)] = true
		}
	}
	for _, name := range sortedKeys(delta.Traits) {
		if pinned[strings.ToLower(name)] {
			return fmt.Errorf("%w: persona trait %q cannot be changed", ErrPinnedTrait, name)
		}
	}
	if err := e.validateScalarDelta(delta.Traits, current.Traits, e.config.TraitRanges, "persona", ValueRange{}); err != nil {
		return err
	}
	prompt := firstNonEmpty(delta.Prompt, delta.PromptDelta)
	if prompt != "" {
		if forbiddenPromptMutation(prompt) {
			return fmt.Errorf("%w: persona prompt attempts to alter an immutable boundary", ErrForbiddenMutation)
		}
		if err := validatePromptText(prompt, e.config.MaxPromptBytes, ErrInvalidProposal); err != nil {
			return err
		}
		result := strings.TrimSpace(delta.Prompt)
		if result == "" {
			result = strings.TrimSpace(current.Prompt)
			if result == "" {
				result = strings.TrimSpace(delta.PromptDelta)
			} else {
				result += "\n" + strings.TrimSpace(delta.PromptDelta)
			}
		}
		if len([]byte(result)) > e.config.MaxPromptBytes {
			return fmt.Errorf("%w: resulting persona prompt exceeds %d bytes", ErrDeltaExceeded, e.config.MaxPromptBytes)
		}
	}
	return nil
}
