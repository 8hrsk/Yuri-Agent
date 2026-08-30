package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// RelationshipOperation is the journal operation for relationship snapshots.
type RelationshipOperation string

const (
	RelationshipOperationCreate   RelationshipOperation = "create"
	RelationshipOperationUpdate   RelationshipOperation = "update"
	RelationshipOperationRollback RelationshipOperation = "rollback"
	RelationshipOperationReset    RelationshipOperation = "reset"
)

func (o RelationshipOperation) Valid() bool {
	switch o {
	case RelationshipOperationCreate, RelationshipOperationUpdate,
		RelationshipOperationRollback, RelationshipOperationReset:
		return true
	default:
		return false
	}
}

const (
	RelationshipDimensionTrust       = "trust"
	RelationshipDimensionAttachment  = "attachment"
	RelationshipDimensionRespect     = "respect"
	RelationshipDimensionIrritation  = "irritation"
	RelationshipDimensionJealousy    = "jealousy"
	RelationshipDimensionResentment  = "resentment"
	RelationshipDimensionGratitude   = "gratitude"
	RelationshipDimensionCloseness   = "closeness"
	RelationshipDimensionReliability = "reliability"
)

// RelationshipOpinion is a subjective inference, never a replacement for a
// factual memory. Content/Claim/Value are compatibility spellings; callers
// should normally use Claim.
type RelationshipOpinion struct {
	ID            ID             `json:"id,omitempty"`
	Subject       string         `json:"subject"`
	Topic         string         `json:"topic,omitempty"`
	Label         string         `json:"label,omitempty"`
	Claim         string         `json:"claim,omitempty"`
	Content       string         `json:"content,omitempty"`
	Value         string         `json:"value,omitempty"`
	Confidence    float64        `json:"confidence"`
	Evidence      []EvidenceLink `json:"evidence,omitempty"`
	Provenance    string         `json:"provenance,omitempty"`
	ProvenanceRef *EvidenceLink  `json:"provenance_ref,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at,omitempty"`
}

func (o RelationshipOpinion) Text() string {
	if strings.TrimSpace(o.Claim) != "" {
		return o.Claim
	}
	if strings.TrimSpace(o.Content) != "" {
		return o.Content
	}
	return o.Value
}

func (o RelationshipOpinion) Valid() bool { return o.Validate() == nil }

func (o RelationshipOpinion) Validate() error {
	if strings.TrimSpace(o.Subject) == "" || strings.TrimSpace(o.Text()) == "" {
		return fmt.Errorf("%w: relationship opinion subject and claim are required", ErrInvalidArgument)
	}
	if !finite(o.Confidence) || o.Confidence < 0 || o.Confidence > 1 {
		return fmt.Errorf("%w: relationship opinion confidence is out of range", ErrInvalidArgument)
	}
	if o.Label != "" && o.Label != "opinion" && o.Label != "inference" {
		return fmt.Errorf("%w: relationship opinion label must be opinion or inference", ErrInvalidArgument)
	}
	if len(o.Evidence) == 0 {
		return fmt.Errorf("%w: relationship opinion requires evidence", ErrInvalidArgument)
	}
	for _, evidence := range o.Evidence {
		if err := evidence.Validate(); err != nil {
			return err
		}
	}
	if o.ProvenanceRef != nil {
		if err := o.ProvenanceRef.Validate(); err != nil {
			return fmt.Errorf("%w: invalid relationship opinion provenance: %v", ErrInvalidArgument, err)
		}
	}
	return nil
}

