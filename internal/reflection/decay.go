package reflection

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// DecayPolicy controls deterministic exponential affect decay. HalfLife is
// the time required for a value to halve. DimensionHalfLives can override it
// for a named affect dimension; both maps are copied before use.
type DecayPolicy struct {
	HalfLife           time.Duration            `json:"half_life"`
	DimensionHalfLives map[string]time.Duration `json:"dimension_half_lives,omitempty"`
}

func DefaultDecayPolicy() DecayPolicy {
	return DecayPolicy{HalfLife: 7 * 24 * time.Hour}
}

func (p DecayPolicy) Valid() bool {
	if p.HalfLife <= 0 {
		return false
	}
	for name, halfLife := range p.DimensionHalfLives {
		if validateName(name) != nil || halfLife <= 0 {
			return false
		}
	}
	return true
}

func (p DecayPolicy) normalize() DecayPolicy {
	if p.HalfLife <= 0 {
		p.HalfLife = DefaultDecayPolicy().HalfLife
	}
	if p.DimensionHalfLives != nil {
		overrides := make(map[string]time.Duration, len(p.DimensionHalfLives))
		for name, halfLife := range p.DimensionHalfLives {
			if validateName(name) == nil && halfLife > 0 {
				overrides[strings.ToLower(name)] = halfLife
			}
		}
		p.DimensionHalfLives = overrides
	}
	return p
}

func (p DecayPolicy) halfLife(name string) time.Duration {
	if value, ok := p.DimensionHalfLives[strings.ToLower(name)]; ok && value > 0 {
		return value
	}
	return p.HalfLife
}

// DecayAffect returns a deep copy whose dimensions are decayed to now. It
// performs no random sampling and never reads the process clock. Values are
// updated using value * 2^(-elapsed / halfLife), preserving their sign.
// Calling it again with the same state and timestamp is idempotent.
func DecayAffect(state AffectiveState, now time.Time, policy DecayPolicy) (AffectiveState, error) {
	if now.IsZero() {
		return AffectiveState{}, fmt.Errorf("%w: affect decay timestamp is required", ErrInvalidSnapshot)
	}
	if err := state.Validate(); err != nil {
		return AffectiveState{}, err
	}
	if !policy.Valid() {
		return AffectiveState{}, fmt.Errorf("%w: invalid affect decay policy", ErrInvalidSnapshot)
	}
	policy = policy.normalize()
	result := cloneAffect(state)
	now = now.UTC()
	base := state.UpdatedAt
	if base.IsZero() {
		base = now
	}
	if now.Before(base) {
		return AffectiveState{}, fmt.Errorf("%w: affect decay timestamp precedes state", ErrInvalidSnapshot)
	}
	for name, value := range result.Dimensions {
		at := base
		if candidate, ok := state.DimensionUpdated[name]; ok {
			at = candidate
		}
		if at.IsZero() {
			at = base
		}
		if now.Before(at) {
			return AffectiveState{}, fmt.Errorf("%w: affect dimension %q timestamp is in the future", ErrInvalidSnapshot, name)
		}
		elapsed := now.Sub(at)
		if elapsed > 0 {
			factor := math.Exp2(-float64(elapsed) / float64(policy.halfLife(name)))
			result.Dimensions[name] = value * factor
			// Avoid retaining subnormal noise forever while keeping the operation
			// deterministic across repeated runs.
			if math.Abs(result.Dimensions[name]) < 1e-15 {
				result.Dimensions[name] = 0
			}
		}
	}
	result.UpdatedAt = now
	if result.DimensionUpdated != nil {
		for name := range result.Dimensions {
			result.DimensionUpdated[name] = now
		}
	}
	return result, nil
}

// ApplyAffectDecay is a descriptive alias for callers that use command-style
// naming. It has the same deterministic, copy-on-write semantics.
func ApplyAffectDecay(state AffectiveState, now time.Time, policy DecayPolicy) (AffectiveState, error) {
	return DecayAffect(state, now, policy)
}

// Decay is kept short for background workers that expose a generic decay
// operation while still requiring an explicit timestamp and policy.
func Decay(state AffectiveState, now time.Time, policy DecayPolicy) (AffectiveState, error) {
	return DecayAffect(state, now, policy)
}

// affectStateChanged reports whether a decay projection differs from its
// source state. Time values are compared by instant rather than location or
// monotonic-clock metadata, so equivalent timestamps do not look like a
// change merely because their representation differs.
func affectStateChanged(before, after AffectiveState) bool {
	if before.Version != after.Version || !sameFloatMap(before.Dimensions, after.Dimensions) ||
		!sameTimeMap(before.DimensionUpdated, after.DimensionUpdated) {
		return true
	}
	return !sameInstant(before.UpdatedAt, after.UpdatedAt)
}

func sameFloatMap(before, after map[string]float64) bool {
	if len(before) != len(after) {
		return false
	}
	for name, value := range before {
		if candidate, ok := after[name]; !ok || candidate != value {
			return false
		}
	}
	return true
}

func sameTimeMap(before, after map[string]time.Time) bool {
	if len(before) != len(after) {
		return false
	}
	for name, value := range before {
		candidate, ok := after[name]
		if !ok || !sameInstant(value, candidate) {
			return false
		}
	}
	return true
}

func sameInstant(before, after time.Time) bool {
	if before.IsZero() || after.IsZero() {
		return before.IsZero() && after.IsZero()
	}
	return before.Equal(after)
}
