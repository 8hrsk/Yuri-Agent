package reflection

import (
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// MutablePersona is the only identity-adjacent state reflection may update.
// Immutable policy and identity seed are deliberately absent and therefore
// cannot be overwritten by a proposal or persisted as a persona delta.
type MutablePersona struct {
	Version      uint64             `json:"version,omitempty"`
	Traits       map[string]float64 `json:"traits,omitempty"`
	Prompt       string             `json:"prompt,omitempty"`
	PinnedTraits []string           `json:"pinned_traits,omitempty"`
	UpdatedAt    time.Time          `json:"updated_at,omitempty"`
}

func (p MutablePersona) Valid() bool { return p.Validate() == nil }

func (p MutablePersona) Validate() error {
	if err := validateScalarMap(p.Traits, "persona traits"); err != nil {
		return err
	}
	if err := validatePromptText(p.Prompt, 8192, ErrInvalidSnapshot); err != nil {
		return err
	}
	if strings.TrimSpace(p.Prompt) != "" && forbiddenPromptMutation(p.Prompt) {
		return fmt.Errorf("%w: mutable persona prompt attempts to alter an immutable boundary", ErrInvalidSnapshot)
	}
	seen := make(map[string]struct{}, len(p.PinnedTraits))
	for _, trait := range p.PinnedTraits {
		if err := validateName(trait); err != nil {
			return fmt.Errorf("%w: invalid pinned trait %q: %v", ErrInvalidSnapshot, trait, err)
		}
		key := strings.ToLower(strings.TrimSpace(trait))
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%w: duplicate pinned trait %q", ErrInvalidSnapshot, trait)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// OpinionLabel explicitly distinguishes Yuri's subjective opinion from an
// inference that an adapter may have derived from evidence. Both labels are
// data-only relationship state and neither one is a fact or a policy input.
type OpinionLabel string

const (
	maxSubjectiveOpinions            = 64
	maxSubjectiveOpinionContentBytes = 4096

	OpinionLabelOpinion   OpinionLabel = "opinion"
	OpinionLabelInference OpinionLabel = "inference"

	// Short aliases keep adapter call sites readable while the prefixed names
	// remain unambiguous in larger packages.
	LabelOpinion   = OpinionLabelOpinion
	LabelInference = OpinionLabelInference
	Opinion        = OpinionLabelOpinion
	Inference      = OpinionLabelInference
)

func (l OpinionLabel) Valid() bool {
	return l == OpinionLabelOpinion || l == OpinionLabelInference
}

// SubjectiveOpinion is Yuri's typed, evidence-backed view of the user. It is
// deliberately separate from factual memory and has no policy authority.
// Claim is the canonical text spelling; adapters may map it to a richer
// relationship record when persisting the reflection result.
type SubjectiveOpinion struct {
	ID          domain.ID    `json:"id,omitempty"`
	Subject     string       `json:"subject"`
	Topic       string       `json:"topic,omitempty"`
	Claim       string       `json:"claim"`
	Label       OpinionLabel `json:"label"`
	Confidence  float64      `json:"confidence"`
	Reason      string       `json:"reason"`
	EvidenceIDs []domain.ID  `json:"evidence_ids,omitempty"`
	Evidence    []domain.ID  `json:"evidence,omitempty"`
	CreatedAt   time.Time    `json:"created_at,omitempty"`
	UpdatedAt   time.Time    `json:"updated_at,omitempty"`
}

// SubjectiveOpinionDelta is a compatibility alias for adapters that use the
// state-oriented name when handling relationship changes.
type SubjectiveOpinionDelta = OpinionDelta

// RelationshipState is subjective relationship metadata, not factual memory
// and not a policy input. Dimensions are normalized scalar values; adapters
// may choose their own configured ranges. Opinions are bounded, deterministic
// records that can be appended or replaced by their canonical subject/topic/
// label key.
type RelationshipState struct {
	Version    uint64              `json:"version,omitempty"`
	Dimensions map[string]float64  `json:"dimensions,omitempty"`
	Summary    string              `json:"summary,omitempty"`
	Confidence float64             `json:"confidence,omitempty"`
	Evidence   []domain.ID         `json:"evidence,omitempty"`
	Opinions   []SubjectiveOpinion `json:"opinions,omitempty"`
	UpdatedAt  time.Time           `json:"updated_at,omitempty"`
}

func (s RelationshipState) Valid() bool { return s.Validate() == nil }

func (s RelationshipState) Validate() error {
	if err := validateScalarMap(s.Dimensions, "relationship dimensions"); err != nil {
		return err
	}
	if !finite(s.Confidence) || s.Confidence < 0 || s.Confidence > 1 {
		return fmt.Errorf("%w: relationship confidence is outside [0,1]", ErrInvalidSnapshot)
	}
	if err := validateIDSlice(s.Evidence, "relationship evidence", ErrInvalidSnapshot); err != nil {
		return err
	}
	if strings.ContainsRune(s.Summary, '\x00') {
		return fmt.Errorf("%w: relationship summary contains NUL", ErrInvalidSnapshot)
	}
	if len(s.Opinions) > maxSubjectiveOpinions {
		return fmt.Errorf("%w: relationship opinions exceed %d", ErrInvalidSnapshot, maxSubjectiveOpinions)
	}
	seenIDs := make(map[domain.ID]struct{}, len(s.Opinions))
	seenKeys := make(map[string]struct{}, len(s.Opinions))
	for index, opinion := range s.Opinions {
		if err := validateSubjectiveOpinion(opinion); err != nil {
			return fmt.Errorf("%w: relationship opinion at index %d: %v", ErrInvalidSnapshot, index, err)
		}
		if _, exists := seenIDs[opinion.ID]; exists {
			return fmt.Errorf("%w: duplicate relationship opinion id %s", ErrInvalidSnapshot, opinion.ID)
		}
		seenIDs[opinion.ID] = struct{}{}
		key := opinionKey(opinion.Subject, opinion.Topic, opinion.Label)
		if _, exists := seenKeys[key]; exists {
			return fmt.Errorf("%w: duplicate relationship opinion key %q", ErrInvalidSnapshot, key)
		}
		seenKeys[key] = struct{}{}
	}
	return nil
}

// AffectiveState stores modeled emotional dimensions. Values are signed by
// convention (positive/negative valence); configured ranges can instead model
// separate non-negative intensities. Decay is deterministic and independent
// of wall-clock calls inside the package.
type AffectiveState struct {
	Version          uint64               `json:"version,omitempty"`
	Dimensions       map[string]float64   `json:"dimensions,omitempty"`
	DimensionUpdated map[string]time.Time `json:"dimension_updated,omitempty"`
	UpdatedAt        time.Time            `json:"updated_at,omitempty"`
}

// AffectState is a concise compatibility alias.
type AffectState = AffectiveState

func (s AffectiveState) Valid() bool { return s.Validate() == nil }

func (s AffectiveState) Validate() error {
	if err := validateScalarMap(s.Dimensions, "affective dimensions"); err != nil {
		return err
	}
	for name, at := range s.DimensionUpdated {
		if err := validateName(name); err != nil {
			return fmt.Errorf("%w: invalid affect timestamp key %q: %v", ErrInvalidSnapshot, name, err)
		}
		if at.IsZero() {
			return fmt.Errorf("%w: affect timestamp for %q is required", ErrInvalidSnapshot, name)
		}
	}
	return nil
}

// ReflectionState is the adapter boundary for the latest state. The engine
// never persists it itself; State in ReflectionResult is a copy suitable for
// an atomic versioned write by a storage/application adapter.
type ReflectionState struct {
	Version          uint64            `json:"version,omitempty"`
	Persona          MutablePersona    `json:"persona"`
	Relationship     RelationshipState `json:"relationship"`
	Affect           AffectiveState    `json:"affect"`
	LastReflectionAt time.Time         `json:"last_reflection_at,omitempty"`
	UpdatedAt        time.Time         `json:"updated_at,omitempty"`
}

func (s ReflectionState) Valid() bool { return s.Validate() == nil }

func (s ReflectionState) Validate() error {
	if err := s.Persona.Validate(); err != nil {
		return err
	}
	if err := s.Relationship.Validate(); err != nil {
		return err
	}
	if err := s.Affect.Validate(); err != nil {
		return err
	}
	return nil
}

// InputSnapshot is captured once before an analyzer call. It contains the
// immutable boundaries for reference, current mutable state, and read-only
// evidence. Engine.Run clones this value before handing it to any analyzer.
type InputSnapshot struct {
	ProfileID       domain.ID       `json:"profile_id"`
	RunID           domain.ID       `json:"run_id"`
	Trigger         Trigger         `json:"trigger"`
	CapturedAt      time.Time       `json:"captured_at"`
	ImmutablePolicy string          `json:"immutable_policy"`
	IdentitySeed    string          `json:"identity_seed"`
	State           ReflectionState `json:"state"`
	Evidence        []Evidence      `json:"evidence"`
}

func (s InputSnapshot) Valid() bool { return s.Validate() == nil }

func (s InputSnapshot) Validate() error {
	if s.ProfileID.Empty() || s.RunID.Empty() || !s.Trigger.Valid() || s.CapturedAt.IsZero() {
		return fmt.Errorf("%w: profile, run, trigger, and captured_at are required", ErrInvalidSnapshot)
	}
	if strings.TrimSpace(s.ImmutablePolicy) == "" || strings.TrimSpace(s.IdentitySeed) == "" {
		return fmt.Errorf("%w: immutable policy and identity seed are required", ErrInvalidSnapshot)
	}
	if strings.ContainsRune(s.ImmutablePolicy, '\x00') || strings.ContainsRune(s.IdentitySeed, '\x00') {
		return fmt.Errorf("%w: immutable boundary contains NUL", ErrInvalidSnapshot)
	}
	if err := s.State.Validate(); err != nil {
		return err
	}
	seen := make(map[domain.ID]struct{}, len(s.Evidence))
	for index, evidence := range s.Evidence {
		if err := evidence.Validate(); err != nil {
			return fmt.Errorf("%w: evidence at index %d: %v", ErrInvalidSnapshot, index, err)
		}
		if _, ok := seen[evidence.ID]; ok {
			return fmt.Errorf("%w: duplicate evidence id %s", ErrInvalidSnapshot, evidence.ID)
		}
		seen[evidence.ID] = struct{}{}
	}
	return nil
}
