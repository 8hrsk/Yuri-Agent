package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// PersonaOperation describes why a mutable persona revision was written. The
// operation is metadata only: it never changes the immutable policy or the
// identity seed.
type PersonaOperation string

const (
	PersonaOperationCreate   PersonaOperation = "create"
	PersonaOperationUpdate   PersonaOperation = "update"
	PersonaOperationRollback PersonaOperation = "rollback"
	PersonaOperationReset    PersonaOperation = "reset"
	PersonaOperationPin      PersonaOperation = "pin"
)

func (o PersonaOperation) Valid() bool {
	switch o {
	case PersonaOperationCreate, PersonaOperationUpdate, PersonaOperationRollback,
		PersonaOperationReset, PersonaOperationPin:
		return true
	default:
		return false
	}
}

// PersonaTraitName is deliberately a string rather than an enum. Product
// traits may grow over time (for example speech habits), while validation
// still rejects names that could be confused with immutable policy fields.
type PersonaTraitName string

const (
	TraitWarmth       PersonaTraitName = "warmth"
	TraitTrust        PersonaTraitName = "trust"
	TraitAttachment   PersonaTraitName = "attachment"
	TraitJealousy     PersonaTraitName = "jealousy"
	TraitIrritability PersonaTraitName = "irritability"
	TraitRomanticTone PersonaTraitName = "romantic_tone"
	TraitEmotionality PersonaTraitName = "emotionality"
	TraitDirectness   PersonaTraitName = "directness"
	TraitPlayfulness  PersonaTraitName = "playfulness"
	TraitFormality    PersonaTraitName = "formality"
	TraitInitiative   PersonaTraitName = "initiative"
)

// CommonPersonaTraits is the stable set used by the default seed. Custom
// traits are allowed when they use a safe snake_case name so this layer does
// not need to change whenever a new speaking habit is introduced.
var CommonPersonaTraits = map[PersonaTraitName]struct{}{
	TraitWarmth: {}, TraitTrust: {}, TraitAttachment: {}, TraitJealousy: {},
	TraitIrritability: {}, TraitRomanticTone: {}, TraitEmotionality: {},
	TraitDirectness: {}, TraitPlayfulness: {}, TraitFormality: {}, TraitInitiative: {},
}

// PersonaLimits bounds evolution. Values are normalized to [0, 1] and a
// regular reflection may move one trait by at most MaxDelta. Rollback/reset
// operations intentionally bypass MaxDelta because they restore an existing
// trusted snapshot rather than accepting a new model-generated value.
type PersonaLimits struct {
	MinValue        float64
	MaxValue        float64
	MaxDelta        float64
	MaxTraits       int
	MaxPromptBytes  int
	RequireEvidence bool
}

// DefaultPersonaLimits are conservative defaults for autonomous reflection.
// Applications may use stricter limits; loosening them should require an
// explicit product decision rather than happen through untrusted input.
var DefaultPersonaLimits = PersonaLimits{
	MinValue:        0,
	MaxValue:        1,
	MaxDelta:        0.15,
	MaxTraits:       64,
	MaxPromptBytes:  16 * 1024,
	RequireEvidence: true,
}

func (l PersonaLimits) valid() bool {
	return finite(l.MinValue) && finite(l.MaxValue) && finite(l.MaxDelta) &&
		l.MinValue <= l.MaxValue && l.MinValue >= -1 && l.MaxValue <= 1 &&
		l.MaxDelta >= 0 && l.MaxDelta <= 1 && l.MaxTraits >= 1 &&
		l.MaxTraits <= 1024 && l.MaxPromptBytes >= 0 && l.MaxPromptBytes <= 1<<20
}

