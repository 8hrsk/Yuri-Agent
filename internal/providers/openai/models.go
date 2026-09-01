package openai

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

const (
	maxCatalogModels          = 5000
	maxCapabilityCacheEntries = 4096
	capabilityCacheTTL        = 10 * time.Minute
	maxCatalogListValues      = 128
	maxCatalogListValueBytes  = 128
)

type ModelListOptions struct {
	Sort string
}

type ModelCatalogEntry struct {
	ID                            string
	Name                          string
	Description                   string
	ContextLength                 int
	MaxCompletionTokens           int
	PromptPrice                   string
	CompletionPrice               string
	RequestPrice                  string
	Free                          bool
	SupportedParameters           []string
	SupportsTools                 bool
	SupportsToolsKnown            bool
	SupportsStructuredOutput      bool
	SupportsStructuredOutputKnown bool
	SupportsJSONSchema            bool
	SupportsJSONSchemaKnown       bool
	InputModalities               []string
	OutputModalities              []string
	SupportsVision                bool
	SupportsVisionKnown           bool
	Created                       int64
}

type catalogEnvelope struct {
	Data []catalogModel `json:"data"`
}

type catalogModel struct {
	ID                  string          `json:"id"`
	Name                string          `json:"name"`
	Description         string          `json:"description"`
	ContextLength       int             `json:"context_length"`
	MaxCompletionTokens int             `json:"max_completion_tokens"`
	Created             int64           `json:"created"`
	Pricing             json.RawMessage `json:"pricing"`
	// Raw arrays let us distinguish an omitted field (capability unknown) from
	// an explicitly empty one (capability unsupported). This matters for
	// private/manual model IDs: absence of catalog metadata must never block a
	// request that might still be accepted by the endpoint.
	SupportedParameters json.RawMessage `json:"supported_parameters"`
	Architecture        struct {
		Modality         string          `json:"modality"`
		InputModalities  json.RawMessage `json:"input_modalities"`
		OutputModalities json.RawMessage `json:"output_modalities"`
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

// ModelCapabilities is a secret-free projection of one catalog row used by
// the request boundary. The Known flags are intentionally per capability: a
// provider can describe modalities while omitting supported_parameters, or
// vice versa.
type ModelCapabilities struct {
	SupportsTools                 bool
	SupportsToolsKnown            bool
	SupportsStructuredOutput      bool
	SupportsStructuredOutputKnown bool
	SupportsJSONSchema            bool
	SupportsJSONSchemaKnown       bool
	SupportsVision                bool
	SupportsVisionKnown           bool
	ContextLength                 int
	MaxCompletionTokens           int
	InputModalities               []string
	OutputModalities              []string
}

type cachedCapabilities struct {
	model        string
	capabilities ModelCapabilities
	updatedAt    time.Time
}

// The cache deliberately contains only bounded, non-secret model metadata.
// It is process-local: no API key, response body, description or price is
// retained here. A catalog request refreshes entries and a short TTL avoids a
// /models round trip before every tool-required run when the desktop bridge
// creates a fresh adapter for each run.
var capabilityCache = struct {
	sync.Mutex
	entries map[string]cachedCapabilities
	fetched map[string]time.Time
}{
	entries: make(map[string]cachedCapabilities),
	fetched: make(map[string]time.Time),
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
	markCatalogFetch(c.catalogScope(), time.Now())
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
		maxCompletionTokens := raw.MaxCompletionTokens
		if maxCompletionTokens <= 0 {
			maxCompletionTokens = raw.TopProvider.MaxCompletionTokens
		}
		supportedParameters, supportedParametersKnown := decodeCatalogStringList(raw.SupportedParameters)
		inputModalities, inputModalitiesKnown := decodeCatalogStringList(raw.Architecture.InputModalities)
		outputModalities, outputModalitiesKnown := decodeCatalogStringList(raw.Architecture.OutputModalities)
		if len(inputModalities) == 0 && strings.TrimSpace(raw.Architecture.Modality) != "" {
			derivedInput, derivedOutput := parseCatalogModality(raw.Architecture.Modality)
			if !inputModalitiesKnown {
				inputModalities = derivedInput
				inputModalitiesKnown = len(derivedInput) > 0
			}
			if !outputModalitiesKnown {
				outputModalities = derivedOutput
				outputModalitiesKnown = len(derivedOutput) > 0
			}
		}
		capabilities := catalogCapabilities(supportedParameters, supportedParametersKnown, inputModalities, inputModalitiesKnown, outputModalities, outputModalitiesKnown, contextLength, maxCompletionTokens)
		models = append(models, ModelCatalogEntry{
			ID: id, Name: boundedCatalogText(raw.Name, 256), Description: boundedCatalogText(raw.Description, 2000),
			ContextLength: contextLength, MaxCompletionTokens: maxCompletionTokens,
			PromptPrice: pricing.Prompt, CompletionPrice: pricing.Completion, RequestPrice: pricing.Request,
			Free: modelIsFree(id, pricing), SupportedParameters: append([]string(nil), supportedParameters...),
			SupportsTools: capabilities.SupportsTools, SupportsToolsKnown: capabilities.SupportsToolsKnown,
			SupportsStructuredOutput: capabilities.SupportsStructuredOutput, SupportsStructuredOutputKnown: capabilities.SupportsStructuredOutputKnown,
			SupportsJSONSchema: capabilities.SupportsJSONSchema, SupportsJSONSchemaKnown: capabilities.SupportsJSONSchemaKnown,
			InputModalities: append([]string(nil), inputModalities...), OutputModalities: append([]string(nil), outputModalities...),
			SupportsVision: capabilities.SupportsVision, SupportsVisionKnown: capabilities.SupportsVisionKnown, Created: raw.Created,
		})
		storeCatalogCapabilities(c.catalogScope(), id, capabilities, time.Now())
	}
	return models, nil
}

// ModelCapabilities returns the most recent process-local catalog metadata for
// model. The bool reports whether the model itself was present in a catalog;
// individual capability Known flags still determine whether a particular
// capability can be enforced. It never performs network I/O.
func (c *Client) ModelCapabilities(model string) (ModelCapabilities, bool) {
	if c == nil {
		return ModelCapabilities{}, false
	}
	return cachedModelCapabilities(c.catalogScope(), model, time.Now())
}

// validateRequiredCapabilities performs a conservative preflight. A catalog
// row is authoritative only when it explicitly describes supported_parameters
// and says that tools are absent. If /models is unavailable, or the selected
// ID is not listed, the request proceeds for compatibility with private and
// manually entered OpenAI-compatible model IDs.
func (c *Client) validateRequiredCapabilities(ctx context.Context, request agent.ModelRequest) error {
	if request.ToolChoice.Mode != agent.ToolChoiceRequired || len(request.Tools) == 0 {
		return nil
	}
	model := strings.TrimSpace(request.Model)
	if model == "" {
		model = strings.TrimSpace(c.config.Model)
	}
	if model == "" {
		return nil
	}
	capabilities, found := c.ModelCapabilities(model)
	if !found && !catalogFetchedRecently(c.catalogScope(), time.Now()) {
		// Do not turn a gateway without /models into a hard failure. The selected
		// model may be private or the endpoint may intentionally expose only the
		// completion route; Start will still perform the normal provider request.
		_, _ = c.ListModels(ctx, ModelListOptions{})
		capabilities, found = c.ModelCapabilities(model)
	}
	if found && capabilities.SupportsToolsKnown && !capabilities.SupportsTools {
		return &agent.ModelCapabilityError{Model: model, Capability: "tools"}
	}
	return nil
}

func catalogCapabilities(parameters []string, parametersKnown bool, inputModalities []string, inputKnown bool, outputModalities []string, outputKnown bool, contextLength, maxCompletionTokens int) ModelCapabilities {
	tools, structured, schema := false, false, false
	if parametersKnown {
		tools = containsCatalogParameter(parameters, "tools")
		structured = containsCatalogParameter(parameters, "structured_outputs", "response_format", "json_object")
		schema = containsCatalogParameter(parameters, "json_schema", "structured_outputs", "response_format")
	}
	vision := containsVisionModality(inputModalities)
	return ModelCapabilities{
		SupportsTools: tools, SupportsToolsKnown: parametersKnown,
		SupportsStructuredOutput: structured, SupportsStructuredOutputKnown: parametersKnown,
		SupportsJSONSchema: schema, SupportsJSONSchemaKnown: parametersKnown,
		SupportsVision: vision, SupportsVisionKnown: inputKnown,
		ContextLength: contextLength, MaxCompletionTokens: maxCompletionTokens,
		InputModalities: append([]string(nil), inputModalities...), OutputModalities: append([]string(nil), outputModalities...),
	}
}

func decodeCatalogStringList(raw json.RawMessage) ([]string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, false
	}
	if len(values) > maxCatalogListValues {
		values = values[:maxCatalogListValues]
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || len(value) > maxCatalogListValueBytes {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, true
}

func parseCatalogModality(value string) ([]string, []string) {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(value)), "->", 2)
	if len(parts) == 0 {
		return nil, nil
	}
	input := splitCatalogModalities(parts[0])
	if len(parts) == 1 {
		return input, nil
	}
	return input, splitCatalogModalities(parts[1])
}

