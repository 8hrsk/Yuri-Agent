// Package reflection contains the provider-neutral safety core for Yuri's
// background self-reflection.  It deliberately has no persistence, desktop,
// prompt-builder, or provider dependencies.  A run receives an immutable
// typed snapshot and returns a validated projection that an application
// service may persist atomically through its own adapter.
package reflection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

var (
	// ErrInvalidSnapshot means that a reflection request is incomplete or
	// contains malformed, unverifiable input.
	ErrInvalidSnapshot = errors.New("reflection: invalid input snapshot")
	// ErrInvalidProposal means that the analyzer returned a malformed or
	// semantically inconsistent proposal.
	ErrInvalidProposal = errors.New("reflection: invalid proposal")
	// ErrSchema means that model output is not the strict reflection schema.
	ErrSchema = errors.New("reflection: schema validation failed")
	// ErrNoAnalyzer means that the reflection engine has no analyzer port.
	ErrNoAnalyzer = errors.New("reflection: analyzer is not configured")
	// ErrNoModel means that the model-backed analyzer has no model port.
	ErrNoModel = errors.New("reflection: model is not configured")
	// ErrBudgetExceeded means that a reflection run exceeded an explicit
	// input, output, token, evidence, or wall-clock budget.
	ErrBudgetExceeded = errors.New("reflection: run budget exceeded")
	// ErrInsufficientEvidence means that a proposed change does not have the
	// configured minimum number/weight of independent evidence references.
	ErrInsufficientEvidence = errors.New("reflection: insufficient evidence")
	// ErrDeltaExceeded means that a proposed scalar change is larger than the
	// configured bounded delta.
	ErrDeltaExceeded = errors.New("reflection: maximum delta exceeded")
	// ErrOutOfRange means that applying a proposed change would leave a trait or
	// state dimension outside its configured range.
	ErrOutOfRange = errors.New("reflection: value outside configured range")
	// ErrPinnedTrait means that a proposal attempts to alter a pinned persona
	// trait.
	ErrPinnedTrait = errors.New("reflection: trait is pinned")
	// ErrForbiddenMutation means that a proposal tries to modify an immutable
	// security/identity boundary or embeds an instruction that could do so.
	ErrForbiddenMutation = errors.New("reflection: forbidden mutation")
	// ErrUntrustedEvidence means that unconfirmed external data was selected as
	// the basis for a mutable persona/identity change.
	ErrUntrustedEvidence = errors.New("reflection: untrusted evidence cannot mutate identity")
	// ErrCooldown means that the profile's reflection cooldown has not elapsed.
	ErrCooldown = errors.New("reflection: cooldown is active")
	// ErrProfileBusy is returned by TryRun when another reflection is active for
	// the same profile.
	ErrProfileBusy = errors.New("reflection: profile already has an active run")
	// ErrOpinionLimit is returned when a relationship opinion would exceed the
	// configured count or content bound.
	ErrOpinionLimit = errors.New("reflection: subjective opinion limit exceeded")
)

// Trigger identifies why a reflection run was requested.
type Trigger string

const (
	TriggerPostTurn   Trigger = "post_turn"
	TriggerIdle       Trigger = "idle"
	TriggerCron       Trigger = "cron"
	TriggerBeforeComp Trigger = "before_compression"
	TriggerSessionEnd Trigger = "session_end"
	TriggerManual     Trigger = "manual"
)

// Valid reports whether the trigger is part of the stable reflection
// vocabulary. Unknown triggers are rejected so they cannot silently alter
// scheduling or audit semantics.
func (t Trigger) Valid() bool {
	switch t {
	case TriggerPostTurn, TriggerIdle, TriggerCron, TriggerBeforeComp,
		TriggerSessionEnd, TriggerManual:
		return true
	default:
		return false
	}
}

// Outcome is the only two outcomes an analyzer may request. Guards return a
// no-change result with an explanatory decision while malformed or unsafe
// proposals return an error and never produce an applied state.
type Outcome string

const (
	OutcomeNoChange Outcome = "no_change"
	OutcomeChanged  Outcome = "changed"
)

func (o Outcome) Valid() bool { return o == OutcomeNoChange || o == OutcomeChanged }

