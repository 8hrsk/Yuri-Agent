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
	"github.com/OrdoAI/yuri-agent/internal/providers/antigravity"
	openaiadapter "github.com/OrdoAI/yuri-agent/internal/providers/openai"
)

func (b *Bridge) probeProvider(input ProviderSettingsInput) ProviderTestResult {
	ctx, cancel := b.context()
	defer cancel()
	if input.Kind == config.ProviderAntigravity {
		status := antigravity.Status()
		return ProviderTestResult{
			Message: status.Message, ErrorCode: status.ErrorCode,
			Alternative: status.Alternative, ProviderID: antigravity.ProviderID,
			Onboarding: b.GetOnboardingState(),
		}
	}
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
	candidate.Onboarding.Completed = candidate.Onboarding.AgentConfigured
	candidate.Onboarding.ProviderTested = true
	if err := config.Save(b.paths, candidate); err != nil {
		return err
	}
	b.config = candidate
	return nil
}
