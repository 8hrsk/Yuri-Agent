package desktop

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/providers/antigravity"
	"github.com/OrdoAI/yuri-agent/internal/providers/codexapp"
	googleaistudio "github.com/OrdoAI/yuri-agent/internal/providers/googleaistudio"
	openaiadapter "github.com/OrdoAI/yuri-agent/internal/providers/openai"
	"github.com/OrdoAI/yuri-agent/internal/security"
)

func (b *Bridge) AllowedDirectories() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]string(nil), b.config.AllowedDirectories...)
}

func (b *Bridge) SaveAllowedDirectories(directories []string) error {
	cleaned := make([]string, 0, len(directories))
	for _, directory := range directories {
		if strings.TrimSpace(directory) != "" {
			cleaned = append(cleaned, strings.TrimSpace(directory))
		}
	}
	if len(cleaned) > 0 {
		allowlist, err := security.NewPathAllowlist(cleaned)
		if err != nil {
			return err
		}
		cleaned = allowlist.Roots()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	candidate := b.config
	candidate.AllowedDirectories = cleaned
	if err := candidate.Validate(); err != nil {
		return err
	}
	if err := config.Save(b.paths, candidate); err != nil {
		return err
	}
	b.config = candidate
	return nil
}

// addAllowedDirectory merges one approval scope while holding the config lock
// for the whole read-modify-write cycle. Two concurrent "allow always"
// decisions therefore cannot overwrite each other's grants.
func (b *Bridge) addAllowedDirectory(directory string) error {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return fmt.Errorf("filesystem permission directory is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	roots := appendUniqueRoot(b.config.AllowedDirectories, directory)
	allowlist, err := security.NewPathAllowlist(roots)
	if err != nil {
		return err
	}
	candidate := b.config
	candidate.AllowedDirectories = allowlist.Roots()
	if err := candidate.Validate(); err != nil {
		return err
	}
	if err := config.Save(b.paths, candidate); err != nil {
		return err
	}
	b.config = candidate
	return nil
}

func (b *Bridge) chatBackend(ctx context.Context) (agent.ModelBackend, string, error) {
	return b.chatBackendForRoute(ctx, "", "")
}

// chatBackendForAgent resolves a durable per-agent route. Legacy agents with
// no route continue to follow the one installation-wide enabled provider.
func (b *Bridge) chatBackendForAgent(ctx context.Context, agentID domain.ID) (agent.ModelBackend, string, error) {
	profile, err := b.repositories.Agents.Get(ctx, agentID)
	if err != nil {
		return nil, "", err
	}
	backend, model, err := b.chatBackendForRoute(ctx, profile.ProviderID, profile.Model)
	if err != nil {
		return nil, "", fmt.Errorf("model route for agent %s: %w", profile.Name, err)
	}
	return backend, model, nil
}

func (b *Bridge) validateAgentModelRoute(providerID, model string) error {
	providerID = strings.TrimSpace(providerID)
	model = strings.TrimSpace(model)
	if providerID == "" {
		if model != "" {
			return fmt.Errorf("%w: model requires an explicit provider", domain.ErrInvalidArgument)
		}
		return nil
	}
	b.mu.RLock()
	providers := append([]config.ProviderConfig(nil), b.config.Providers...)
	b.mu.RUnlock()
	provider, found := configuredProvider(providers, providerID)
	if !found {
		return fmt.Errorf("%w: provider %q is not configured", domain.ErrNotFound, providerID)
	}
	if provider.Kind == config.ProviderAntigravity {
		return antigravity.NewUnsupportedAuthModeError()
	}
	if (provider.Kind == config.ProviderOpenAICompatible || provider.Kind == config.ProviderGoogleAIStudio) && model == "" && strings.TrimSpace(provider.Model) == "" {
		return fmt.Errorf("%w: provider route requires a model", domain.ErrInvalidArgument)
	}
	if provider.Kind != config.ProviderOpenAICompatible && provider.Kind != config.ProviderGoogleAIStudio && provider.Kind != config.ProviderCodexAppServer {
		return fmt.Errorf("%w: unsupported provider kind %q", domain.ErrInvalidArgument, provider.Kind)
	}
	return nil
}

func (b *Bridge) chatBackendForRoute(ctx context.Context, providerID, modelOverride string) (agent.ModelBackend, string, error) {
	route, err := b.resolveInferenceRoute(providerID, modelOverride)
	if err != nil {
		return nil, "", err
	}
	b.mu.RLock()
	providers := append([]config.ProviderConfig(nil), b.config.Providers...)
	paths := b.paths
	allowed := append([]string(nil), b.config.AllowedDirectories...)
	b.mu.RUnlock()
	var selected *config.ProviderConfig
	for index := range providers {
		if providers[index].ID == route.ProviderID {
			selected = &providers[index]
			break
		}
	}
	if selected == nil {
		return nil, "", fmt.Errorf("configured provider %q no longer exists", route.ProviderID)
	}
	model := route.Model
	switch selected.Kind {
	case config.ProviderOpenAICompatible:
		secret, err := b.keyring.Get(ctx, selected.CredentialRef)
		if err != nil {
			return nil, "", errors.New("provider credential is unavailable in the system keyring")
		}
		client, err := openaiadapter.New(openaiadapter.Config{
			BaseURL: selected.BaseURL, APIKey: secret, Model: model,
			Style: openAIAdapterStyle(*selected),
		})
		if err != nil {
			return nil, "", err
		}
		return client, model, nil
	case config.ProviderGoogleAIStudio:
		secret, err := b.keyring.Get(ctx, selected.CredentialRef)
		if err != nil {
			return nil, "", errors.New("provider credential is unavailable in the system keyring")
		}
		client, err := b.newGoogleAIStudioClient(googleaistudio.Config{APIKey: secret, Model: model})
		if err != nil {
			return nil, "", err
		}
		backend, err := b.googleBackendWithSlowMode(ctx, *selected, model, client)
		if err != nil {
			return nil, "", err
		}
		return backend, model, nil
	case config.ProviderCodexAppServer:
		client, err := b.ensureCodex(ctx)
		if err != nil {
			return nil, "", err
		}
		backend, err := codexapp.NewBackend(client, paths.DataDirectory, allowed)
		if model == "" {
			model = "codex-default"
		}
		if err != nil {
			return nil, "", err
		}
		return gatedBackend{backend: backend, turns: b.modelTurns}, model, nil
	case config.ProviderAntigravity:
		return nil, "", antigravity.NewUnsupportedAuthModeError()
	default:
		return nil, "", fmt.Errorf("unsupported provider kind %q", selected.Kind)
	}
}

// resolveInferenceRoute captures the exact non-secret route before a durable
// run is created. Callers then pass the explicit values back to
// chatBackendForRoute, so a concurrent global-provider switch cannot relabel
// or redirect an in-flight run.
func (b *Bridge) resolveInferenceRoute(providerID, modelOverride string) (domain.RunInferenceRoute, error) {
	b.mu.RLock()
	providers := append([]config.ProviderConfig(nil), b.config.Providers...)
	b.mu.RUnlock()
	providerID = strings.TrimSpace(providerID)
	var selected *config.ProviderConfig
	if providerID != "" {
		for index := range providers {
			if providers[index].ID == providerID {
				selected = &providers[index]
				break
			}
		}
	} else {
		for index := range providers {
			if providers[index].Enabled {
				selected = &providers[index]
				break
			}
		}
	}
	if selected == nil {
		if providerID != "" {
			return domain.RunInferenceRoute{}, fmt.Errorf("configured provider %q no longer exists", providerID)
		}
		return domain.RunInferenceRoute{}, errors.New("configure and enable an AI provider in Settings")
	}
	model := strings.TrimSpace(modelOverride)
	if model == "" {
		model = strings.TrimSpace(selected.Model)
	}
	if selected.Kind == config.ProviderCodexAppServer && model == "" {
		model = "codex-default"
	}
	route := domain.RunInferenceRoute{ProviderID: strings.TrimSpace(selected.ID), Model: model}
	if !route.Valid() || route.ProviderID == "" {
		return domain.RunInferenceRoute{}, fmt.Errorf("%w: invalid inference route", domain.ErrInvalidArgument)
	}
	return route, nil
}

func openAIAdapterStyle(provider config.ProviderConfig) openaiadapter.APIStyle {
	if normalizedOpenAIAPIStyle(provider.APIStyle, provider.BaseURL) == config.ProviderAPIStyleChatCompletions {
		return openaiadapter.APIStyleChatCompletions
	}
	return openaiadapter.APIStyleResponses
}
