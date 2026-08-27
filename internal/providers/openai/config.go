// Package openai implements the raw HTTP ModelBackend for OpenAI-compatible
// Responses and Chat Completions endpoints. It intentionally has no SDK
// dependency so the agent runtime remains portable across providers.
package openai

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type APIStyle string

const (
	APIStyleResponses       APIStyle = "responses"
	APIStyleChatCompletions APIStyle = "chat_completions"
)

// Config contains only adapter-local configuration. APIKey is consumed at the
// HTTP boundary and is never copied into requests, logs, or returned errors.
// Production wiring should load it from a keychain-backed secret port.
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Style   APIStyle

	HTTPClient *http.Client
	Timeout    time.Duration

	MaxRequestBytes  int64
	MaxResponseBytes int64
	MaxEvents        int
	MaxLineBytes     int
	MaxAttempts      int
	InitialBackoff   time.Duration
	MaxBackoff       time.Duration
	Organization     string
	Project          string
	ExtraHeaders     map[string]string
}

const (
	defaultTimeout          = 2 * time.Minute
	defaultMaxRequestBytes  = int64(4 * 1024 * 1024)
	defaultMaxResponseBytes = int64(16 * 1024 * 1024)
	defaultMaxEvents        = 10000
	defaultMaxLineBytes     = 1024 * 1024
	defaultMaxAttempts      = 3
	defaultInitialBackoff   = 200 * time.Millisecond
	defaultMaxBackoff       = 4 * time.Second
)

func (c Config) normalized() (Config, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return Config{}, fmt.Errorf("openai: base URL is required")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return Config{}, fmt.Errorf("openai: invalid base URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Config{}, fmt.Errorf("openai: base URL must use http or https")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Config{}, fmt.Errorf("openai: base URL must not contain credentials, query, or fragment")
	}
	if c.Style == "" {
		c.Style = APIStyleResponses
	}
	if c.Style != APIStyleResponses && c.Style != APIStyleChatCompletions {
		return Config{}, fmt.Errorf("openai: unsupported API style %q", c.Style)
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.MaxRequestBytes <= 0 {
		c.MaxRequestBytes = defaultMaxRequestBytes
	}
	if c.MaxResponseBytes <= 0 {
		c.MaxResponseBytes = defaultMaxResponseBytes
	}
	if c.MaxEvents <= 0 {
		c.MaxEvents = defaultMaxEvents
	}
	if c.MaxLineBytes <= 0 {
		c.MaxLineBytes = defaultMaxLineBytes
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = defaultMaxAttempts
	}
	if c.InitialBackoff <= 0 {
		c.InitialBackoff = defaultInitialBackoff
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = defaultMaxBackoff
	}
	if c.MaxBackoff < c.InitialBackoff {
		c.MaxBackoff = c.InitialBackoff
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{}
	}
	// Copy headers so a caller cannot mutate adapter behavior during an active
	// request, and normalize only names the HTTP package itself understands.
	if c.ExtraHeaders != nil {
		headers := make(map[string]string, len(c.ExtraHeaders))
		for key, value := range c.ExtraHeaders {
			headers[key] = value
		}
		c.ExtraHeaders = headers
	}
	return c, nil
}

func endpoint(baseURL string, path string) string {
	u, _ := url.Parse(baseURL)
	basePath := strings.TrimRight(u.Path, "/")
	if basePath == "" {
		basePath = ""
	}
	u.Path = basePath + "/" + strings.TrimLeft(path, "/")
	u.RawPath = ""
	return u.String()
}
