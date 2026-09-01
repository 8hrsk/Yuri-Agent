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
	ID             string                  `json:"id"`
	Kind           config.ProviderKind     `json:"kind"`
	DisplayName    string                  `json:"displayName"`
	BaseURL        string                  `json:"baseUrl,omitempty"`
	Model          string                  `json:"model,omitempty"`
	APIStyle       config.ProviderAPIStyle `json:"apiStyle,omitempty"`
	FavoriteModels []string                `json:"favoriteModels,omitempty"`
	Binary         string                  `json:"binary,omitempty"`
	Enabled        bool                    `json:"enabled"`
	HasSecret      bool                    `json:"hasSecret"`
}

type SaveOpenAIProviderInput struct {
	ID          string                  `json:"id"`
	DisplayName string                  `json:"displayName"`
	BaseURL     string                  `json:"baseUrl"`
	Model       string                  `json:"model"`
	APIStyle    config.ProviderAPIStyle `json:"apiStyle,omitempty"`
	APIKey      string                  `json:"apiKey"`
	Enabled     bool                    `json:"enabled"`
}

// SaveOpenAIProviderCredentialInput stores a provider token before a model is
// selected. A new provider remains disabled, so the currently active provider
// keeps serving chat until the owner explicitly selects and saves a model.
type SaveOpenAIProviderCredentialInput struct {
	ID          string                  `json:"id"`
	DisplayName string                  `json:"displayName"`
	BaseURL     string                  `json:"baseUrl"`
	APIStyle    config.ProviderAPIStyle `json:"apiStyle,omitempty"`
	APIKey      string                  `json:"apiKey"`
}

type OpenAIModelCatalogInput struct {
	ProviderID string `json:"providerId"`
	Sort       string `json:"sort,omitempty"`
}

type OpenAIModelView struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Description         string   `json:"description,omitempty"`
	ContextLength       int      `json:"contextLength,omitempty"`
	MaxCompletionTokens int      `json:"maxCompletionTokens,omitempty"`
	PromptPrice         string   `json:"promptPrice,omitempty"`
	CompletionPrice     string   `json:"completionPrice,omitempty"`
	RequestPrice        string   `json:"requestPrice,omitempty"`
	Free                bool     `json:"free"`
	SupportsTools       bool     `json:"supportsTools"`
	InputModalities     []string `json:"inputModalities,omitempty"`
	OutputModalities    []string `json:"outputModalities,omitempty"`
	Created             int64    `json:"created,omitempty"`
	Favorite            bool     `json:"favorite"`
}

type SetProviderModelFavoriteInput struct {
	ProviderID string `json:"providerId"`
	Model      string `json:"model"`
	Favorite   bool   `json:"favorite"`
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
	ProviderID       string                  `json:"providerId,omitempty"`
	Kind             config.ProviderKind     `json:"kind"`
	BaseURL          string                  `json:"baseUrl"`
	Model            string                  `json:"model"`
	APIStyle         config.ProviderAPIStyle `json:"apiStyle,omitempty"`
	TimeoutSeconds   int                     `json:"timeoutSeconds"`
	StreamResponses  bool                    `json:"streamResponses"`
	APIKeyConfigured bool                    `json:"apiKeyConfigured"`
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
