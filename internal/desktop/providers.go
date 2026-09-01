package desktop

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/providers/antigravity"
)

// GetOnboardingState returns durable first-run state without consulting or
// exposing any provider credential.
func (b *Bridge) GetOnboardingState() OnboardingView {
	b.mu.RLock()
	completed := b.config.Onboarding.Completed
	providerTested := b.config.Onboarding.ProviderTested
	agentConfigured := b.config.Onboarding.AgentConfigured
	activeAgentID := b.config.Persona.ProfileID
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
	if completed && providerTested && agentConfigured {
		state = OnboardingStateComplete
	}
	completed = completed && providerTested && agentConfigured
	return OnboardingView{State: state, Completed: completed, ProviderTested: providerTested, ProviderConfigured: configured, AgentConfigured: agentConfigured, ActiveAgentID: activeAgentID}
}

func (b *Bridge) ListProviders() []ProviderView {
	b.mu.RLock()
	providers := append([]config.ProviderConfig(nil), b.config.Providers...)
	b.mu.RUnlock()
	views := make([]ProviderView, 0, len(providers))
	for _, provider := range providers {
		views = append(views, providerView(provider))
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
	ctx, cancel := b.context()
	defer cancel()
	b.mu.Lock()
	defer b.mu.Unlock()
	existing, _ := configuredProvider(b.config.Providers, input.ID)
	style := normalizedOpenAIAPIStyle(input.APIStyle, input.BaseURL)
	if input.APIStyle == "" && existing.APIStyle != "" {
		style = existing.APIStyle
	}
	provider := config.ProviderConfig{
		ID: input.ID, Kind: config.ProviderOpenAICompatible, DisplayName: strings.TrimSpace(input.DisplayName),
		BaseURL: strings.TrimSpace(input.BaseURL), Model: strings.TrimSpace(input.Model), APIStyle: style,
		FavoriteModels: append([]string(nil), existing.FavoriteModels...),
		CredentialRef:  "provider." + input.ID + ".api-key", Enabled: input.Enabled,
	}
	return b.saveOpenAIProviderLocked(ctx, provider, input.APIKey)
}

// SaveOpenAIProviderCredential persists the token and endpoint metadata before
// model selection. It never activates a new provider or disables the current
// one. Existing activation/model/favorites are preserved.
func (b *Bridge) SaveOpenAIProviderCredential(input SaveOpenAIProviderCredentialInput) (ProviderView, error) {
	input.ID = strings.TrimSpace(input.ID)
	if input.ID == "" {
		input.ID = "openai"
	}
	if strings.TrimSpace(input.DisplayName) == "" {
		input.DisplayName = "OpenAI-compatible"
	}
	ctx, cancel := b.context()
	defer cancel()
	b.mu.Lock()
	defer b.mu.Unlock()
	existing, found := configuredProvider(b.config.Providers, input.ID)
	if found && existing.Kind != config.ProviderOpenAICompatible {
		return ProviderView{}, fmt.Errorf("provider %q is not OpenAI-compatible", input.ID)
	}
	style := normalizedOpenAIAPIStyle(input.APIStyle, input.BaseURL)
	if input.APIStyle == "" && existing.APIStyle != "" {
		style = existing.APIStyle
	}
	provider := config.ProviderConfig{
		ID: input.ID, Kind: config.ProviderOpenAICompatible, DisplayName: strings.TrimSpace(input.DisplayName),
		BaseURL: strings.TrimSpace(input.BaseURL), APIStyle: style,
		CredentialRef: "provider." + input.ID + ".api-key",
	}
	if found {
		provider.Model = existing.Model
		provider.Enabled = existing.Enabled
		provider.FavoriteModels = append([]string(nil), existing.FavoriteModels...)
	}
	return b.saveOpenAIProviderLocked(ctx, provider, input.APIKey)
}

func (b *Bridge) saveOpenAIProviderLocked(ctx context.Context, provider config.ProviderConfig, apiKey string) (ProviderView, error) {
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
	oldSecret, oldError := b.keyring.Get(ctx, provider.CredentialRef)
	if apiKey == "" {
		if oldError != nil {
			return ProviderView{}, errors.New("API key is required for a new provider")
		}
	} else if err := b.keyring.Put(ctx, provider.CredentialRef, apiKey); err != nil {
		return ProviderView{}, err
	}
	if err := config.Save(b.paths, candidate); err != nil {
		if apiKey != "" {
			if oldError == nil {
				_ = b.keyring.Put(context.Background(), provider.CredentialRef, oldSecret)
			} else {
				_ = b.keyring.Delete(context.Background(), provider.CredentialRef)
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
	// Invalidate any launch already in flight so it cannot publish a client for
	// the previous binary over the configuration just saved.
	b.codexGeneration++
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
			Model: settings.Model, APIStyle: settings.APIStyle, APIKey: input.APIKey, Enabled: true,
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
	case config.ProviderAntigravity:
		status := antigravity.Status()
		return OnboardingResult{
			Message: status.Message, ErrorCode: status.ErrorCode,
			Alternative: status.Alternative, State: b.GetOnboardingState(),
		}
	default:
		return OnboardingResult{Message: fmt.Sprintf("unsupported provider kind %q", settings.Kind), State: b.GetOnboardingState()}
	}

	probe := b.ProbeProvider(settings)
	return OnboardingResult{
		OK: probe.OK, Message: probe.Message, ErrorCode: probe.ErrorCode,
		Alternative: probe.Alternative, State: probe.Onboarding,
	}
}
