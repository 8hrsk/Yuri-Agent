package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

const (
	WebSearchToolID       = "web.search"
	defaultSearchLimit    = 5
	minSearchLimit        = 3
	maxSearchLimit        = 10
	maxSearchResponseBody = 2 << 20
)

type WebSearchRequest struct {
	Query    string `json:"query"`
	Limit    int    `json:"limit,omitempty"`
	Language string `json:"language,omitempty"`
}

type WebSearchResult struct {
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet,omitempty"`
	Source      string  `json:"source,omitempty"`
	Score       float64 `json:"score,omitempty"`
	PublishedAt string  `json:"published_at,omitempty"`
}

type WebSearchResponse struct {
	Query   string            `json:"query"`
	Results []WebSearchResult `json:"results"`
}

// SearchProvider keeps the agent tool independent from any search engine.
// Providers return a small normalized result set; fetching a selected page is
// deliberately a separate web.fetch action with its own trace and limits.
type SearchProvider interface {
	Search(context.Context, WebSearchRequest) (WebSearchResponse, error)
}

type WebSearchTool struct{ provider SearchProvider }

func NewWebSearch(provider SearchProvider) (*WebSearchTool, error) {
	if provider == nil {
		return nil, errors.New("web search provider is required")
	}
	return &WebSearchTool{provider: provider}, nil
}

func (tool *WebSearchTool) Definition() ToolDefinition {
	return ToolDefinition{
		ID:           WebSearchToolID,
		Description:  "Search the public web and return normalized titles, URLs, and snippets. Use web.fetch separately to read a selected result.",
		Risk:         domain.RiskLow,
		Capabilities: []domain.Capability{domain.CapabilityNetworkHTTP},
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"query":    map[string]any{"type": "string", "description": "Search query"},
				"limit":    map[string]any{"type": "integer", "minimum": minSearchLimit, "maximum": maxSearchLimit},
				"language": map[string]any{"type": "string", "description": "Optional language code, for example ru or en"},
			},
			"required": []string{"query"},
		},
	}
}

func (tool *WebSearchTool) ID() string { return WebSearchToolID }

func (tool *WebSearchTool) Execute(ctx context.Context, request WebSearchRequest) (WebSearchResponse, error) {
	if tool == nil || tool.provider == nil {
		return WebSearchResponse{}, errors.New("web search provider is unavailable")
	}
	request.Query = strings.TrimSpace(request.Query)
	request.Language = strings.TrimSpace(request.Language)
	if request.Query == "" {
		return WebSearchResponse{}, errors.New("web search query is required")
	}
	if request.Limit == 0 {
		request.Limit = defaultSearchLimit
	}
	if request.Limit < minSearchLimit || request.Limit > maxSearchLimit {
		return WebSearchResponse{}, fmt.Errorf("web search limit must be between %d and %d", minSearchLimit, maxSearchLimit)
	}
	return tool.provider.Search(ctx, request)
}

type SearXNGConfig struct {
	Endpoint string
	Client   *http.Client
	Timeout  time.Duration
}

type SearXNGProvider struct {
	endpoint *url.URL
	client   *http.Client
}

func NewSearXNGProvider(config SearXNGConfig) (*SearXNGProvider, error) {
	endpoint, err := url.Parse(strings.TrimSpace(config.Endpoint))
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") {
		return nil, errors.New("SearXNG endpoint must be an absolute HTTP(S) URL")
	}
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("SearXNG endpoint must not contain credentials, query, or fragment")
	}
	loopback := strings.EqualFold(endpoint.Hostname(), "localhost") || net.ParseIP(endpoint.Hostname()).IsLoopback()
	if endpoint.Scheme != "https" && !loopback {
		return nil, errors.New("SearXNG endpoint must use HTTPS outside localhost")
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/search"
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	client := config.Client
	if client == nil {
		origin := endpoint.Scheme + "://" + endpoint.Host
		client = &http.Client{
			Transport: &http.Transport{
				Proxy: nil, ForceAttemptHTTP2: true, MaxIdleConns: 4, MaxIdleConnsPerHost: 2,
				IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 10 * time.Second, MaxResponseHeaderBytes: 64 * 1024,
			},
			Timeout: timeout,
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return errors.New("SearXNG stopped after 3 redirects")
				}
				if request.URL.Scheme+"://"+request.URL.Host != origin {
					return errors.New("SearXNG redirect changed endpoint origin")
				}
				return nil
			},
		}
	}
	return &SearXNGProvider{endpoint: endpoint, client: client}, nil
}

func (provider *SearXNGProvider) Search(ctx context.Context, request WebSearchRequest) (WebSearchResponse, error) {
	endpoint := *provider.endpoint
	query := endpoint.Query()
	query.Set("q", request.Query)
	query.Set("format", "json")
	if request.Language != "" {
		query.Set("language", request.Language)
	}
	endpoint.RawQuery = query.Encode()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return WebSearchResponse{}, fmt.Errorf("create SearXNG request: %w", err)
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("User-Agent", "Yuri-Agent/0.1 web.search")
	response, err := provider.client.Do(httpRequest)
	if err != nil {
		return WebSearchResponse{}, fmt.Errorf("query SearXNG: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return WebSearchResponse{}, fmt.Errorf("SearXNG returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Results []struct {
			Title         string  `json:"title"`
			URL           string  `json:"url"`
			Content       string  `json:"content"`
			Engine        string  `json:"engine"`
			Score         float64 `json:"score"`
			PublishedDate string  `json:"publishedDate"`
		} `json:"results"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxSearchResponseBody+1))
	if err := decoder.Decode(&payload); err != nil {
		return WebSearchResponse{}, fmt.Errorf("decode SearXNG response: %w", err)
	}
	results := make([]WebSearchResult, 0, min(request.Limit, len(payload.Results)))
	for _, item := range payload.Results {
		if len(results) >= request.Limit {
			break
		}
		parsed, err := url.Parse(strings.TrimSpace(item.URL))
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			continue
		}
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = parsed.Hostname()
		}
		results = append(results, WebSearchResult{
			Title: title, URL: parsed.String(), Snippet: strings.TrimSpace(item.Content),
			Source: strings.TrimSpace(item.Engine), Score: item.Score, PublishedAt: strings.TrimSpace(item.PublishedDate),
		})
	}
	return WebSearchResponse{Query: request.Query, Results: results}, nil
}