// RelationshipState is a versioned snapshot of one subjective relationship.
// The primary state keyed by agent ID models the owner; directional peer
// states use separate derived IDs and an explicit observer/subject mapping.
// Dimensions and opinions are not factual memory and have no policy authority.
type RelationshipState struct {
	ID            ID                    `json:"id"`
	RevisionID    ID                    `json:"revision_id,omitempty"`
	Version       uint64                `json:"version"`
	ParentID      ID                    `json:"parent_id,omitempty"`
	ParentVersion uint64                `json:"parent_version,omitempty"`
	Operation     RelationshipOperation `json:"operation,omitempty"`
	Dimensions    map[string]float64    `json:"dimensions"`
	// DimensionsJSON follows the storage/data-model spelling. Repositories
	// normalize it to Dimensions and reject disagreement.
	DimensionsJSON string                `json:"dimensions_json,omitempty"`
	Opinions       []RelationshipOpinion `json:"opinions,omitempty"`
	Summary        string                `json:"summary,omitempty"`
	Reason         string                `json:"reason,omitempty"`
	Evidence       []EvidenceLink        `json:"evidence,omitempty"`
	AuthorRunID    ID                    `json:"author_run_id,omitempty"`
	CreatedAt      time.Time             `json:"created_at"`
	UpdatedAt      time.Time             `json:"updated_at"`
}

type RelationshipVersionRecord struct {
	Relationship  RelationshipState     `json:"relationship"`
	RevisionID    ID                    `json:"revision_id"`
	ParentID      ID                    `json:"parent_id,omitempty"`
	ParentVersion uint64                `json:"parent_version,omitempty"`
	Operation     RelationshipOperation `json:"operation"`
	Reason        string                `json:"reason,omitempty"`
	Evidence      []EvidenceLink        `json:"evidence,omitempty"`
	AuthorRunID   ID                    `json:"author_run_id,omitempty"`
}