// Decision is a stable machine-readable explanation for a reflection result.
type Decision string

const (
	DecisionApplied     Decision = "applied"
	DecisionNoChange    Decision = "no_change"
	DecisionCooldown    Decision = "cooldown"
	DecisionNoEvidence  Decision = "insufficient_evidence"
	DecisionPinnedTrait Decision = "pinned_trait"
	DecisionDeltaLimit  Decision = "max_delta"
	DecisionUntrusted   Decision = "untrusted_evidence"
	DecisionBudget      Decision = "budget"
	DecisionCancelled   Decision = "cancelled"
)

// EvidenceSource is provenance for a snapshot item. External sources are
// intentionally distinct from user/agent transcript evidence.
type EvidenceSource string

const (
	EvidenceSourceUser       EvidenceSource = "user"
	EvidenceSourceAssistant  EvidenceSource = "assistant"
	EvidenceSourceSystem     EvidenceSource = "system"
	EvidenceSourceTranscript EvidenceSource = "transcript"
	EvidenceSourceMemory     EvidenceSource = "memory"
	EvidenceSourceTool       EvidenceSource = "tool"
	EvidenceSourceFile       EvidenceSource = "file"
	EvidenceSourceWeb        EvidenceSource = "web"
	EvidenceSourcePlugin     EvidenceSource = "plugin"
	EvidenceSourceReflection EvidenceSource = "reflection"
)

// Short aliases are useful to adapters while the EvidenceSource-prefixed
// constants make call sites self-documenting.
const (
	SourceUser       = EvidenceSourceUser
	SourceAssistant  = EvidenceSourceAssistant
	SourceSystem     = EvidenceSourceSystem
	SourceTranscript = EvidenceSourceTranscript
	SourceMemory     = EvidenceSourceMemory
	SourceTool       = EvidenceSourceTool
	SourceFile       = EvidenceSourceFile
	SourceWeb        = EvidenceSourceWeb
	SourcePlugin     = EvidenceSourcePlugin
	SourceReflection = EvidenceSourceReflection
)

func (s EvidenceSource) Valid() bool {
	switch s {
	case EvidenceSourceUser, EvidenceSourceAssistant, EvidenceSourceSystem,
		EvidenceSourceTranscript, EvidenceSourceMemory, EvidenceSourceTool,
		EvidenceSourceFile, EvidenceSourceWeb, EvidenceSourcePlugin,
		EvidenceSourceReflection:
		return true
	default:
		return false
	}
}

// EvidenceTrust is an assertion made by the adapter about provenance. The
// reflection package never promotes external content merely because it says
// it is trusted: external sources still require explicit user confirmation.
type EvidenceTrust string

const (
	EvidenceTrusted   EvidenceTrust = "trusted"
	EvidenceUntrusted EvidenceTrust = "untrusted"
)

const (
	TrustTrusted   = EvidenceTrusted
	TrustUntrusted = EvidenceUntrusted
)

func (t EvidenceTrust) Valid() bool { return t == EvidenceTrusted || t == EvidenceUntrusted }

// EvidenceID is an adapter-friendly alias for the canonical domain ID.
type EvidenceID = domain.ID

// Evidence is a bounded, provenance-bearing piece of data available to the
// analyzer. Content/Text/Excerpt are data only; no field is interpreted as an
// instruction by this package. Adapters should normally fill Content. Text
// and Excerpt are compatibility aliases for source-specific projections.
type Evidence struct {
	ID             domain.ID      `json:"id"`
	Source         EvidenceSource `json:"source"`
	SourceID       domain.ID      `json:"source_id,omitempty"`
	ConversationID domain.ID      `json:"conversation_id,omitempty"`
	MessageID      domain.ID      `json:"message_id,omitempty"`
	Content        string         `json:"content,omitempty"`
	Text           string         `json:"text,omitempty"`
	Excerpt        string         `json:"excerpt,omitempty"`
	Trust          EvidenceTrust  `json:"trust"`
	UserConfirmed  bool           `json:"user_confirmed,omitempty"`
	Weight         float64        `json:"weight,omitempty"`
	Confidence     float64        `json:"confidence,omitempty"`
	OccurredAt     time.Time      `json:"occurred_at"`
}

