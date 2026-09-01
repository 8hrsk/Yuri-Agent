package desktop

import (
	"errors"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestAgentFallbackRouteBridgePersistsExplicitOwnerChoice(t *testing.T) {
	bridge := newAgentTestBridge(t)
	bridge.config.Providers = []config.ProviderConfig{
		{ID: "primary", Kind: config.ProviderCodexAppServer, DisplayName: "Primary", Model: "primary-model", Enabled: true},
		{ID: "fallback", Kind: config.ProviderCodexAppServer, DisplayName: "Fallback", Model: "fallback-model", Enabled: true},
	}
	created, err := bridge.CreateAgent(CreateAgentInput{
		Name: "Эмили", Gender: "female", ProviderID: "primary", Model: "primary-model",
		FallbackEnabled: true, FallbackProviderID: "fallback", FallbackModel: "fallback-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created.FallbackEnabled || created.FallbackProviderID != "fallback" || created.FallbackModel != "fallback-model" {
		t.Fatalf("created fallback route = %#v", created)
	}

	disabled, err := bridge.UpdateActiveAgentFallbackRoute(UpdateAgentFallbackRouteInput{
		Enabled: false, ProviderID: "fallback", Model: "fallback-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if disabled.FallbackEnabled || disabled.FallbackProviderID != "fallback" || disabled.FallbackModel != "fallback-model" {
		t.Fatalf("disabled fallback route = %#v", disabled)
	}
	enabled, err := bridge.UpdateActiveAgentFallbackRoute(UpdateAgentFallbackRouteInput{
		Enabled: true, ProviderID: "fallback", Model: "fallback-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.FallbackEnabled || enabled.FallbackProviderID != "fallback" || enabled.FallbackModel != "fallback-model" {
		t.Fatalf("enabled fallback route = %#v", enabled)
	}
	stored, err := bridge.repositories.Agents.Get(t.Context(), domain.ID(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	if stored.ProviderID != "primary" || stored.Model != "primary-model" || !stored.FallbackEnabled {
		t.Fatalf("primary route changed with fallback update = %#v", stored)
	}
	if _, err := bridge.UpdateActiveAgentFallbackRoute(UpdateAgentFallbackRouteInput{
		Enabled: true, ProviderID: "fallback",
	}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("partial enabled fallback error = %v, want ErrInvalidArgument", err)
	}
}
