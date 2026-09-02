package desktop

import (
	"errors"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/config"
	googleaistudio "github.com/OrdoAI/yuri-agent/internal/providers/googleaistudio"
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
		if provider.Kind != config.ProviderOpenAICompatible && provider.Kind != config.ProviderGoogleAIStudio {
			continue
		}
		if providerID == "" || provider.ID == providerID {
			selected, found = provider, true
			break
		}
	}
	if !found {
		return nil, errors.New("Сначала сохраните provider и API key")
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
	if selected.Kind == config.ProviderGoogleAIStudio {
		client, err := googleaistudio.New(googleaistudio.Config{APIKey: secret, Model: selected.Model, MaxResponseBytes: 16 * 1024 * 1024, Timeout: 30 * time.Second})
		if err != nil {
			return nil, errors.New(safeError(err.Error()))
		}
		models, err := client.ListModels(ctx)
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
			views = append(views, OpenAIModelView{ID: model.ID, Name: model.DisplayName, Description: model.Description,
				ContextLength: int(model.InputTokenLimit), MaxCompletionTokens: int(model.OutputTokenLimit),
				// The native catalog does not authoritatively report Free Tier,
				// function-calling, or modality support for the caller's project.
				// Leave those capability flags unknown instead of inventing them.
				Free: false, InputModalities: []string{"text"}, OutputModalities: []string{"text"}, Favorite: favorite})
		}
		return views, nil
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
			Free: model.Free, SupportsTools: model.SupportsTools, SupportsToolsKnown: model.SupportsToolsKnown,
			SupportsStructuredOutput: model.SupportsStructuredOutput, SupportsStructuredOutputKnown: model.SupportsStructuredOutputKnown,
			SupportsJSONSchema: model.SupportsJSONSchema, SupportsJSONSchemaKnown: model.SupportsJSONSchemaKnown,
			InputModalities: model.InputModalities, OutputModalities: model.OutputModalities,
			SupportsVision: model.SupportsVision, SupportsVisionKnown: model.SupportsVisionKnown,
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
	if !found || (provider.Kind != config.ProviderOpenAICompatible && provider.Kind != config.ProviderGoogleAIStudio) {
		return ProviderView{}, errors.New("provider not found")
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
