package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	AgentNameMaxRunes        = 64
	AgentGenderMaxRunes      = 64
	AgentPreferencesMaxRunes = 2000
	AgentBackstoryMaxRunes   = 12000
	AgentProviderIDMaxRunes  = 128
	AgentModelMaxRunes       = 256
	AgentMinimumAge          = 1
	AgentMaximumAge          = 200
)

// ExecutionBudgetPreset is an owner-controlled resource profile. Concrete
// limits are resolved at run start from this preset, the workload kind, and
// secret-free model metadata; the profile never stores provider credentials.
type ExecutionBudgetPreset string

const (
	ExecutionBudgetEfficient ExecutionBudgetPreset = "efficient"
	ExecutionBudgetBalanced  ExecutionBudgetPreset = "balanced"
	ExecutionBudgetExtended  ExecutionBudgetPreset = "extended"
)

func (preset ExecutionBudgetPreset) Valid() bool {
	switch preset {
	case "", ExecutionBudgetEfficient, ExecutionBudgetBalanced, ExecutionBudgetExtended:
		return true
	default:
		return false
	}
}

func (preset ExecutionBudgetPreset) Normalized() ExecutionBudgetPreset {
	if preset == "" {
		return ExecutionBudgetBalanced
	}
	return preset
}

// AgentProfile is an owner-created, durable top-level personality. It is
// deliberately separate from MutablePersona: the owner controls identity
// fields, while reflection may only evolve the bounded mutable persona that
// shares this profile ID.
type AgentProfile struct {
	ID          ID     `json:"id"`
	Name        string `json:"name"`
	Age         int    `json:"age"`
	Gender      string `json:"gender"`
	Preferences string `json:"preferences,omitempty"`
	Backstory   string `json:"backstory,omitempty"`
	ProviderID  string `json:"provider_id,omitempty"`
	Model       string `json:"model,omitempty"`
	// FallbackEnabled is an explicit owner-controlled opt-in. A configured
	// fallback is never selected implicitly; the orchestration layer must make
	// a visible, audited switch before using it.
	FallbackEnabled    bool                  `json:"fallback_enabled,omitempty"`
	FallbackProviderID string                `json:"fallback_provider_id,omitempty"`
	FallbackModel      string                `json:"fallback_model,omitempty"`
	ExecutionBudget    ExecutionBudgetPreset `json:"execution_budget,omitempty"`
	CreatedAt          time.Time             `json:"created_at"`
	UpdatedAt          time.Time             `json:"updated_at"`
	DeletedAt          *time.Time            `json:"deleted_at,omitempty"`
}

func NewAgentProfile(id ID, name string, age int, gender, preferences string, now time.Time) (AgentProfile, error) {
	return NewAgentProfileWithBackstory(id, name, age, gender, preferences, "", now)
}

