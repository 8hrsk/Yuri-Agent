package desktop

import (
	"github.com/OrdoAI/yuri-agent/internal/config"
)

// OnboardingState is the durable first-run lifecycle exposed to the UI. The
// bridge only transitions to complete after a provider probe succeeds; there
// is intentionally no generic setter for this state.
type OnboardingState string

const (
	OnboardingStatePending  OnboardingState = "pending"
	OnboardingStateComplete OnboardingState = "complete"
)

// OnboardingView is a read-only, secret-free snapshot of first-run state.
// ProviderConfigured describes saved metadata, not provider health.
type OnboardingView struct {
	State              OnboardingState `json:"state"`
	Completed          bool            `json:"completed"`
	ProviderTested     bool            `json:"providerTested"`
	ProviderConfigured bool            `json:"providerConfigured"`
	AgentConfigured    bool            `json:"agentConfigured"`
	ActiveAgentID      string          `json:"activeAgentId,omitempty"`
}

type ProviderView struct {
	ID          string              `json:"id"`
	Kind        config.ProviderKind `json:"kind"`
	DisplayName string              `json:"displayName"`
	BaseURL     string              `json:"baseUrl,omitempty"`
	Model       string              `json:"model,omitempty"`
	Binary      string              `json:"binary,omitempty"`
	Enabled     bool                `json:"enabled"`
	HasSecret   bool                `json:"hasSecret"`
}

type SaveOpenAIProviderInput struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	BaseURL     string `json:"baseUrl"`
	Model       string `json:"model"`
	APIKey      string `json:"apiKey"`
	Enabled     bool   `json:"enabled"`
}

type SaveCodexProviderInput struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Model       string `json:"model"`
	Binary      string `json:"binary"`
	Enabled     bool   `json:"enabled"`
}

type LoginView struct {
	Type            string `json:"type"`
	LoginID         string `json:"loginId,omitempty"`
	AuthURL         string `json:"authUrl,omitempty"`
	VerificationURL string `json:"verificationUrl,omitempty"`
	UserCode        string `json:"userCode,omitempty"`
}

type CodexLogoutView struct {
	Disconnected bool           `json:"disconnected"`
	Onboarding   OnboardingView `json:"onboarding"`
}

type CodexModelView struct {
	ID                     string   `json:"id"`
	Model                  string   `json:"model"`
	DisplayName            string   `json:"displayName"`
	Description            string   `json:"description,omitempty"`
	IsDefault              bool     `json:"isDefault"`
	DefaultReasoningEffort string   `json:"defaultReasoningEffort,omitempty"`
	InputModalities        []string `json:"inputModalities,omitempty"`
}

type ProviderSettingsInput struct {
	ProviderID       string              `json:"providerId,omitempty"`
	Kind             config.ProviderKind `json:"kind"`
	BaseURL          string              `json:"baseUrl"`
	Model            string              `json:"model"`
	TimeoutSeconds   int                 `json:"timeoutSeconds"`
	StreamResponses  bool                `json:"streamResponses"`
	APIKeyConfigured bool                `json:"apiKeyConfigured"`
}

type ProviderTestResult struct {
	OK          bool           `json:"ok"`
	Message     string         `json:"message"`
	ErrorCode   string         `json:"errorCode,omitempty"`
	Alternative string         `json:"alternative,omitempty"`
	ProviderID  string         `json:"providerId,omitempty"`
	Onboarding  OnboardingView `json:"onboarding"`
}

// ProviderProbeInput is the typed bridge contract for a provider probe. It is
// an alias so existing TestProvider callers keep the same wire shape.
type ProviderProbeInput = ProviderSettingsInput

// ProviderProbeResult is the typed bridge result for a provider probe.
type ProviderProbeResult = ProviderTestResult

// CompleteOnboardingInput combines the transient provider form with the
// secret needed to save a new OpenAI-compatible credential. APIKey is used
// only during this call and is never copied into config, SQLite, audit, or the
// returned result.
type CompleteOnboardingInput struct {
	Settings ProviderSettingsInput `json:"settings"`
	APIKey   string                `json:"apiKey,omitempty"`
}

// OnboardingResult reports the result of the save-and-probe operation and the
// resulting durable state. It intentionally contains no provider payload.
type OnboardingResult struct {
	OK          bool           `json:"ok"`
	Message     string         `json:"message"`
	ErrorCode   string         `json:"errorCode,omitempty"`
	Alternative string         `json:"alternative,omitempty"`
	State       OnboardingView `json:"state"`
}
