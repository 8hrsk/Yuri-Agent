package desktop

import (
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/config"
)

func configuredProvider(providers []config.ProviderConfig, id string) (config.ProviderConfig, bool) {
	for _, provider := range providers {
		if provider.ID == id {
			return provider, true
		}
	}
	return config.ProviderConfig{}, false
}

func normalizedOpenAIAPIStyle(style config.ProviderAPIStyle, baseURL string) config.ProviderAPIStyle {
	if style != "" {
		return style
	}
	if strings.Contains(strings.ToLower(baseURL), "openrouter.ai") {
		return config.ProviderAPIStyleChatCompletions
	}
	return config.ProviderAPIStyleResponses
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
		BaseURL: provider.BaseURL, Model: provider.Model, APIStyle: normalizedOpenAIAPIStyle(provider.APIStyle, provider.BaseURL),
		FavoriteModels: append([]string(nil), provider.FavoriteModels...), Binary: provider.Binary,
		Enabled: provider.Enabled, HasSecret: provider.CredentialRef != "",
	}
}
