package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const maxCatalogModels = 5000

type ModelListOptions struct {
	Sort string
}

type ModelCatalogEntry struct {
	ID                  string
	Name                string
	Description         string
	ContextLength       int
	MaxCompletionTokens int
	PromptPrice         string
	CompletionPrice     string
	RequestPrice        string
	Free                bool
	SupportedParameters []string
	InputModalities     []string
	OutputModalities    []string
	Created             int64
}

type catalogEnvelope struct {
	Data []catalogModel `json:"data"`
}

type catalogModel struct {
	ID                  string          `json:"id"`
	Name                string          `json:"name"`
	Description         string          `json:"description"`
	ContextLength       int             `json:"context_length"`
	Created             int64           `json:"created"`
	Pricing             json.RawMessage `json:"pricing"`
	SupportedParameters []string        `json:"supported_parameters"`
	Architecture        struct {
		InputModalities  []string `json:"input_modalities"`
		OutputModalities []string `json:"output_modalities"`
	} `json:"architecture"`
	TopProvider struct {
		ContextLength       int `json:"context_length"`
		MaxCompletionTokens int `json:"max_completion_tokens"`
	} `json:"top_provider"`
}

type catalogPricing struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
	Request    string `json:"request"`
}

var allowedCatalogSorts = map[string]struct{}{
	"": {}, "pricing-low-to-high": {}, "pricing-high-to-low": {},
	"context-high-to-low": {}, "throughput-high-to-low": {}, "latency-low-to-high": {},
	"most-popular": {}, "top-weekly": {}, "newest": {},
	"intelligence-high-to-low": {}, "design-arena-elo-high-to-low": {},
}

// ListModels reads the standard OpenAI-compatible /models endpoint and keeps
// richer OpenRouter fields when the gateway provides them. Authorization is
// added only at the HTTP boundary and provider errors are credential-redacted.
func (c *Client) ListModels(ctx context.Context, options ModelListOptions) ([]ModelCatalogEntry, error) {
	if c == nil {
		return nil, providerError(ErrorKindRequest, "list models", 0, "client is nil", false, 0)
	}
	if ctx == nil {
		return nil, providerError(ErrorKindRequest, "list models", 0, "context is nil", false, 0)
	}
	sortValue := strings.TrimSpace(options.Sort)
	if _, ok := allowedCatalogSorts[sortValue]; !ok {
		return nil, providerError(ErrorKindRequest, "list models", 0, "unsupported model sort", false, 0)
	}
	requestURL, err := url.Parse(endpoint(c.config.BaseURL, "/models"))
	if err != nil {
		return nil, providerError(ErrorKindRequest, "list models", 0, "invalid models endpoint", false, 0)
	}
	if sortValue != "" {
		query := requestURL.Query()
		query.Set("sort", sortValue)
		requestURL.RawQuery = query.Encode()
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, providerError(ErrorKindRequest, "list models", 0, err.Error(), false, 0)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "yuri-agent/0.1")
	if c.config.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	}
	response, err := c.config.HTTPClient.Do(request)
	if err != nil {
		if contextError(ctx) != nil {
			return nil, contextError(ctx)
		}
		return nil, providerError(ErrorKindNetwork, "list models", 0, err.Error(), true, 0, c.config.APIKey)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, readErr := readErrorBody(response.Body, c.config.MaxResponseBytes)
		if readErr != nil {
			message = readErr.Error()
		}
		return nil, providerError(ErrorKindHTTP, "list models", response.StatusCode, message, isRetryableStatus(response.StatusCode), parseRetryAfter(response.Header.Get("Retry-After")), c.config.APIKey)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, c.config.MaxResponseBytes+1))
	if err != nil {
		return nil, providerError(ErrorKindDecode, "list models", response.StatusCode, err.Error(), false, 0)
	}
	if int64(len(data)) > c.config.MaxResponseBytes {
		return nil, providerError(ErrorKindResponseLimit, "list models", response.StatusCode, fmt.Sprintf("response exceeds %d bytes", c.config.MaxResponseBytes), false, 0)
	}
	var envelope catalogEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, providerError(ErrorKindDecode, "list models", response.StatusCode, err.Error(), false, 0)
	}
	if len(envelope.Data) > maxCatalogModels {
		return nil, providerError(ErrorKindResponseLimit, "list models", response.StatusCode, fmt.Sprintf("catalog exceeds %d models", maxCatalogModels), false, 0)
	}
	models := make([]ModelCatalogEntry, 0, len(envelope.Data))
	for _, raw := range envelope.Data {
		id := strings.TrimSpace(raw.ID)
		if id == "" || len(id) > 256 {
			continue
		}
		pricing := decodeCatalogPricing(raw.Pricing)
		contextLength := raw.ContextLength
		if contextLength <= 0 {
			contextLength = raw.TopProvider.ContextLength
		}
		models = append(models, ModelCatalogEntry{
			ID: id, Name: boundedCatalogText(raw.Name, 256), Description: boundedCatalogText(raw.Description, 2000),
			ContextLength: contextLength, MaxCompletionTokens: raw.TopProvider.MaxCompletionTokens,
			PromptPrice: pricing.Prompt, CompletionPrice: pricing.Completion, RequestPrice: pricing.Request,
			Free: modelIsFree(id, pricing), SupportedParameters: append([]string(nil), raw.SupportedParameters...),
			InputModalities:  append([]string(nil), raw.Architecture.InputModalities...),
			OutputModalities: append([]string(nil), raw.Architecture.OutputModalities...), Created: raw.Created,
		})
	}
	return models, nil
}

func decodeCatalogPricing(raw json.RawMessage) catalogPricing {
	var pricing catalogPricing
	if json.Unmarshal(raw, &pricing) == nil {
		return pricing
	}
	var tiers []catalogPricing
	if json.Unmarshal(raw, &tiers) == nil && len(tiers) > 0 {
		return tiers[0]
	}
	return catalogPricing{}
}

func modelIsFree(id string, pricing catalogPricing) bool {
	if strings.HasSuffix(strings.ToLower(id), ":free") {
		return true
	}
	if pricing.Prompt == "" || pricing.Completion == "" {
		return false
	}
	for _, value := range []string{pricing.Prompt, pricing.Completion, pricing.Request} {
		if value == "" {
			continue
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || parsed != 0 {
			return false
		}
	}
	return true
}

func boundedCatalogText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