// Valid performs source-independent evidence validation. It intentionally
// permits untrusted evidence in a snapshot; a later semantic guard decides
// whether that evidence may support a persona mutation.
func (e Evidence) Valid() bool { return e.Validate() == nil }

func (e Evidence) Validate() error {
	if e.ID.Empty() || !e.Source.Valid() || !e.Trust.Valid() || e.OccurredAt.IsZero() {
		return fmt.Errorf("%w: evidence id, source, trust, and occurred_at are required", ErrInvalidSnapshot)
	}
	if strings.TrimSpace(e.Content) == "" && strings.TrimSpace(e.Text) == "" && strings.TrimSpace(e.Excerpt) == "" {
		return fmt.Errorf("%w: evidence %s has no bounded content", ErrInvalidSnapshot, e.ID)
	}
	if !finite(e.Weight) || e.Weight < 0 || e.Weight > 1 {
		return fmt.Errorf("%w: evidence %s weight is outside [0,1]", ErrInvalidSnapshot, e.ID)
	}
	if !finite(e.Confidence) || e.Confidence < 0 || e.Confidence > 1 {
		return fmt.Errorf("%w: evidence %s confidence is outside [0,1]", ErrInvalidSnapshot, e.ID)
	}
	if strings.ContainsRune(e.Content, '\x00') || strings.ContainsRune(e.Text, '\x00') || strings.ContainsRune(e.Excerpt, '\x00') {
		return fmt.Errorf("%w: evidence %s contains NUL", ErrInvalidSnapshot, e.ID)
	}
	return nil
}

// Data returns the preferred bounded text projection. It is intentionally a
// plain string; callers must keep it in an evidence/data envelope when
// constructing a model prompt.
func (e Evidence) Data() string {
	if value := strings.TrimSpace(e.Content); value != "" {
		return value
	}
	if value := strings.TrimSpace(e.Text); value != "" {
		return value
	}
	return strings.TrimSpace(e.Excerpt)
}

// External reports whether this item came from a source that cannot, by
// itself, establish a user fact or identity change.
func (e Evidence) External() bool {
	switch e.Source {
	case EvidenceSourceTool, EvidenceSourceFile, EvidenceSourceWeb, EvidenceSourcePlugin:
		return true
	default:
		return false
	}
}

// AllowsPersonaMutation is intentionally conservative. User confirmation
// can promote a previously external item, but a source adapter cannot bypass
// that confirmation by setting TrustTrusted.
func (e Evidence) AllowsPersonaMutation() bool {
	if e.UserConfirmed {
		return true
	}
	return !e.External() && e.Trust == EvidenceTrusted
}

// ValueRange bounds a scalar trait or state dimension.
type ValueRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

func (r ValueRange) Valid() bool { return finite(r.Min) && finite(r.Max) && r.Min <= r.Max }

func (r ValueRange) Contains(value float64) bool {
	return r.Valid() && finite(value) && value >= r.Min && value <= r.Max
}

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

// AnalysisRequest is the only request an Analyzer receives. Snapshot is a
// read-only value by contract; Engine also deep-copies maps/slices before the
// call to prevent a malicious analyzer from mutating caller-owned state.
type AnalysisRequest struct {
	Snapshot     InputSnapshot
	Budget       ReflectionBudget
	OutputSchema json.RawMessage
}

// AnalysisResponse may carry typed output from a local analyzer or raw strict
// JSON from a model adapter. Engine accepts either representation, preferring
// Proposal when Raw is empty.
type AnalysisResponse struct {
	Proposal ReflectionProposal
	Raw      json.RawMessage
	Usage    Usage
}

// Analyzer is the provider-neutral reflection analysis port. It cannot
// execute tools; all external content is already present in the typed
// read-only snapshot.
type Analyzer interface {
	Analyze(context.Context, AnalysisRequest) (AnalysisResponse, error)
}

// AnalyzerFunc adapts a function to Analyzer.
type AnalyzerFunc func(context.Context, AnalysisRequest) (AnalysisResponse, error)

func (f AnalyzerFunc) Analyze(ctx context.Context, request AnalysisRequest) (AnalysisResponse, error) {
	if f == nil {
		return AnalysisResponse{}, ErrNoAnalyzer
	}
	return f(ctx, request)
}

