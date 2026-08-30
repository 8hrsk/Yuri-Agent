package desktop

import (
	"github.com/OrdoAI/yuri-agent/internal/config"
)

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
