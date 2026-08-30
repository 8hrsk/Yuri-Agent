package desktop

import (
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/providers/codexapp"
)

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

func (b *Bridge) CodexModels() ([]CodexModelView, error) {
	ctx, cancel := b.context()
	defer cancel()
	client, err := b.ensureCodex(ctx)
	if err != nil {
		return nil, err
	}
	models, err := client.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]CodexModelView, 0, len(models))
	for _, model := range models {
		if model.Hidden || strings.TrimSpace(model.Model) == "" {
			continue
		}
		views = append(views, CodexModelView{
			ID: model.ID, Model: model.Model, DisplayName: model.DisplayName,
			Description: model.Description, IsDefault: model.IsDefault,
			DefaultReasoningEffort: model.DefaultReasoningEffort,
			InputModalities:        append([]string(nil), model.InputModalities...),
		})
	}
	return views, nil
}

func (b *Bridge) CodexLogout() (CodexLogoutView, error) {
	ctx, cancel := b.context()
	defer cancel()
	client, err := b.ensureCodex(ctx)
	if err != nil {
		return CodexLogoutView{}, err
	}
	// Persist the fail-closed state before the external logout. If the app
	// server request fails, a restart still cannot treat the old OAuth probe as
	// current; the owner can retry login/logout explicitly.
	if err := b.markProviderUntested(); err != nil {
		return CodexLogoutView{}, err
	}
	if err := client.Logout(ctx); err != nil {
		return CodexLogoutView{}, err
	}
	b.mu.Lock()
	if b.codex == client {
		b.codex = nil
	}
	b.codexGeneration++
	b.mu.Unlock()
	_ = client.Close()
	return CodexLogoutView{Disconnected: true, Onboarding: b.GetOnboardingState()}, nil
}

func (b *Bridge) markProviderUntested() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	candidate := b.config
	candidate.Onboarding.Completed = false
	candidate.Onboarding.ProviderTested = false
	if err := config.Save(b.paths, candidate); err != nil {
		return err
	}
	b.config = candidate
	return nil
}
