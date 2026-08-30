package reflection

import (
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// ReflectionBudget bounds one background call. Zero values are normalized by
// Engine to DefaultBudget; negative values are always rejected.
type ReflectionBudget struct {
	MaxDuration    time.Duration `json:"max_duration"`
	MaxTokens      int64         `json:"max_tokens"`
	MaxInputBytes  int           `json:"max_input_bytes"`
	MaxOutputBytes int           `json:"max_output_bytes"`
	MaxEvidence    int           `json:"max_evidence"`
}

// Budget is the shorter name used by reflection callers.
type Budget = ReflectionBudget

func DefaultBudget() ReflectionBudget {
	return ReflectionBudget{
		MaxDuration:    2 * time.Minute,
		MaxTokens:      4096,
		MaxInputBytes:  256 * 1024,
		MaxOutputBytes: 32 * 1024,
		MaxEvidence:    128,
	}
}

func (b ReflectionBudget) Valid() bool {
	return b.MaxDuration >= 0 && b.MaxTokens >= 0 && b.MaxInputBytes >= 0 &&
		b.MaxOutputBytes >= 0 && b.MaxEvidence >= 0
}

func (b ReflectionBudget) normalize() ReflectionBudget {
	d := DefaultBudget()
	if b.MaxDuration <= 0 {
		b.MaxDuration = d.MaxDuration
	}
	if b.MaxTokens <= 0 {
		b.MaxTokens = d.MaxTokens
	}
	if b.MaxInputBytes <= 0 {
		b.MaxInputBytes = d.MaxInputBytes
	}
	if b.MaxOutputBytes <= 0 {
		b.MaxOutputBytes = d.MaxOutputBytes
	}
	if b.MaxEvidence <= 0 {
		b.MaxEvidence = d.MaxEvidence
	}
	return b
}

// Usage is provider-reported accounting plus output size measured by the
// model adapter. Reflection still enforces byte limits locally even when a
// provider does not report token usage.
type Usage struct {
	InputTokens  int64 `json:"input_tokens,omitempty"`
	OutputTokens int64 `json:"output_tokens,omitempty"`
	TotalTokens  int64 `json:"total_tokens,omitempty"`
	OutputBytes  int   `json:"output_bytes,omitempty"`
}

func (u Usage) Valid() bool {
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.TotalTokens < 0 || u.OutputBytes < 0 {
		return false
	}
	if u.InputTokens > int64(^uint64(0)>>1)-u.OutputTokens {
		return false
	}
	return u.TotalTokens == 0 || u.TotalTokens >= u.InputTokens+u.OutputTokens
}

// Config controls semantic guards and run coordination.
type Config struct {
	Analyzer               Analyzer
	Coordinator            *Coordinator
	Clock                  domain.Clock
	Budget                 ReflectionBudget
	MaxDelta               float64
	MaxDeltaByTrait        map[string]float64
	MinimumEvidence        int
	MinimumEvidenceWeight  float64
	Cooldown               time.Duration
	DurableStateCooldown   time.Duration
	AffectDecay            DecayPolicy
	AffectAppraisal        AffectAppraisalPolicy
	TraitRanges            map[string]ValueRange
	RelationshipRanges     map[string]ValueRange
	AffectRanges           map[string]ValueRange
	PinnedTraits           map[string]bool
	MaxPromptBytes         int
	MaxOpinions            int
	MaxOpinionContentBytes int
}

// DefaultConfig returns conservative, provider-neutral defaults. It leaves
// cooldown disabled because persistence owns the product-specific cadence.
func DefaultConfig() Config {
	return Config{
		Budget:                 DefaultBudget(),
		MaxDelta:               0.20,
		MinimumEvidence:        1,
		MinimumEvidenceWeight:  0,
		AffectDecay:            DefaultDecayPolicy(),
		MaxPromptBytes:         4096,
		MaxOpinions:            maxSubjectiveOpinions,
		MaxOpinionContentBytes: maxSubjectiveOpinionContentBytes,
	}
}
