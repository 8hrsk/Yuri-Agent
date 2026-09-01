package desktop

import (
	"errors"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/config"
	openaiadapter "github.com/OrdoAI/yuri-agent/internal/providers/openai"
)

// ListOpenAIModels loads a credential only from the system keyring and returns
// a bounded, secret-free catalog projection. Disabled provider drafts are
// intentionally supported so model selection can happen before activation.
func (b *Bridge) ListOpenAIModels(input OpenAIModelCatalogInput) ([]OpenAIModelView, error) {
	providerID := strings.TrimSpace(input.ProviderID)
	b.mu.RLock()
	providers := append([]config.ProviderConfig(nil), b.config.Providers...)
	b.mu.RUnlock()
	var selected config.ProviderConfig
	found := false
	for _, provider := range providers {
		if provider.Kind != config.ProviderOpenAICompatible {
			continue
		}
		if providerID == "" || provider.ID == providerID {
			selected, found = provider, true
			break
		}
	}
	if !found {
		return nil, errors.New("Сначала сохраните OpenAI-compatible endpoint и API key")
	}
	if b.keyring == nil {
		return nil, errors.New("API key недоступен в системном keyring")
	}
	ctx, cancel := b.context()
	defer cancel()
	secret, err := b.keyring.Get(ctx, selected.CredentialRef)
	if err != nil {
		return nil, errors.New("API key недоступен в системном keyring")
	}
	client, err := openaiadapter.New(openaiadapter.Config{
		BaseURL: selected.BaseURL, APIKey: secret, Model: selected.Model,
		Style: openAIAdapterStyle(selected), MaxAttempts: 1,
	})
	if err != nil {
		return nil, errors.New(safeError(err.Error()))
	}
	models, err := client.ListModels(ctx, openaiadapter.ModelListOptions{Sort: strings.TrimSpace(input.Sort)})
	if err != nil {
		return nil, errors.New(safeError(err.Error()))
	}
	favorites := make(map[string]struct{}, len(selected.FavoriteModels))
	for _, model := range selected.FavoriteModels {
		favorites[model] = struct{}{}
	}
	views := make([]OpenAIModelView, 0, len(models))
	for _, model := range models {
		_, favorite := favorites[model.ID]
		views = append(views, OpenAIModelView{
			ID: model.ID, Name: model.Name, Description: model.Description,
			ContextLength: model.ContextLength, MaxCompletionTokens: model.MaxCompletionTokens,
			PromptPrice: model.PromptPrice, CompletionPrice: model.CompletionPrice, RequestPrice: model.RequestPrice,
			Free: model.Free, SupportsTools: containsCatalogValue(model.SupportedParameters, "tools"),
			InputModalities: model.InputModalities, OutputModalities: model.OutputModalities,
			Created: model.Created, Favorite: favorite,
		})
	}
	return views, nil
}

func (b *Bridge) SetProviderModelFavorite(input SetProviderModelFavoriteInput) (ProviderView, error) {
	providerID, model := strings.TrimSpace(input.ProviderID), strings.TrimSpace(input.Model)
	if providerID == "" || model == "" || len(model) > 256 {
		return ProviderView{}, errors.New("providerId and a valid model are required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	provider, found := configuredProvider(b.config.Providers, providerID)
	if !found || provider.Kind != config.ProviderOpenAICompatible {
		return ProviderView{}, errors.New("OpenAI-compatible provider not found")
	}
	favorites := make([]string, 0, len(provider.FavoriteModels)+1)
	seen := false
	for _, candidate := range provider.FavoriteModels {
		if candidate == model {
			seen = true
			if !input.Favorite {
				continue
			}
		}
		favorites = append(favorites, candidate)
	}
	if input.Favorite && !seen {
		favorites = append(favorites, model)
	}
	provider.FavoriteModels = favorites
	candidate := b.config
	candidate.Providers = upsertProvider(candidate.Providers, provider)
	if err := candidate.Validate(); err != nil {
		return ProviderView{}, err
	}
	if err := config.Save(b.paths, candidate); err != nil {
		return ProviderView{}, err
	}
	b.config = candidate
	return providerView(provider), nil
}

func containsCatalogValue(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), expected) {
			return true
		}
	}
	return false
}
