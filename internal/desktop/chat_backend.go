package desktop

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/providers/antigravity"
	"github.com/OrdoAI/yuri-agent/internal/providers/codexapp"
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

func (b *Bridge) chatBackend(ctx context.Context) (agent.ModelBackend, string, error) {
	b.mu.RLock()
	providers := append([]config.ProviderConfig(nil), b.config.Providers...)
	paths := b.paths
	allowed := append([]string(nil), b.config.AllowedDirectories...)
	b.mu.RUnlock()
	var selected *config.ProviderConfig
	for index := range providers {
		if providers[index].Enabled {
			selected = &providers[index]
			break
		}
	}
	if selected == nil {
		return nil, "", errors.New("configure and enable an AI provider in Settings")
	}
	model := strings.TrimSpace(selected.Model)
	switch selected.Kind {
	case config.ProviderOpenAICompatible:
		secret, err := b.keyring.Get(ctx, selected.CredentialRef)
		if err != nil {
			return nil, "", errors.New("provider credential is unavailable in the system keyring")
		}
		client, err := openaiadapter.New(openaiadapter.Config{
			BaseURL: selected.BaseURL, APIKey: secret, Model: model,
			Style: openaiadapter.APIStyleResponses,
		})
		if err != nil {
			return nil, "", err
		}
		return client, model, nil
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
