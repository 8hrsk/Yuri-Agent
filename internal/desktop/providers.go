package desktop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/providers/codexapp"
	openaiadapter "github.com/OrdoAI/yuri-agent/internal/providers/openai"
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
	OK         bool           `json:"ok"`
	Message    string         `json:"message"`
	ProviderID string         `json:"providerId,omitempty"`
	Onboarding OnboardingView `json:"onboarding"`
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
	OK      bool           `json:"ok"`
	Message string         `json:"message"`
	State   OnboardingView `json:"state"`
}

// GetOnboardingState returns durable first-run state without consulting or
// exposing any provider credential.
func (b *Bridge) GetOnboardingState() OnboardingView {
	b.mu.RLock()
	completed := b.config.Onboarding.Completed
	providerTested := b.config.Onboarding.ProviderTested
	providers := append([]config.ProviderConfig(nil), b.config.Providers...)
	b.mu.RUnlock()

	configured := false
	for _, provider := range providers {
		if !provider.Enabled {
			continue
		}
		switch provider.Kind {
		case config.ProviderOpenAICompatible:
			configured = provider.Model != "" && provider.CredentialRef != ""
		case config.ProviderCodexAppServer:
			configured = true
		}
		if configured {
			break
		}
	}
	state := OnboardingStatePending
	if completed && providerTested {
		state = OnboardingStateComplete
	}
	completed = completed && providerTested
	return OnboardingView{State: state, Completed: completed, ProviderTested: providerTested, ProviderConfigured: configured}
}

func (b *Bridge) ListProviders() []ProviderView {
	b.mu.RLock()
	providers := append([]config.ProviderConfig(nil), b.config.Providers...)
	b.mu.RUnlock()
	views := make([]ProviderView, 0, len(providers))
	for _, provider := range providers {
		views = append(views, ProviderView{
			ID: provider.ID, Kind: provider.Kind, DisplayName: provider.DisplayName,
			BaseURL: provider.BaseURL, Model: provider.Model, Binary: provider.Binary,
			Enabled: provider.Enabled, HasSecret: provider.CredentialRef != "",
		})
	}
	return views
}

func (b *Bridge) SaveOpenAIProvider(input SaveOpenAIProviderInput) (ProviderView, error) {
	input.ID = strings.TrimSpace(input.ID)
	if input.ID == "" {
		input.ID = "openai"
	}
	if strings.TrimSpace(input.DisplayName) == "" {
		input.DisplayName = "OpenAI-compatible"
	}
	reference := "provider." + input.ID + ".api-key"
	provider := config.ProviderConfig{
		ID: input.ID, Kind: config.ProviderOpenAICompatible, DisplayName: strings.TrimSpace(input.DisplayName),
		BaseURL: strings.TrimSpace(input.BaseURL), Model: strings.TrimSpace(input.Model),
		CredentialRef: reference, Enabled: input.Enabled,
	}
	ctx, cancel := b.context()
	defer cancel()
	b.mu.Lock()
	defer b.mu.Unlock()
	candidate := b.config
	if provider.Enabled {
		candidate.Providers = disableProviders(candidate.Providers)
	}
	candidate.Providers = upsertProvider(candidate.Providers, provider)
	if err := candidate.Validate(); err != nil {
		return ProviderView{}, err
	}
	if b.keyring == nil {
		return ProviderView{}, errors.New("system keyring is unavailable")
	}
	oldSecret, oldError := b.keyring.Get(ctx, reference)
	if input.APIKey == "" {
		if oldError != nil {
			return ProviderView{}, errors.New("API key is required for a new provider")
		}
	} else if err := b.keyring.Put(ctx, reference, input.APIKey); err != nil {
		return ProviderView{}, err
	}
	if err := config.Save(b.paths, candidate); err != nil {
		if input.APIKey != "" {
			if oldError == nil {
				_ = b.keyring.Put(context.Background(), reference, oldSecret)
			} else {
				_ = b.keyring.Delete(context.Background(), reference)
			}
		}
		return ProviderView{}, err
	}
	b.config = candidate
	return providerView(provider), nil
}