// EvidenceLink points at durable evidence without copying potentially
// sensitive source text into persona/relationship state. ExcerptHash is an
// integrity hint, not the excerpt itself.
type EvidenceLink struct {
	ID             ID        `json:"id,omitempty"`
	SourceType     string    `json:"source_type"`
	SourceID       ID        `json:"source_id,omitempty"`
	RunID          ID        `json:"run_id,omitempty"`
	ConversationID ID        `json:"conversation_id,omitempty"`
	MessageID      ID        `json:"message_id,omitempty"`
	ExcerptHash    string    `json:"excerpt_hash,omitempty"`
	Provenance     string    `json:"provenance,omitempty"`
	UserConfirmed  bool      `json:"user_confirmed,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// Evidence and PersonaEvidence are aliases used by callers that prefer a
// shorter or more specific name. They intentionally share one validation
// contract across persona, relationship, and affect writes.
type Evidence = EvidenceLink
type PersonaEvidence = EvidenceLink
type RelationshipEvidence = EvidenceLink

func (e EvidenceLink) Validate() error {
	if strings.TrimSpace(e.SourceType) == "" {
		return fmt.Errorf("%w: evidence source type is required", ErrInvalidArgument)
	}
	if e.SourceID.Empty() && e.RunID.Empty() && e.ConversationID.Empty() && e.MessageID.Empty() && strings.TrimSpace(e.ExcerptHash) == "" {
		return fmt.Errorf("%w: evidence must reference a durable source", ErrInvalidArgument)
	}
	// Zero evidence timestamps are accepted for candidates and filled from the
	// parent revision by repositories. A non-zero value is retained verbatim.
	return nil
}

func (e EvidenceLink) Valid() bool { return e.Validate() == nil }

// MutablePersona is one immutable journal snapshot of the mutable persona.
// The identity seed and immutable policy are intentionally absent: no persona
// write can mutate either boundary by construction.
type MutablePersona struct {
	ID             ID                 `json:"id"`
	RevisionID     ID                 `json:"revision_id,omitempty"`
	Version        uint64             `json:"version"`
	ParentID       ID                 `json:"parent_id,omitempty"`
	ParentVersion  uint64             `json:"parent_version,omitempty"`
	Operation      PersonaOperation   `json:"operation,omitempty"`
	Traits         map[string]float64 `json:"traits"`
	Diff           map[string]float64 `json:"diff,omitempty"`
	PinnedTraits   []string           `json:"pinned_traits,omitempty"`
	IdentityPrompt string             `json:"identity_prompt,omitempty"`
	// PromptText follows the storage/data-model naming. IdentityPrompt is the
	// domain-facing name; repositories normalize both to the same value.
	PromptText  string         `json:"prompt_text,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	Evidence    []EvidenceLink `json:"evidence,omitempty"`
	AuthorRunID ID             `json:"author_run_id,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// PersonaVersion is the architectural name for an immutable persona
// snapshot. It is an alias so callers can use either vocabulary without
// converting values or weakening the versioned boundary.
type PersonaVersion = MutablePersona

// PersonaVersionRecord is an immutable history envelope shared by storage
// adapters and reflection/application services.
type PersonaVersionRecord struct {
	Persona       MutablePersona     `json:"persona"`
	RevisionID    ID                 `json:"revision_id"`
	ParentID      ID                 `json:"parent_id,omitempty"`
	ParentVersion uint64             `json:"parent_version,omitempty"`
	Operation     PersonaOperation   `json:"operation"`
	Diff          map[string]float64 `json:"diff,omitempty"`
	Reason        string             `json:"reason,omitempty"`
	Evidence      []EvidenceLink     `json:"evidence,omitempty"`
	AuthorRunID   ID                 `json:"author_run_id,omitempty"`
}

func NewMutablePersona(id ID, traits map[string]float64, prompt string, now time.Time) (MutablePersona, error) {
	if id.Empty() || now.IsZero() {
		return MutablePersona{}, fmt.Errorf("%w: persona id and timestamp are required", ErrInvalidArgument)
	}
	result := MutablePersona{
		ID: id, Version: 1, Operation: PersonaOperationCreate,
		Traits: cloneFloatMap(traits), IdentityPrompt: strings.TrimSpace(prompt),
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if err := result.Validate(); err != nil {
		return MutablePersona{}, err
	}
	return result, nil
}

// Prompt returns the normalized prompt text regardless of which compatibility
// field a caller populated.
func (p MutablePersona) Prompt() string {
	if strings.TrimSpace(p.IdentityPrompt) != "" {
		return p.IdentityPrompt
	}
	return p.PromptText
}

func (p MutablePersona) Valid() bool { return p.Validate() == nil }

func (p MutablePersona) Validate() error {
	limits := DefaultPersonaLimits
	if err := p.ValidateWithLimits(limits); err != nil {
		return err
	}
	if p.Operation != "" && !p.Operation.Valid() {
		return fmt.Errorf("%w: invalid persona operation %q", ErrInvalidArgument, p.Operation)
	}
	if strings.TrimSpace(p.IdentityPrompt) != "" && strings.TrimSpace(p.PromptText) != "" && strings.TrimSpace(p.IdentityPrompt) != strings.TrimSpace(p.PromptText) {
		return fmt.Errorf("%w: identity_prompt and prompt_text differ", ErrInvalidArgument)
	}
	for _, evidence := range p.Evidence {
		if err := evidence.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (p MutablePersona) ValidateWithLimits(limits PersonaLimits) error {
	if !limits.valid() {
		return fmt.Errorf("%w: invalid persona limits", ErrInvalidArgument)
	}
	if p.ID.Empty() || p.Version == 0 {
		return fmt.Errorf("%w: persona id and positive version are required", ErrInvalidArgument)
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: persona timestamps are required", ErrInvalidArgument)
	}
	if len(p.Traits) > limits.MaxTraits {
		return fmt.Errorf("%w: too many persona traits", ErrInvalidArgument)
	}
	for name, value := range p.Traits {
		if err := ValidatePersonaTrait(name, value, limits); err != nil {
			return err
		}
	}
	if err := validatePinnedTraits(p.Traits, p.PinnedTraits); err != nil {
		return err
	}
	if len(p.Diff) > limits.MaxTraits {
		return fmt.Errorf("%w: too many persona diff traits", ErrInvalidArgument)
	}
	for name, value := range p.Diff {
		if err := ValidatePersonaTrait(name, value, PersonaLimits{MinValue: -1, MaxValue: 1, MaxDelta: 1, MaxTraits: limits.MaxTraits, MaxPromptBytes: limits.MaxPromptBytes}); err != nil {
			return err
		}
	}
	if limits.MaxPromptBytes > 0 && len([]byte(p.Prompt())) > limits.MaxPromptBytes {
		return fmt.Errorf("%w: persona prompt is too large", ErrInvalidArgument)
	}
	return nil
}

func ValidatePersonaTrait(name string, value float64, limits PersonaLimits) error {
	name = strings.TrimSpace(name)
	if !validTraitName(name) {
		return fmt.Errorf("%w: invalid persona trait name %q", ErrInvalidArgument, name)
	}
	if !finite(value) || value < limits.MinValue || value > limits.MaxValue {
		return fmt.Errorf("%w: persona trait %q is out of range", ErrInvalidArgument, name)
	}
	return nil
}

func ValidatePersonaTraits(traits map[string]float64) error {
	limits := DefaultPersonaLimits
	if len(traits) > limits.MaxTraits {
		return fmt.Errorf("%w: too many persona traits", ErrInvalidArgument)
	}
	for name, value := range traits {
		if err := ValidatePersonaTrait(name, value, limits); err != nil {
			return err
		}
	}
	return nil
}

func validatePinnedTraits(traits map[string]float64, pinned []string) error {
	seen := make(map[string]struct{}, len(pinned))
	for _, raw := range pinned {
		name := strings.TrimSpace(raw)
		if !validTraitName(name) {
			return fmt.Errorf("%w: invalid pinned persona trait %q", ErrInvalidArgument, raw)
		}
		if _, ok := traits[name]; !ok {
			return fmt.Errorf("%w: pinned trait %q is not present", ErrInvalidArgument, name)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("%w: duplicate pinned trait %q", ErrInvalidArgument, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func validTraitName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for index, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (r == '_' && index > 0) {
			continue
		}
		return false
	}
	if name[0] == '_' || name[len(name)-1] == '_' {
		return false
	}
	return !immutablePersonaName(name)
}

func immutablePersonaName(name string) bool {
	for _, denied := range []string{
		"policy", "security", "capability", "capabilities", "grant", "permission",
		"approval", "audit", "retention", "file_root", "filesystem", "credential",
		"secret", "identity_seed", "allow", "deny", "safety_rule",
	} {
		if name == denied || strings.Contains(name, denied) {
			return true
		}
	}
	return false
}

// ValidatePersonaEvolution enforces max-delta and pin invariants for a
// regular autonomous update. Rollback/reset are handled by repositories with
// explicit operation metadata and are intentionally not passed here.
func ValidatePersonaEvolution(previous, next MutablePersona, limits PersonaLimits) error {
	if err := previous.ValidateWithLimits(limits); err != nil {
		return err
	}
	if err := next.ValidateWithLimits(limits); err != nil {
		return err
	}
	if next.Version != previous.Version+1 {
		return fmt.Errorf("%w: persona version must advance by one", ErrConflict)
	}
	if next.ParentVersion != 0 && next.ParentVersion != previous.Version {
		return fmt.Errorf("%w: persona parent version does not match current version", ErrConflict)
	}
	previousPinned := make(map[string]struct{}, len(previous.PinnedTraits))
	for _, name := range previous.PinnedTraits {
		previousPinned[name] = struct{}{}
		value, ok := next.Traits[name]
		if !ok || value != previous.Traits[name] {
			return fmt.Errorf("%w: pinned persona trait %q cannot be changed", ErrNotPermitted, name)
		}
	}
	for name, oldValue := range previous.Traits {
		newValue, ok := next.Traits[name]
		if !ok {
			if _, pinned := previousPinned[name]; pinned {
				return fmt.Errorf("%w: pinned persona trait %q cannot be removed", ErrNotPermitted, name)
			}
			continue
		}
		if math.Abs(newValue-oldValue) > limits.MaxDelta+1e-9 {
			return fmt.Errorf("%w: persona trait %q changed beyond max delta", ErrInvalidArgument, name)
		}
	}
	if limits.RequireEvidence && len(next.Evidence) == 0 {
		return fmt.Errorf("%w: persona evolution requires evidence", ErrInvalidArgument)
	}
	if strings.TrimSpace(next.Reason) == "" {
		return fmt.Errorf("%w: persona evolution reason is required", ErrInvalidArgument)
	}
	return nil
}

func (p MutablePersona) DeltaFrom(previous MutablePersona) map[string]float64 {
	result := make(map[string]float64)
	for name, value := range p.Traits {
		result[name] = value - previous.Traits[name]
	}
	for name, value := range previous.Traits {
		if _, ok := p.Traits[name]; !ok {
			result[name] = -value
		}
	}
	return result
}

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

// RelationshipState is a versioned snapshot of Yuri's subjective model of
// the owner. Dimensions and opinions are stored separately from factual
// memory and have no policy authority.
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

// AffectOperation identifies an immutable affect snapshot revision.
type AffectOperation string

const (
	AffectOperationCreate   AffectOperation = "create"
	AffectOperationEvent    AffectOperation = "event"
	AffectOperationUpdate   AffectOperation = "update"
	AffectOperationRollback AffectOperation = "rollback"
	AffectOperationReset    AffectOperation = "reset"
)

func (o AffectOperation) Valid() bool {
	switch o {
	case AffectOperationCreate, AffectOperationEvent, AffectOperationUpdate,
		AffectOperationRollback, AffectOperationReset:
		return true
	default:
		return false
	}
}

type AffectiveDecayPolicy string

const (
	AffectiveDecayNone        AffectiveDecayPolicy = "none"
	AffectiveDecayLinear      AffectiveDecayPolicy = "linear"
	AffectiveDecayExponential AffectiveDecayPolicy = "exponential"
)

func (p AffectiveDecayPolicy) Valid() bool {
	switch p {
	case "", AffectiveDecayNone, AffectiveDecayLinear, AffectiveDecayExponential:
		return true
	default:
		return false
	}
}

// Emotion names are extensible but the product's initial set is provided as
// constants. Values in AffectiveState are signed [-1,1] contributions; event
// intensity is unsigned and valence supplies the sign.
const (
	EmotionSympathy   = "sympathy"
	EmotionTenderness = "tenderness"
	EmotionJoy        = "joy"
	EmotionGratitude  = "gratitude"
	EmotionLonging    = "longing"
	EmotionAnger      = "anger"
	EmotionIrritation = "irritation"
	EmotionJealousy   = "jealousy"
	EmotionResentment = "resentment"
	EmotionAnxiety    = "anxiety"
)

type AffectiveEvent struct {
	ID              ID                   `json:"id"`
	AffectID        ID                   `json:"affect_id,omitempty"`
	StateVersion    uint64               `json:"state_version,omitempty"`
	SourceID        ID                   `json:"source_id,omitempty"`
	SourceType      string               `json:"source_type,omitempty"`
	RunID           ID                   `json:"run_id,omitempty"`
	ConversationID  ID                   `json:"conversation_id,omitempty"`
	Emotion         string               `json:"emotion"`
	Intensity       float64              `json:"intensity"`
	Valence         float64              `json:"valence"`
	DecayPolicy     AffectiveDecayPolicy `json:"decay_policy,omitempty"`
	DecayRate       float64              `json:"decay_rate,omitempty"`
	HalfLifeSeconds int64                `json:"half_life_seconds,omitempty"`
	DecaysAt        time.Time            `json:"decays_at,omitempty"`
	Provenance      string               `json:"provenance,omitempty"`
	Evidence        []EvidenceLink       `json:"evidence,omitempty"`
	MetadataJSON    string               `json:"metadata_json,omitempty"`
	CreatedAt       time.Time            `json:"created_at"`
}

func (e AffectiveEvent) Valid() bool { return e.Validate() == nil }

func (e AffectiveEvent) Validate() error {
	if e.ID.Empty() || strings.TrimSpace(e.Emotion) == "" || e.CreatedAt.IsZero() {
		return fmt.Errorf("%w: affect event id, emotion and timestamp are required", ErrInvalidArgument)
	}
	if !finite(e.Intensity) || e.Intensity < 0 || e.Intensity > 1 {
		return fmt.Errorf("%w: affect event intensity is out of range", ErrInvalidArgument)
	}
	if !finite(e.Valence) || e.Valence < -1 || e.Valence > 1 {
		return fmt.Errorf("%w: affect event valence is out of range", ErrInvalidArgument)
	}
	if !e.DecayPolicy.Valid() || !finite(e.DecayRate) || e.DecayRate < 0 || e.DecayRate > 1e6 {
		return fmt.Errorf("%w: affect event decay metadata is invalid", ErrInvalidArgument)
	}
	if e.HalfLifeSeconds < 0 || e.HalfLifeSeconds > 100*365*24*60*60 {
		return fmt.Errorf("%w: affect event half-life is invalid", ErrInvalidArgument)
	}
	if !e.DecaysAt.IsZero() && e.DecaysAt.Before(e.CreatedAt) {
		return fmt.Errorf("%w: affect event decays_at precedes created_at", ErrInvalidArgument)
	}
	if strings.TrimSpace(e.MetadataJSON) != "" && !json.Valid([]byte(e.MetadataJSON)) {
		return fmt.Errorf("%w: affect event metadata_json must be valid JSON", ErrInvalidArgument)
	}
	for _, evidence := range e.Evidence {
		if err := evidence.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// EffectiveIntensity computes the event's contribution at at without mutating
// the immutable event. No-decay events remain constant; decaying events reach
// zero at DecaysAt or follow their explicit half-life.
func (e AffectiveEvent) EffectiveIntensity(at time.Time) float64 {
	if at.IsZero() {
		at = e.CreatedAt
	}
	if at.Before(e.CreatedAt) || e.Intensity == 0 {
		return 0
	}
	if e.DecayPolicy == AffectiveDecayNone || (e.DecaysAt.IsZero() && e.HalfLifeSeconds <= 0) {
		return e.Intensity
	}
	if !e.DecaysAt.IsZero() && !at.Before(e.DecaysAt) {
		return 0
	}
	if e.DecayPolicy == AffectiveDecayExponential && e.HalfLifeSeconds > 0 {
		seconds := at.Sub(e.CreatedAt).Seconds()
		return e.Intensity * math.Pow(0.5, seconds/float64(e.HalfLifeSeconds))
	}
	if e.DecaysAt.IsZero() {
		return e.Intensity
	}
	remaining := e.DecaysAt.Sub(at).Seconds() / e.DecaysAt.Sub(e.CreatedAt).Seconds()
	if remaining < 0 {
		return 0
	}
	return e.Intensity * remaining
}

type AffectiveState struct {
	ID            ID                 `json:"id"`
	RevisionID    ID                 `json:"revision_id,omitempty"`
	Version       uint64             `json:"version"`
	ParentID      ID                 `json:"parent_id,omitempty"`
	ParentVersion uint64             `json:"parent_version,omitempty"`
	Operation     AffectOperation    `json:"operation,omitempty"`
	Emotions      map[string]float64 `json:"emotions"`
	// Dimensions and Values are compatibility aliases for callers that use a
	// generic affect/mood vocabulary. Repositories normalize one source.
	Dimensions  map[string]float64 `json:"dimensions,omitempty"`
	Values      map[string]float64 `json:"values,omitempty"`
	Summary     string             `json:"summary,omitempty"`
	Reason      string             `json:"reason,omitempty"`
	AuthorRunID ID                 `json:"author_run_id,omitempty"`
	AsOf        time.Time          `json:"as_of,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

type AffectiveVersionRecord struct {
	State         AffectiveState  `json:"state"`
	RevisionID    ID              `json:"revision_id"`
	ParentID      ID              `json:"parent_id,omitempty"`
	ParentVersion uint64          `json:"parent_version,omitempty"`
	Operation     AffectOperation `json:"operation"`
	Reason        string          `json:"reason,omitempty"`
	AuthorRunID   ID              `json:"author_run_id,omitempty"`
}

// AffectiveEventRecord joins an event to the state revision produced by an
// atomic AppendEvent call.
type AffectiveEventRecord struct {
	Event        AffectiveEvent `json:"event"`
	StateVersion uint64         `json:"state_version"`
}

func NewAffectiveState(id ID, emotions map[string]float64, summary string, now time.Time) (AffectiveState, error) {
	if id.Empty() || now.IsZero() {
		return AffectiveState{}, fmt.Errorf("%w: affect state id and timestamp are required", ErrInvalidArgument)
	}
	result := AffectiveState{ID: id, Version: 1, Operation: AffectOperationCreate,
		Emotions: cloneFloatMap(emotions), Summary: strings.TrimSpace(summary),
		AsOf: now.UTC(), CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	if err := result.Validate(); err != nil {
		return AffectiveState{}, err
	}
	return result, nil
}

func (a AffectiveState) effectiveValues() map[string]float64 {
	if a.Emotions != nil {
		return a.Emotions
	}
	if a.Dimensions != nil {
		return a.Dimensions
	}
	return a.Values
}

func (a AffectiveState) Valid() bool { return a.Validate() == nil }

func (a AffectiveState) Validate() error {
	if a.ID.Empty() || a.Version == 0 || a.CreatedAt.IsZero() || a.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: affect state id, version and timestamps are required", ErrInvalidArgument)
	}
	if a.Operation != "" && !a.Operation.Valid() {
		return fmt.Errorf("%w: invalid affect operation %q", ErrInvalidArgument, a.Operation)
	}
	if a.Emotions != nil && a.Dimensions != nil && !floatMapsEqual(a.Emotions, a.Dimensions) {
		return fmt.Errorf("%w: affect emotions differ from dimensions", ErrInvalidArgument)
	}
	if a.Emotions != nil && a.Values != nil && !floatMapsEqual(a.Emotions, a.Values) {
		return fmt.Errorf("%w: affect emotions differ from values", ErrInvalidArgument)
	}
	for name, value := range a.effectiveValues() {
		if !validEmotionName(name) || !finite(value) || value < -1 || value > 1 {
			return fmt.Errorf("%w: affect value %q is invalid", ErrInvalidArgument, name)
		}
	}
	return nil
}

// ApplyEvent returns a new state with one event's signed contribution applied.
// The caller/repository persists the returned state and event atomically.
func (a AffectiveState) ApplyEvent(event AffectiveEvent, at time.Time) (AffectiveState, error) {
	if err := a.Validate(); err != nil {
		return AffectiveState{}, err
	}
	if err := event.Validate(); err != nil {
		return AffectiveState{}, err
	}
	if at.IsZero() {
		at = event.CreatedAt
	}
	result := a
	result.Emotions = cloneFloatMap(a.effectiveValues())
	if result.Emotions == nil {
		result.Emotions = make(map[string]float64)
	}
	name := strings.TrimSpace(event.Emotion)
	result.Emotions[name] = clamp(result.Emotions[name]+event.EffectiveIntensity(at)*event.Valence, -1, 1)
	result.Dimensions = nil
	result.Values = nil
	result.Version++
	result.ParentVersion = a.Version
	result.Operation = AffectOperationEvent
	result.AsOf = at.UTC()
	result.UpdatedAt = at.UTC()
	return result, nil
}

func (a AffectiveState) Decay(events []AffectiveEvent, at time.Time) AffectiveState {
	result := a
	if at.IsZero() {
		at = a.UpdatedAt
	}
	values := make(map[string]float64, len(a.effectiveValues()))
	for name := range a.effectiveValues() {
		values[name] = 0
	}
	for _, event := range events {
		values[event.Emotion] += event.EffectiveIntensity(at) * event.Valence
	}
	for name, value := range values {
		values[name] = clamp(value, -1, 1)
	}
	result.Emotions = values
	result.Dimensions = nil
	result.Values = nil
	result.AsOf = at.UTC()
	result.UpdatedAt = at.UTC()
	return result
}

func validDimensionName(name string) bool {
	return validSimpleName(name)
}

func validEmotionName(name string) bool {
	return validSimpleName(name)
}

func validSimpleName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for index, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (r == '_' && index > 0) {
			continue
		}
		return false
	}
	return name[0] != '_' && name[len(name)-1] != '_'
}

func cloneFloatMap(input map[string]float64) map[string]float64 {
	if input == nil {
		return nil
	}
	result := make(map[string]float64, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func floatMapsEqual(left, right map[string]float64) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if other, ok := right[key]; !ok || other != value {
			return false
		}
	}
	return true
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func clamp(value, minimum, maximum float64) float64 {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

// SortedPersonaTraitNames returns a deterministic copy for JSON/storage
// normalization and UI history rendering.
func SortedPersonaTraitNames(names []string) []string {
	result := append([]string(nil), names...)
	for index := range result {
		result[index] = strings.TrimSpace(result[index])
	}
	sort.Strings(result)
	return result
}
