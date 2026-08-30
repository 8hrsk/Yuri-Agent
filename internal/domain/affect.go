package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

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
	EmotionSympathy      = "sympathy"
	EmotionTenderness    = "tenderness"
	EmotionJoy           = "joy"
	EmotionGratitude     = "gratitude"
	EmotionLonging       = "longing"
	EmotionAnger         = "anger"
	EmotionIrritation    = "irritation"
	EmotionJealousy      = "jealousy"
	EmotionResentment    = "resentment"
	EmotionAnxiety       = "anxiety"
	EmotionFear          = "fear"
	EmotionEmbarrassment = "embarrassment"
	EmotionBoredom       = "boredom"
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