// ProposalAnalyzer is a convenient port for adapters that already produce a
// typed proposal and do not need to inspect the output schema/budget.
type ProposalAnalyzer interface {
	AnalyzeProposal(context.Context, InputSnapshot) (ReflectionProposal, error)
}

// ProposalAnalyzerFunc adapts a typed proposal function to Analyzer.
type ProposalAnalyzerFunc func(context.Context, InputSnapshot) (ReflectionProposal, error)

func (f ProposalAnalyzerFunc) AnalyzeProposal(ctx context.Context, snapshot InputSnapshot) (ReflectionProposal, error) {
	if f == nil {
		return ReflectionProposal{}, ErrNoAnalyzer
	}
	return f(ctx, snapshot)
}

// ProposalAnalyzerAdapter bridges the compact ProposalAnalyzer port to the
// budget-aware Analyzer interface.
type ProposalAnalyzerAdapter struct{ Source ProposalAnalyzer }

func (a ProposalAnalyzerAdapter) Analyze(ctx context.Context, request AnalysisRequest) (AnalysisResponse, error) {
	if a.Source == nil {
		return AnalysisResponse{}, ErrNoAnalyzer
	}
	proposal, err := a.Source.AnalyzeProposal(ctx, request.Snapshot)
	return AnalysisResponse{Proposal: proposal}, err
}

// ModelRequest is provider-neutral and intentionally does not contain tools,
// capabilities, credentials, or side-effect intents. A provider adapter may
// render Snapshot into its own prompt while preserving provenance envelopes.
type ModelRequest struct {
	Snapshot     InputSnapshot
	Budget       ReflectionBudget
	OutputSchema json.RawMessage
}

// ModelResponse is the bounded structured output of a model adapter.
type ModelResponse struct {
	JSON  json.RawMessage
	Usage Usage
}

// Model is the provider-neutral model port used by ModelAnalyzer.
type Model interface {
	Complete(context.Context, ModelRequest) (ModelResponse, error)
}

// ModelBackend is an architectural alias for callers that use backend
// terminology elsewhere in the application.
type ModelBackend = Model

// ModelFunc adapts a function to Model.
type ModelFunc func(context.Context, ModelRequest) (ModelResponse, error)

func (f ModelFunc) Complete(ctx context.Context, request ModelRequest) (ModelResponse, error) {
	if f == nil {
		return ModelResponse{}, ErrNoModel
	}
	return f(ctx, request)
}

// ModelAnalyzer decodes a model's strict JSON output and exposes it through
// the reflection Analyzer port.
type ModelAnalyzer struct{ Backend Model }

func NewModelAnalyzer(model Model) (*ModelAnalyzer, error) {
	if model == nil {
		return nil, ErrNoModel
	}
	return &ModelAnalyzer{Backend: model}, nil
}

func (a *ModelAnalyzer) Analyze(ctx context.Context, request AnalysisRequest) (AnalysisResponse, error) {
	if a == nil || a.Backend == nil {
		return AnalysisResponse{}, ErrNoModel
	}
	if len(request.OutputSchema) == 0 {
		request.OutputSchema = ProposalSchema()
	}
	response, err := a.Backend.Complete(ctx, ModelRequest{
		Snapshot: cloneSnapshot(request.Snapshot), Budget: request.Budget,
		OutputSchema: append(json.RawMessage(nil), request.OutputSchema...),
	})
	if err != nil {
		return AnalysisResponse{}, err
	}
	if request.Budget.MaxOutputBytes > 0 && len(response.JSON) > request.Budget.MaxOutputBytes {
		return AnalysisResponse{}, fmt.Errorf("%w: model output size %d exceeds %d bytes", ErrBudgetExceeded, len(response.JSON), request.Budget.MaxOutputBytes)
	}
	proposal, err := DecodeProposal(response.JSON)
	if err != nil {
		return AnalysisResponse{}, err
	}
	return AnalysisResponse{Proposal: proposal, Raw: append(json.RawMessage(nil), response.JSON...), Usage: response.Usage}, nil
}

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
	AffectDecay            DecayPolicy
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

