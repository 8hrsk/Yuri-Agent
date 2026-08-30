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

	// HTTPClient is used as-is. Leave its own Timeout at zero: it covers the
	// whole response body, including the stream, and would reintroduce the
	// total-duration cutoff the two budgets below exist to avoid.
	HTTPClient *http.Client

	// Timeout is the hard deadline for connection setup, sending the request
	// (including retry backoff), and receiving the first byte of the response
	// body. It deliberately does NOT bound the total duration of a streaming
	// response: a healthy generation may stream for far longer than this, and
	// cutting it off mid-stream surfaces to the runtime as a cancelled run.
	// Default: 2 minutes.
	Timeout time.Duration
	// StreamIdleTimeout bounds the gap between two consecutive bytes of the
	// response body. It is re-armed on every chunk received, so a slow but
	// steady stream never expires while only a fully stalled one is cancelled.
	// Default: 1 minute, raised to Timeout whenever Timeout is larger, so a
	// caller that only sets Timeout keeps at least the inter-chunk budget the
	// previous single-timeout behavior gave it. With everything left at its
	// default that makes the effective idle budget 2 minutes.
	StreamIdleTimeout time.Duration

	MaxRequestBytes  int64
	MaxResponseBytes int64
	MaxEvents        int
	MaxLineBytes     int
	MaxAttempts      int
	InitialBackoff   time.Duration
	MaxBackoff       time.Duration
	// MaxRetryAfter caps how long a server-supplied Retry-After header is
	// honoured. MaxBackoff only governs the adapter's own exponential backoff:
	// clamping an explicit Retry-After down to it retries earlier than the
	// server allowed and compounds the rate limit. Default: 1 minute, never
	// below MaxBackoff.
	MaxRetryAfter time.Duration
	Organization  string
	Project       string
	ExtraHeaders  map[string]string
}

const (
	// defaultTimeout covers connect + request + first response byte. Two
	// minutes is generous for a queued request on a loaded provider while
	// still failing fast on a black-holed endpoint.
	defaultTimeout = 2 * time.Minute
	// defaultStreamIdleTimeout is the maximum silence tolerated between two
	// chunks. One minute comfortably exceeds the pauses a reasoning model
	// takes between emitted events, and is short enough to notice a dropped
	// connection that never produces a TCP-level error.
	defaultStreamIdleTimeout = time.Minute
	// defaultMaxRetryAfter is the ceiling for an explicit Retry-After. One
	// minute honours realistic rate-limit hints without letting a hostile or
	// misconfigured server park a request indefinitely.
	defaultMaxRetryAfter    = time.Minute
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
	if c.StreamIdleTimeout <= 0 {
		c.StreamIdleTimeout = defaultStreamIdleTimeout
		// Backward compatibility: a caller that only raised Timeout used to get
		// that much slack between chunks as well. Never fail a stream the old
		// single-timeout behavior would have allowed.
		if c.Timeout > c.StreamIdleTimeout {
			c.StreamIdleTimeout = c.Timeout
		}
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
	if c.MaxRetryAfter <= 0 {
		c.MaxRetryAfter = defaultMaxRetryAfter
	}
	if c.MaxRetryAfter < c.MaxBackoff {
		// A server hint must never shorten the wait below the backoff the
		// adapter would have taken on its own.
		c.MaxRetryAfter = c.MaxBackoff
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
