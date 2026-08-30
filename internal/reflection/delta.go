package reflection

import (
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// RelationshipDelta, AffectDelta, and PersonaDelta are deliberately typed
// and separate so an adapter cannot smuggle one kind of state into another.
// Opinions are append-or-replace records; scalar dimensions remain additive.
type OpinionDelta struct {
	ID          domain.ID    `json:"id,omitempty"`
	Subject     string       `json:"subject"`
	Topic       string       `json:"topic,omitempty"`
	Claim       string       `json:"claim"`
	Label       OpinionLabel `json:"label"`
	Confidence  float64      `json:"confidence"`
	Reason      string       `json:"reason"`
	EvidenceIDs []domain.ID  `json:"evidence_ids,omitempty"`
	Evidence    []domain.ID  `json:"evidence,omitempty"`
}

func (o SubjectiveOpinion) Valid() bool { return o.Validate() == nil }

// Validate delegates to validateSubjectiveOpinion, which lives in
// validation.go alongside the other semantic validators.
func (o SubjectiveOpinion) Validate() error { return validateSubjectiveOpinion(o) }

// Key returns the deterministic identity used for append-or-replace. Claims
// may change as new evidence arrives; subject/topic/label identify the stable
// opinion slot instead.
func (o SubjectiveOpinion) Key() string { return opinionKey(o.Subject, o.Topic, o.Label) }

func (d OpinionDelta) Valid() bool { return d.Validate() == nil }

func (d OpinionDelta) Validate() error { return validateOpinionDelta(d) }

type RelationshipDelta struct {
	Dimensions  map[string]float64 `json:"dimensions,omitempty"`
	Opinions    []OpinionDelta     `json:"opinions,omitempty"`
	EvidenceIDs []domain.ID        `json:"evidence_ids,omitempty"`
	Evidence    []domain.ID        `json:"evidence,omitempty"`
	Reason      string             `json:"reason,omitempty"`
	Confidence  float64            `json:"confidence,omitempty"`
}

type AffectDelta struct {
	Dimensions  map[string]float64 `json:"dimensions,omitempty"`
	EvidenceIDs []domain.ID        `json:"evidence_ids,omitempty"`
	Evidence    []domain.ID        `json:"evidence,omitempty"`
	Reason      string             `json:"reason,omitempty"`
	Confidence  float64            `json:"confidence,omitempty"`
}

type PersonaDelta struct {
	Traits      map[string]float64 `json:"traits,omitempty"`
	Prompt      string             `json:"prompt,omitempty"`
	PromptDelta string             `json:"prompt_delta,omitempty"`
	EvidenceIDs []domain.ID        `json:"evidence_ids,omitempty"`
	Evidence    []domain.ID        `json:"evidence,omitempty"`
	Reason      string             `json:"reason,omitempty"`
	Confidence  float64            `json:"confidence,omitempty"`
}

// ReflectionProposal is the strict model output schema. The top-level
// EvidenceIDs/Evidence references are inherited by a delta that has no local
// references, reducing repetition without weakening provenance validation.
type ReflectionProposal struct {
	Outcome      Outcome            `json:"outcome"`
	Reason       string             `json:"reason"`
	EvidenceIDs  []domain.ID        `json:"evidence_ids,omitempty"`
	Evidence     []domain.ID        `json:"evidence,omitempty"`
	Relationship *RelationshipDelta `json:"relationship,omitempty"`
	Affect       *AffectDelta       `json:"affect,omitempty"`
	Persona      *PersonaDelta      `json:"persona,omitempty"`
}

// Proposal is a concise alias used by application adapters.
type Proposal = ReflectionProposal

// ReflectionResult is an in-memory, validated projection. Persistence and
// audit adapters should write State/Proposal atomically with their own
// version/parent checks; Engine itself performs no durable writes.
type ReflectionResult struct {
	ProfileID          domain.ID          `json:"profile_id"`
	RunID              domain.ID          `json:"run_id"`
	Outcome            Outcome            `json:"outcome"`
	Decision           Decision           `json:"decision"`
	Proposal           ReflectionProposal `json:"proposal"`
	State              ReflectionState    `json:"state"`
	AffectDecayChanged bool               `json:"affect_decay_changed"`
	Usage              Usage              `json:"usage,omitempty"`
	StartedAt          time.Time          `json:"started_at"`
	FinishedAt         time.Time          `json:"finished_at"`
}

func (r ReflectionResult) Changed() bool  { return r.Outcome == OutcomeChanged }
func (r ReflectionResult) NoChange() bool { return r.Outcome == OutcomeNoChange }

// CanPersistAffectDecay reports whether State.Affect contains a decay
// projection that a caller may append to its own durable state. Decay is
// intentionally not persisted for cooldown or guard-rejection results: those
// decisions did not authorize an internal state transition, even though the
// projected state is still returned for the analyzer/result consumer.
func (r ReflectionResult) CanPersistAffectDecay() bool {
	if !r.AffectDecayChanged {
		return false
	}
	return r.Decision == DecisionApplied || r.Decision == DecisionNoChange
}