// Clone helpers below are intentionally internal to prevent analyzer code
// from mutating snapshots or state held by the caller.
func cloneSnapshot(input InputSnapshot) InputSnapshot {
	output := input
	output.ImmutablePolicy = string([]byte(input.ImmutablePolicy))
	output.IdentitySeed = string([]byte(input.IdentitySeed))
	output.State = cloneState(input.State)
	output.Evidence = append([]Evidence(nil), input.Evidence...)
	for index := range output.Evidence {
		output.Evidence[index].Content = string([]byte(output.Evidence[index].Content))
		output.Evidence[index].Text = string([]byte(output.Evidence[index].Text))
		output.Evidence[index].Excerpt = string([]byte(output.Evidence[index].Excerpt))
	}
	return output
}

func cloneState(input ReflectionState) ReflectionState {
	output := input
	output.Persona = clonePersona(input.Persona)
	output.Relationship = cloneRelationship(input.Relationship)
	output.Affect = cloneAffect(input.Affect)
	return output
}

func clonePersona(input MutablePersona) MutablePersona {
	output := input
	output.Traits = cloneFloatMap(input.Traits)
	output.PinnedTraits = append([]string(nil), input.PinnedTraits...)
	return output
}

func cloneRelationship(input RelationshipState) RelationshipState {
	output := input
	output.Dimensions = cloneFloatMap(input.Dimensions)
	output.Evidence = append([]domain.ID(nil), input.Evidence...)
	if input.Opinions != nil {
		output.Opinions = make([]SubjectiveOpinion, len(input.Opinions))
		for index, opinion := range input.Opinions {
			output.Opinions[index] = cloneOpinion(opinion)
		}
	}
	return output
}

func cloneOpinion(input SubjectiveOpinion) SubjectiveOpinion {
	output := input
	output.EvidenceIDs = append([]domain.ID(nil), input.EvidenceIDs...)
	output.Evidence = append([]domain.ID(nil), input.Evidence...)
	return output
}

func cloneAffect(input AffectiveState) AffectiveState {
	output := input
	output.Dimensions = cloneFloatMap(input.Dimensions)
	if input.DimensionUpdated != nil {
		output.DimensionUpdated = make(map[string]time.Time, len(input.DimensionUpdated))
		for key, value := range input.DimensionUpdated {
			output.DimensionUpdated[key] = value
		}
	}
	return output
}

func cloneFloatMap(input map[string]float64) map[string]float64 {
	if input == nil {
		return nil
	}
	output := make(map[string]float64, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

var namePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)

func validateName(value string) error {
	value = strings.TrimSpace(value)
	if !namePattern.MatchString(value) {
		return fmt.Errorf("name must match %s", namePattern.String())
	}
	return nil
}

func validateScalarMap(values map[string]float64, label string) error {
	if len(values) > 128 {
		return fmt.Errorf("%w: %s contains too many dimensions", ErrInvalidSnapshot, label)
	}
	for name, value := range values {
		if err := validateName(name); err != nil {
			return fmt.Errorf("%w: invalid %s key %q: %v", ErrInvalidSnapshot, label, name, err)
		}
		if !finite(value) {
			return fmt.Errorf("%w: %s value %q is not finite", ErrInvalidSnapshot, label, name)
		}
	}
	return nil
}

func validateIDSlice(ids []domain.ID, label string, sentinel error) error {
	seen := make(map[domain.ID]struct{}, len(ids))
	for _, id := range ids {
		if id.Empty() {
			return fmt.Errorf("%w: %s contains an empty id", sentinel, label)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%w: %s contains duplicate id %s", sentinel, label, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validatePromptText(value string, maxBytes int, sentinel error) error {
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%w: prompt contains NUL", sentinel)
	}
	if maxBytes > 0 && len([]byte(value)) > maxBytes {
		return fmt.Errorf("%w: prompt exceeds %d bytes", sentinel, maxBytes)
	}
	return nil
}

// sortedKeys makes deterministic guard/error ordering possible for map-based
// deltas and is also useful to adapters that want stable audit output.
func sortedKeys(values map[string]float64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ContextError converts a nil/cancelled context to a stable package error
// while preserving context.Canceled and context.DeadlineExceeded for callers.
func ContextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalidSnapshot)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
