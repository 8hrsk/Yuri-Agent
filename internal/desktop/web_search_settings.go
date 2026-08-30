package desktop

import (
	"context"
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/config"
	builtintools "github.com/OrdoAI/yuri-agent/internal/tools"
)

const webSearchConnectivityQuery = "Yuri connectivity check"

type WebSearchSettingsView struct {
	Enabled            bool   `json:"enabled"`
	Provider           string `json:"provider"`
	Endpoint           string `json:"endpoint"`
	DefaultResultLimit int    `json:"defaultResultLimit"`
}

type WebSearchTestResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func webSearchSettingsView(value config.WebSearchConfig) WebSearchSettingsView {
	limit := value.DefaultResultLimit
	if limit == 0 {
		limit = 5
	}
	provider := strings.TrimSpace(value.Provider)
	if provider == "" {
		provider = "searxng"
	}
	return WebSearchSettingsView{
		Enabled: value.Enabled, Provider: provider, Endpoint: value.Endpoint, DefaultResultLimit: limit,
	}
}

func (b *Bridge) GetWebSearchSettings() WebSearchSettingsView {
	b.mu.RLock()
	value := b.config.WebSearch
	b.mu.RUnlock()
	return webSearchSettingsView(value)
}

func (b *Bridge) SaveWebSearchSettings(input WebSearchSettingsView) error {
	value := config.WebSearchConfig{
		Enabled: input.Enabled, Provider: strings.TrimSpace(input.Provider),
		Endpoint: strings.TrimRight(strings.TrimSpace(input.Endpoint), "/"), DefaultResultLimit: input.DefaultResultLimit,
	}
	if value.Provider == "" {
		value.Provider = "searxng"
	}
	if err := value.Validate(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	candidate := b.config
	candidate.WebSearch = value
	if err := config.Save(b.paths, candidate); err != nil {
		return err
	}
	b.config = candidate
	return nil
}

// TestWebSearchSettings probes the candidate endpoint without persisting it.
// This is an explicit owner action; no provider credentials or browser state
// are attached to the request.
func (b *Bridge) TestWebSearchSettings(input WebSearchSettingsView) WebSearchTestResult {
	value := config.WebSearchConfig{
		Enabled: true, Provider: strings.TrimSpace(input.Provider),
		Endpoint: strings.TrimRight(strings.TrimSpace(input.Endpoint), "/"), DefaultResultLimit: input.DefaultResultLimit,
	}
	if value.Provider == "" {
		value.Provider = "searxng"
	}
	if err := value.Validate(); err != nil {
		return WebSearchTestResult{Message: err.Error()}
	}
	tool, err := newWebSearch(value)
	if err != nil {
		return WebSearchTestResult{Message: err.Error()}
	}
	ctx, cancel := b.context()
	defer cancel()
	response, err := tool.Execute(ctx, builtintools.WebSearchRequest{Query: webSearchConnectivityQuery, Limit: 3, Language: "all"})
	if err != nil {
		if ctxErr := context.Cause(ctx); ctxErr != nil {
			err = ctxErr
		}
		return WebSearchTestResult{Message: fmt.Sprintf("SearXNG недоступен: %s", truncateRunes(safeError(err.Error()), 300))}
	}
	return WebSearchTestResult{OK: true, Message: fmt.Sprintf("SearXNG отвечает; получено результатов: %d.", len(response.Results))}
}
