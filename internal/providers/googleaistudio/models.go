package googleaistudio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

const (
	maxCatalogModels = 5000
	maxCatalogPages  = 20
)

// Model is the native Gemini catalog projection. Free is deliberately absent:
// model discovery does not authoritatively reveal a project's billing tier or
// whether a model currently has Free Tier capacity.
type Model struct {
	ID                         string
	DisplayName                string
	Description                string
	Version                    string
	InputTokenLimit            int64
	OutputTokenLimit           int64
	SupportedGenerationMethods []string
	SupportsGenerateContent    bool
	SupportsCountTokens        bool
}

type nativeModel struct {
	Name                       string   `json:"name"`
	DisplayName                string   `json:"displayName"`
	Description                string   `json:"description"`
	Version                    string   `json:"version"`
	InputTokenLimit            int64    `json:"inputTokenLimit"`
	OutputTokenLimit           int64    `json:"outputTokenLimit"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
}

type nativeModelsEnvelope struct {
	Models        []nativeModel `json:"models"`
	NextPageToken string        `json:"nextPageToken"`
}

// ListModels reads the native Gemini model catalog because Google does not
// document the OpenAI-compatible /models route as the source of truth.
func (c *Client) ListModels(ctx context.Context) ([]Model, error) {
	if c == nil {
		return nil, &Error{Operation: "list models", Message: "client is nil"}
	}
	if ctx == nil {
		return nil, &Error{Operation: "list models", Message: "context is nil"}
	}
	var result []Model
	pageToken := ""
	for pageNumber := 0; pageNumber < maxCatalogPages; pageNumber++ {
		requestURL, err := c.nativeURL("models")
		if err != nil {
			return nil, &Error{Operation: "list models", Message: err.Error()}
		}
		query := requestURL.Query()
		query.Set("pageSize", "1000")
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		requestURL.RawQuery = query.Encode()
		var envelope nativeModelsEnvelope
		if err := c.getJSON(ctx, "list models", requestURL.String(), &envelope); err != nil {
			return nil, err
		}
		for _, raw := range envelope.Models {
			model, ok := modelFromNative(raw)
			// The selector must not offer embedding-only or tuning models to an
			// inference route. A manually configured model ID remains possible
			// through Start, but catalog discovery presents only models that
			// declare a generation method.
			if !ok || !model.SupportsGenerateContent {
				continue
			}
			result = append(result, model)
			if len(result) > maxCatalogModels {
				return nil, &Error{Operation: "list models", Message: fmt.Sprintf("catalog exceeds %d models", maxCatalogModels)}
			}
		}
		pageToken = strings.TrimSpace(envelope.NextPageToken)
		if pageToken == "" {
			return result, nil
		}
	}
	return nil, &Error{Operation: "list models", Message: "catalog pagination exceeds configured limit"}
}

func modelFromNative(raw nativeModel) (Model, bool) {
	id, err := normalizeModelID(raw.Name)
	if err != nil {
		return Model{}, false
	}
	methods := uniqueStrings(raw.SupportedGenerationMethods, 64)
	model := Model{
		ID: id, DisplayName: boundedText(raw.DisplayName, 256), Description: boundedText(raw.Description, 2000),
		Version: boundedText(raw.Version, 256), InputTokenLimit: raw.InputTokenLimit,
		OutputTokenLimit: raw.OutputTokenLimit, SupportedGenerationMethods: methods,
	}
	for _, method := range methods {
		switch method {
		case "generatecontent", "streamgeneratecontent":
			model.SupportsGenerateContent = true
		case "counttokens":
			model.SupportsCountTokens = true
		}
	}
	return model, true
}

func (c *Client) nativeURL(element string) (*url.URL, error) {
	base, err := url.Parse(c.nativeBaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid native API endpoint")
	}
	base.Path = path.Join(base.Path, element)
	base.RawPath = ""
	return base, nil
}

func (c *Client) getJSON(ctx context.Context, operation, rawURL string, destination any) error {
	if err := nativeContextError(ctx); err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return &Error{Operation: operation, Message: sanitize(err.Error(), c.config.APIKey)}
	}
	c.applyHeaders(request)
	response, err := c.config.HTTPClient.Do(request)
	if err != nil {
		if err := nativeContextError(ctx); err != nil {
			return err
		}
		return &Error{Operation: operation, Message: sanitize(err.Error(), c.config.APIKey), Retryable: true}
	}
	defer response.Body.Close()
	body, err := readBounded(response.Body, c.config.MaxResponseBytes)
	if err != nil {
		return &Error{Operation: operation, StatusCode: response.StatusCode, Message: err.Error()}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return ParseError(operation, response.StatusCode, body, response.Header, c.config.APIKey)
	}
	if err := json.Unmarshal(body, destination); err != nil {
		return &Error{Operation: operation, StatusCode: response.StatusCode, Message: sanitize(err.Error(), c.config.APIKey)}
	}
	return nil
}

func (c *Client) applyHeaders(request *http.Request) {
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "yuri-agent/0.1")
	request.Header.Set("x-goog-api-client", c.config.clientHeader())
	if c.config.APIKey != "" {
		// Header authentication avoids putting an owner key in request URLs,
		// browser history, proxy logs, or Go's URL formatting errors.
		request.Header.Set("x-goog-api-key", c.config.APIKey)
	}
}

func readBounded(reader io.Reader, maximum int64) ([]byte, error) {
	if maximum <= 0 {
		maximum = defaultMaxResponseSize
	}
	body, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maximum {
		return nil, fmt.Errorf("response exceeds configured limit")
	}
	return body, nil
}