func (b *Bridge) SaveCodexProvider(input SaveCodexProviderInput) (ProviderView, error) {
	if strings.TrimSpace(input.ID) == "" {
		input.ID = "codex"
	}
	if strings.TrimSpace(input.DisplayName) == "" {
		input.DisplayName = "Codex App Server"
	}
	provider := config.ProviderConfig{
		ID: strings.TrimSpace(input.ID), Kind: config.ProviderCodexAppServer,
		DisplayName: strings.TrimSpace(input.DisplayName), Model: strings.TrimSpace(input.Model),
		Binary: strings.TrimSpace(input.Binary), Enabled: input.Enabled,
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	candidate := b.config
	if provider.Enabled {
		candidate.Providers = disableProviders(candidate.Providers)
	}
	candidate.Providers = upsertProvider(candidate.Providers, provider)
	if err := candidate.Validate(); err != nil {
		return ProviderView{}, err
	}
	if err := config.Save(b.paths, candidate); err != nil {
		return ProviderView{}, err
	}
	oldClient := b.codex
	b.codex = nil
	b.config = candidate
	if oldClient != nil {
		_ = oldClient.Close()
	}
	return providerView(provider), nil
}

// TestProvider performs a minimal provider-owned request. It never returns
// credentials or raw provider payloads to the UI.
func (b *Bridge) TestProvider(input ProviderSettingsInput) ProviderTestResult {
	return b.probeProvider(input)
}

// ProbeProvider is the explicit typed provider-probe bridge method used by
// first-run onboarding. A successful probe durably completes onboarding; a
// save without a successful probe leaves it pending.
func (b *Bridge) ProbeProvider(input ProviderProbeInput) ProviderProbeResult {
	return b.probeProvider(input)
}

// CompleteOnboarding saves the submitted provider and immediately probes it.
// It cannot be used as a generic state setter: onboarding becomes complete
// only through the successful probe performed here (or by TestProvider/
// ProbeProvider). A failed save or probe leaves the durable state pending.
func (b *Bridge) CompleteOnboarding(input CompleteOnboardingInput) OnboardingResult {
	settings := input.Settings
	if settings.Kind == "" {
		settings.Kind = config.ProviderOpenAICompatible
	}

	switch settings.Kind {
	case config.ProviderOpenAICompatible:
		providerID := strings.TrimSpace(settings.ProviderID)
		if providerID == "" {
			providerID = "openai"
		}
		if _, err := b.SaveOpenAIProvider(SaveOpenAIProviderInput{
			ID: providerID, DisplayName: "OpenAI-compatible", BaseURL: settings.BaseURL,
			Model: settings.Model, APIKey: input.APIKey, Enabled: true,
		}); err != nil {
			return OnboardingResult{Message: safeError(err.Error()), State: b.GetOnboardingState()}
		}
		settings.ProviderID = providerID
	case config.ProviderCodexAppServer:
		if _, err := b.SaveCodexProvider(SaveCodexProviderInput{
			ID: settings.ProviderID, DisplayName: "Codex App Server", Model: settings.Model,
			Binary: "codex", Enabled: true,
		}); err != nil {
			return OnboardingResult{Message: safeError(err.Error()), State: b.GetOnboardingState()}
		}
	default:
		return OnboardingResult{Message: fmt.Sprintf("unsupported provider kind %q", settings.Kind), State: b.GetOnboardingState()}
	}

	probe := b.ProbeProvider(settings)
	return OnboardingResult{OK: probe.OK, Message: probe.Message, State: probe.Onboarding}
}

func (b *Bridge) probeProvider(input ProviderSettingsInput) ProviderTestResult {
	ctx, cancel := b.context()
	defer cancel()
	if input.Kind == config.ProviderCodexAppServer {
		account, err := b.CodexAccount()
		if err != nil {
			return b.providerProbeFailure(input.ProviderID, safeError(err.Error()))
		}
		if account.Account == nil {
			return b.providerProbeFailure(input.ProviderID, "Codex App Server отвечает, но ChatGPT OAuth ещё не завершён")
		}
		return b.providerProbeSuccess(ctx, input.ProviderID, "Codex App Server и ChatGPT OAuth доступны")
	}
	if input.Kind != "" && input.Kind != config.ProviderOpenAICompatible {
		return b.providerProbeFailure(input.ProviderID, fmt.Sprintf("unsupported provider kind %q", input.Kind))
	}

	b.mu.RLock()
	providers := append([]config.ProviderConfig(nil), b.config.Providers...)
	b.mu.RUnlock()
	var selected *config.ProviderConfig
	for index := range providers {
		if providers[index].Kind == config.ProviderOpenAICompatible && providers[index].Enabled &&
			(input.ProviderID == "" || providers[index].ID == strings.TrimSpace(input.ProviderID)) {
			selected = &providers[index]
			break
		}
	}
	if selected == nil {
		return b.providerProbeFailure(input.ProviderID, "Сначала сохраните OpenAI-compatible провайдер и API key")
	}
	providerID := selected.ID
	if b.keyring == nil {
		return b.providerProbeFailure(providerID, "API key недоступен в системном keyring")
	}
	secret, err := b.keyring.Get(ctx, selected.CredentialRef)
	if err != nil {
		return b.providerProbeFailure(providerID, "API key недоступен в системном keyring")
	}
	timeout := time.Duration(input.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client, err := openaiadapter.New(openaiadapter.Config{
		BaseURL: selected.BaseURL, APIKey: secret, Model: selected.Model,
		Style: openaiadapter.APIStyleResponses, Timeout: timeout, MaxAttempts: 1,
	})
	if err != nil {
		return b.providerProbeFailure(providerID, safeError(err.Error()))
	}
	stream, err := client.Start(ctx, agent.ModelRequest{
		Model: selected.Model,
		Messages: []agent.Message{
			{Role: agent.RoleSystem, Content: "Connection check. Return a very short acknowledgement."},
			{Role: agent.RoleUser, Content: "OK"},
		},
		MaxOutputTokens: 8,
	})
	if err != nil {
		return b.providerProbeFailure(providerID, safeError(err.Error()))
	}
	defer stream.Close()
	for {
		event, receiveErr := stream.Recv(ctx)
		if receiveErr != nil {
			if errors.Is(receiveErr, io.EOF) {
				return b.providerProbeSuccess(ctx, providerID, "Endpoint отвечает")
			}
			return b.providerProbeFailure(providerID, safeError(receiveErr.Error()))
		}
		if event.Type == agent.ModelEventTextDelta || event.Type == agent.ModelEventCompleted {
			return b.providerProbeSuccess(ctx, providerID, "Endpoint отвечает и поддерживает модель")
		}
	}
}

func (b *Bridge) providerProbeSuccess(ctx context.Context, providerID, message string) ProviderTestResult {
	if err := b.completeOnboarding(ctx); err != nil {
		return b.providerProbeFailure(providerID, "Проверка провайдера успешна, но состояние onboarding не удалось сохранить")
	}
	return ProviderTestResult{
		OK: true, Message: message, ProviderID: providerID, Onboarding: b.GetOnboardingState(),
	}
}

func (b *Bridge) providerProbeFailure(providerID, message string) ProviderTestResult {
	return ProviderTestResult{Message: message, ProviderID: strings.TrimSpace(providerID), Onboarding: b.GetOnboardingState()}
}

// completeOnboarding is intentionally private and has no caller other than a
// successful provider probe. The config write and in-memory transition happen
// under one bridge lock, so a restart observes either pending or complete.
func (b *Bridge) completeOnboarding(_ context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.config.Onboarding.Completed && b.config.Onboarding.ProviderTested {
		return nil
	}
	candidate := b.config
	candidate.Onboarding.Completed = true
	candidate.Onboarding.ProviderTested = true
	if err := config.Save(b.paths, candidate); err != nil {
		return err
	}
	b.config = candidate
	return nil
}

func (b *Bridge) CodexAccount() (codexapp.AccountReadResult, error) {
	ctx, cancel := b.context()
	defer cancel()
	client, err := b.ensureCodex(ctx)
	if err != nil {
		return codexapp.AccountReadResult{}, err
	}
	return client.ReadAccount(ctx, false)
}

func (b *Bridge) StartCodexLogin(mode string) (LoginView, error) {
	if err := b.ensureCodexProviderConfigured(); err != nil {
		return LoginView{}, err
	}
	ctx, cancel := b.context()
	defer cancel()
	client, err := b.ensureCodex(ctx)
	if err != nil {
		return LoginView{}, err
	}
	var result codexapp.LoginResult
	switch mode {
	case "", "browser":
		result, err = client.StartChatGPTLogin(ctx)
	case "device-code":
		result, err = client.StartDeviceCodeLogin(ctx)
	default:
		return LoginView{}, fmt.Errorf("unsupported Codex login mode %q", mode)
	}
	if err != nil {
		return LoginView{}, err
	}
	return LoginView{
		Type: result.Type, LoginID: result.LoginID, AuthURL: result.AuthURL,
		VerificationURL: result.VerificationURL, UserCode: result.UserCode,
	}, nil
}

func (b *Bridge) ensureCodexProviderConfigured() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, provider := range b.config.Providers {
		if provider.Kind == config.ProviderCodexAppServer && provider.Enabled {
			return nil
		}
	}
	provider := config.ProviderConfig{
		ID: "codex", Kind: config.ProviderCodexAppServer, DisplayName: "Codex App Server",
		Binary: codexapp.DefaultBinary, Enabled: true,
	}
	candidate := b.config
	candidate.Providers = upsertProvider(disableProviders(candidate.Providers), provider)
	if err := candidate.Validate(); err != nil {
		return err
	}
	if err := config.Save(b.paths, candidate); err != nil {
		return err
	}
	b.config = candidate
	return nil
}

func (b *Bridge) CodexRateLimits() (codexapp.RateLimitsResult, error) {
	ctx, cancel := b.context()
	defer cancel()
	client, err := b.ensureCodex(ctx)
	if err != nil {
		return codexapp.RateLimitsResult{}, err
	}
	return client.ReadRateLimits(ctx)
}

func (b *Bridge) CodexLogout() error {
	ctx, cancel := b.context()
	defer cancel()
	client, err := b.ensureCodex(ctx)
	if err != nil {
		return err
	}
	return client.Logout(ctx)
}

func (b *Bridge) ensureCodex(ctx context.Context) (*codexapp.Client, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.codex != nil {
		return b.codex, nil
	}
	var selected *config.ProviderConfig
	for index := range b.config.Providers {
		provider := &b.config.Providers[index]
		if provider.Kind == config.ProviderCodexAppServer && provider.Enabled {
			selected = provider
			break
		}
	}
	if selected == nil {
		return nil, errors.New("enabled Codex App Server provider is not configured")
	}
	client, err := codexapp.Start(ctx, codexapp.Options{
		Binary: selected.Binary, WorkingDirectory: b.paths.DataDirectory,
		ClientInfo: codexapp.ClientInfo{Name: "yuri", Title: "Yuri", Version: "0.1.0"},
	})
	if err != nil {
		return nil, err
	}
	b.codex = client
	return client, nil
}

func upsertProvider(providers []config.ProviderConfig, provider config.ProviderConfig) []config.ProviderConfig {
	result := append([]config.ProviderConfig(nil), providers...)
	for index := range result {
		if result[index].ID == provider.ID {
			result[index] = provider
			return result
		}
	}
	return append(result, provider)
}

func disableProviders(providers []config.ProviderConfig) []config.ProviderConfig {
	result := append([]config.ProviderConfig(nil), providers...)
	for index := range result {
		result[index].Enabled = false
	}
	return result
}

func providerView(provider config.ProviderConfig) ProviderView {
	return ProviderView{
		ID: provider.ID, Kind: provider.Kind, DisplayName: provider.DisplayName,
		BaseURL: provider.BaseURL, Model: provider.Model, Binary: provider.Binary,
		Enabled: provider.Enabled, HasSecret: provider.CredentialRef != "",
	}
}