// NewAgentProfileWithBackstory creates an owner-controlled agent identity with
// an optional fictional autobiographical seed. The old constructor remains a
// compatibility wrapper for callers that do not provide a backstory.
func NewAgentProfileWithBackstory(id ID, name string, age int, gender, preferences, backstory string, now time.Time) (AgentProfile, error) {
	profile := AgentProfile{
		ID: id, Name: strings.TrimSpace(name), Age: age, Gender: strings.TrimSpace(gender),
		Preferences: strings.TrimSpace(preferences), Backstory: strings.TrimSpace(backstory),
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if err := profile.Validate(); err != nil {
		return AgentProfile{}, err
	}
	return profile, nil
}

func (p AgentProfile) Validate() error {
	if p.ID.Empty() {
		return fmt.Errorf("%w: agent profile id is required", ErrInvalidArgument)
	}
	name := strings.TrimSpace(p.Name)
	if name == "" || utf8.RuneCountInString(name) > AgentNameMaxRunes || strings.ContainsRune(name, '\x00') {
		return fmt.Errorf("%w: agent name must contain 1..%d characters", ErrInvalidArgument, AgentNameMaxRunes)
	}
	// Zero means the owner chose not to specify a fictional age.
	if p.Age != 0 && (p.Age < AgentMinimumAge || p.Age > AgentMaximumAge) {
		return fmt.Errorf("%w: agent age must be between %d and %d", ErrInvalidArgument, AgentMinimumAge, AgentMaximumAge)
	}
	gender := strings.TrimSpace(p.Gender)
	if gender == "" || utf8.RuneCountInString(gender) > AgentGenderMaxRunes || strings.ContainsRune(gender, '\x00') {
		return fmt.Errorf("%w: agent gender must contain 1..%d characters", ErrInvalidArgument, AgentGenderMaxRunes)
	}
	preferences := strings.TrimSpace(p.Preferences)
	if utf8.RuneCountInString(preferences) > AgentPreferencesMaxRunes || strings.ContainsRune(preferences, '\x00') {
		return fmt.Errorf("%w: agent preferences exceed %d characters", ErrInvalidArgument, AgentPreferencesMaxRunes)
	}
	backstory := strings.TrimSpace(p.Backstory)
	if utf8.RuneCountInString(backstory) > AgentBackstoryMaxRunes || strings.ContainsRune(backstory, '\x00') {
		return fmt.Errorf("%w: agent backstory exceeds %d characters", ErrInvalidArgument, AgentBackstoryMaxRunes)
	}
	providerID := strings.TrimSpace(p.ProviderID)
	model := strings.TrimSpace(p.Model)
	if utf8.RuneCountInString(providerID) > AgentProviderIDMaxRunes || strings.ContainsRune(providerID, '\x00') {
		return fmt.Errorf("%w: agent provider id exceeds %d characters", ErrInvalidArgument, AgentProviderIDMaxRunes)
	}
	if utf8.RuneCountInString(model) > AgentModelMaxRunes || strings.ContainsRune(model, '\x00') {
		return fmt.Errorf("%w: agent model exceeds %d characters", ErrInvalidArgument, AgentModelMaxRunes)
	}
	if providerID == "" && model != "" {
		return fmt.Errorf("%w: agent model requires an explicit provider", ErrInvalidArgument)
	}
	fallbackProviderID := strings.TrimSpace(p.FallbackProviderID)
	fallbackModel := strings.TrimSpace(p.FallbackModel)
	if (fallbackProviderID == "") != (fallbackModel == "") {
		return fmt.Errorf("%w: fallback provider and model must be configured together", ErrInvalidArgument)
	}
	if fallbackProviderID != "" && (utf8.RuneCountInString(fallbackProviderID) > AgentProviderIDMaxRunes || strings.ContainsRune(fallbackProviderID, '\x00')) {
		return fmt.Errorf("%w: fallback provider id exceeds %d characters", ErrInvalidArgument, AgentProviderIDMaxRunes)
	}
	if fallbackModel != "" && (utf8.RuneCountInString(fallbackModel) > AgentModelMaxRunes || strings.ContainsRune(fallbackModel, '\x00')) {
		return fmt.Errorf("%w: fallback model exceeds %d characters", ErrInvalidArgument, AgentModelMaxRunes)
	}
	if p.FallbackEnabled && fallbackProviderID == "" {
		return fmt.Errorf("%w: enabled fallback requires a provider and model", ErrInvalidArgument)
	}
	if !p.ExecutionBudget.Valid() {
		return fmt.Errorf("%w: invalid execution budget preset %q", ErrInvalidArgument, p.ExecutionBudget)
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() || p.UpdatedAt.Before(p.CreatedAt) {
		return fmt.Errorf("%w: invalid agent profile timestamps", ErrInvalidArgument)
	}
	if p.DeletedAt != nil && (p.DeletedAt.IsZero() || p.DeletedAt.Before(p.CreatedAt)) {
		return fmt.Errorf("%w: invalid agent deletion timestamp", ErrInvalidArgument)
	}
	return nil
}

func (p AgentProfile) Deleted() bool { return p.DeletedAt != nil }

// FallbackRoute returns the configured fallback only when the owner has
// explicitly enabled it. Returning a separate boolean keeps the disabled
// state distinct from a malformed route and prevents callers from silently
// treating a configured-but-disabled fallback as active.
func (p AgentProfile) FallbackRoute() (RunInferenceRoute, bool, error) {
	if !p.FallbackEnabled {
		return RunInferenceRoute{}, false, nil
	}
	route := RunInferenceRoute{ProviderID: strings.TrimSpace(p.FallbackProviderID), Model: strings.TrimSpace(p.FallbackModel)}
	if route.ProviderID == "" || route.Model == "" || !route.Valid() {
		return RunInferenceRoute{}, false, fmt.Errorf("%w: enabled fallback route is incomplete", ErrInvalidArgument)
	}
	return route, true, nil
}