func splitCatalogModalities(value string) []string {
	value = strings.ReplaceAll(value, ",", "+")
	values := strings.FieldsFunc(value, func(r rune) bool { return r == '+' || r == '/' || r == ' ' })
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, item := range values {
		item = strings.TrimSpace(item)
		if item == "" || len(item) > maxCatalogListValueBytes {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

func containsCatalogParameter(values []string, expected ...string) bool {
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		for _, candidate := range expected {
			if value == candidate {
				return true
			}
		}
	}
	return false
}

func containsVisionModality(values []string) bool {
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "image" || value == "images" || value == "vision" || value == "video" || value == "videos" {
			return true
		}
	}
	return false
}

func catalogEndpoint(baseURL string) string { return endpoint(baseURL, "/models") }

// catalogScope isolates capability metadata for two accounts that use the
// same gateway URL but receive different private model catalogs. The API key
// itself is never retained in the cache key; only an in-memory one-way digest
// namespaces the bounded process-local entries.
func (c *Client) catalogScope() string {
	digest := sha256.Sum256([]byte(c.config.APIKey))
	return catalogEndpoint(c.config.BaseURL) + "\x00" + fmt.Sprintf("%x", digest[:16])
}

func catalogCacheKey(scope, model string) string {
	return scope + "\x00" + strings.TrimSpace(model)
}

func markCatalogFetch(scope string, now time.Time) {
	capabilityCache.Lock()
	defer capabilityCache.Unlock()
	capabilityCache.fetched[scope] = now
}

func catalogFetchedRecently(scope string, now time.Time) bool {
	capabilityCache.Lock()
	defer capabilityCache.Unlock()
	when, ok := capabilityCache.fetched[scope]
	return ok && now.Sub(when) >= 0 && now.Sub(when) < capabilityCacheTTL
}

func storeCatalogCapabilities(scope, model string, capabilities ModelCapabilities, now time.Time) {
	key := catalogCacheKey(scope, model)
	capabilityCache.Lock()
	defer capabilityCache.Unlock()
	if len(capabilityCache.entries) >= maxCapabilityCacheEntries {
		oldestKey := ""
		var oldest time.Time
		for candidate, entry := range capabilityCache.entries {
			if oldestKey == "" || entry.updatedAt.Before(oldest) {
				oldestKey, oldest = candidate, entry.updatedAt
			}
		}
		if oldestKey != "" {
			delete(capabilityCache.entries, oldestKey)
		}
	}
	capabilityCache.entries[key] = cachedCapabilities{model: strings.TrimSpace(model), capabilities: cloneModelCapabilities(capabilities), updatedAt: now}
}

func cachedModelCapabilities(scope, model string, now time.Time) (ModelCapabilities, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return ModelCapabilities{}, false
	}
	capabilityCache.Lock()
	defer capabilityCache.Unlock()
	key := catalogCacheKey(scope, model)
	entry, ok := capabilityCache.entries[key]
	if !ok {
		return ModelCapabilities{}, false
	}
	if now.Sub(entry.updatedAt) < 0 || now.Sub(entry.updatedAt) >= capabilityCacheTTL {
		delete(capabilityCache.entries, key)
		return ModelCapabilities{}, false
	}
	return cloneModelCapabilities(entry.capabilities), true
}

func cloneModelCapabilities(value ModelCapabilities) ModelCapabilities {
	value.InputModalities = append([]string(nil), value.InputModalities...)
	value.OutputModalities = append([]string(nil), value.OutputModalities...)
	return value
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