func NewRelationshipState(id ID, dimensions map[string]float64, summary string, now time.Time) (RelationshipState, error) {
	if id.Empty() || now.IsZero() {
		return RelationshipState{}, fmt.Errorf("%w: relationship id and timestamp are required", ErrInvalidArgument)
	}
	result := RelationshipState{ID: id, Version: 1, Operation: RelationshipOperationCreate,
		Dimensions: cloneFloatMap(dimensions), Summary: strings.TrimSpace(summary), CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	if err := result.Validate(); err != nil {
		return RelationshipState{}, err
	}
	return result, nil
}

// NewOwnerRelationshipState projects the current owner-authored relationship
// seed into an independent mutable state. The seed remains append-only and is
// referenced as provenance; fictional custom history is never promoted to a
// factual memory by this projection.
func NewOwnerRelationshipState(seed PersonalizationSeed, now time.Time) (RelationshipState, error) {
	if err := seed.Validate(); err != nil {
		return RelationshipState{}, err
	}
	state, err := NewRelationshipState(seed.AgentID, seed.RelationshipSeed.Dimensions, seed.RelationshipSeed.Summary, now)
	if err != nil {
		return RelationshipState{}, err
	}
	provenance := "owner_relationship_seed"
	if seed.RelationshipSeed.Preset == RelationshipSeedCustom {
		provenance = "fictional_owner_relationship_seed"
	}
	state.Reason = "relationship initialized from owner seed: " + string(seed.RelationshipSeed.Preset)
	state.Evidence = []EvidenceLink{{
		ID: ID(fmt.Sprintf("%s:relationship-seed:v%d", seed.AgentID, seed.Version)), SourceType: "personalization_seed",
		SourceID: seed.RevisionID, Provenance: provenance, UserConfirmed: true, CreatedAt: now.UTC(),
	}}
	if err := state.Validate(); err != nil {
		return RelationshipState{}, err
	}
	return state, nil
}

// NewPeerRelationshipState projects only stable social predispositions from
// the observer profile. It deliberately does not reuse the owner's relationship
// story or parse backstory text, so a peer starts as a distinct subject rather
// than inheriting closeness or romance intended for the owner.
func NewPeerRelationshipState(id ID, observer PersonalizationSeed, now time.Time) (RelationshipState, error) {
	if id.Empty() || now.IsZero() {
		return RelationshipState{}, fmt.Errorf("%w: peer relationship id and timestamp are required", ErrInvalidArgument)
	}
	if err := observer.Validate(); err != nil {
		return RelationshipState{}, err
	}
	temperament := observer.Temperament
	dimensions := map[string]float64{
		RelationshipDimensionTrust:       clamp(.25+.35*temperament.Trust, 0, 1),
		RelationshipDimensionAttachment:  clamp(.08+.20*temperament.Attachment, 0, 1),
		RelationshipDimensionRespect:     clamp(.42+.16*temperament.Empathy+.12*temperament.Curiosity, 0, 1),
		RelationshipDimensionCloseness:   clamp(.10+.25*temperament.Sociability, 0, 1),
		RelationshipDimensionReliability: .50,
		RelationshipDimensionIrritation:  0,
		RelationshipDimensionJealousy:    0,
		RelationshipDimensionResentment:  0,
		RelationshipDimensionGratitude:   0,
	}
	state, err := NewRelationshipState(id, dimensions, "Новое знакомство с peer; исходные сигналы отражают только социальные предрасположенности наблюдателя.", now)
	if err != nil {
		return RelationshipState{}, err
	}
	state.Reason = "peer relationship initialized from observer temperament"
	state.Evidence = []EvidenceLink{{
		ID: ID(fmt.Sprintf("%s:peer-relationship-seed:v%d", observer.AgentID, observer.Version)), SourceType: "personalization_seed",
		SourceID: observer.RevisionID, Provenance: "observer_temperament_seed", UserConfirmed: true, CreatedAt: now.UTC(),
	}}
	if err := state.Validate(); err != nil {
		return RelationshipState{}, err
	}
	return state, nil
}

func (r RelationshipState) Valid() bool { return r.Validate() == nil }

func (r RelationshipState) Validate() error {
	if r.ID.Empty() || r.Version == 0 || r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: relationship id, version and timestamps are required", ErrInvalidArgument)
	}
	if r.Operation != "" && !r.Operation.Valid() {
		return fmt.Errorf("%w: invalid relationship operation %q", ErrInvalidArgument, r.Operation)
	}
	if r.DimensionsJSON != "" {
		var decoded map[string]float64
		if err := json.Unmarshal([]byte(r.DimensionsJSON), &decoded); err != nil {
			return fmt.Errorf("%w: relationship dimensions_json must be an object", ErrInvalidArgument)
		}
		if r.Dimensions != nil && !floatMapsEqual(r.Dimensions, decoded) {
			return fmt.Errorf("%w: relationship dimensions differ from dimensions_json", ErrInvalidArgument)
		}
	}
	for name, value := range r.Dimensions {
		if !validDimensionName(name) || !finite(value) || value < 0 || value > 1 {
			return fmt.Errorf("%w: relationship dimension %q is invalid", ErrInvalidArgument, name)
		}
	}
	for _, opinion := range r.Opinions {
		if err := opinion.Validate(); err != nil {
			return err
		}
	}
	for _, evidence := range r.Evidence {
		if err := evidence.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func ValidateRelationshipEvolution(previous, next RelationshipState, maxDelta float64) error {
	if err := previous.Validate(); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if !finite(maxDelta) || maxDelta < 0 || maxDelta > 1 {
		return fmt.Errorf("%w: invalid relationship max delta", ErrInvalidArgument)
	}
	if next.Version != previous.Version+1 {
		return fmt.Errorf("%w: relationship version must advance by one", ErrConflict)
	}
	for name, oldValue := range previous.Dimensions {
		if newValue, ok := next.Dimensions[name]; ok && math.Abs(newValue-oldValue) > maxDelta+1e-9 {
			return fmt.Errorf("%w: relationship dimension %q changed beyond max delta", ErrInvalidArgument, name)
		}
	}
	if strings.TrimSpace(next.Reason) == "" {
		return fmt.Errorf("%w: relationship evolution reason is required", ErrInvalidArgument)
	}
	return nil
}
