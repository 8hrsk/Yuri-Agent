// Package googleaistudio implements the Gemini Developer API (Google AI
// Studio) adapter.  It deliberately uses Google's documented OpenAI
// compatibility endpoint for inference, while model discovery and token
// counting use the native Gemini REST endpoints.
package googleaistudio

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is Google's documented OpenAI-compatible Gemini endpoint.
	// It is an endpoint prefix, not a request URL and must remain free of a
	// query string so an API key can never end up in a URL or a log line.
	DefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai/"

	defaultClientVersion   = "0.1"
	defaultTimeout         = 2 * time.Minute
	defaultMaxResponseSize = int64(16 * 1024 * 1024)
)

var clientVersionPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Config contains adapter-local settings. APIKey is sent only as an HTTP
// header and is never included in Config returned by Client.Config.
type Config struct {
	APIKey           string
	Model            string
	HTTPClient       *http.Client
	Timeout          time.Duration
	MaxResponseBytes int64

	// ClientVersion becomes the required x-goog-api-client value
	// "ordoai-yuri/<version>". It is intentionally not an arbitrary header.
	ClientVersion string

	// TestBaseURL replaces the fixed production endpoint only in controlled
	// tests (normally an httptest.Server URL plus /v1beta/openai). Production
	// wiring must leave this empty. Keeping this override visibly test-scoped
	// prevents a user-editable endpoint from weakening the provider boundary.
	TestBaseURL string
}

func (c Config) normalized() (Config, string, string, error) {
	compatibilityBaseURL := strings.TrimSpace(c.TestBaseURL)
	if compatibilityBaseURL == "" {
		compatibilityBaseURL = DefaultBaseURL
	}
	u, err := url.Parse(compatibilityBaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return Config{}, "", "", fmt.Errorf("google ai studio: invalid base URL")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return Config{}, "", "", fmt.Errorf("google ai studio: base URL must use http or https")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Config{}, "", "", fmt.Errorf("google ai studio: base URL must not contain credentials, query, or fragment")
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	const suffix = "/v1beta/openai"
	if !strings.HasSuffix(path, suffix) {
		return Config{}, "", "", fmt.Errorf("google ai studio: base URL must end in %s", suffix)
	}
	// Check both decoded and escaped forms: url.URL keeps an encoded dot
	// segment in RawPath, while Path contains the decoded value. Constructing
	// final endpoints only after this check keeps joins canonical.
	if strings.Contains(u.Path, "..") || strings.Contains(strings.ToLower(path), "%2e") {
		return Config{}, "", "", fmt.Errorf("google ai studio: base URL must not contain path traversal")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	compatibilityBaseURL = strings.TrimRight(u.String(), "/") + "/"
	native := strings.TrimSuffix(compatibilityBaseURL, "openai/")
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{}
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.MaxResponseBytes <= 0 {
		c.MaxResponseBytes = defaultMaxResponseSize
	}
	if c.ClientVersion == "" {
		c.ClientVersion = defaultClientVersion
	}
	if !clientVersionPattern.MatchString(c.ClientVersion) {
		return Config{}, "", "", fmt.Errorf("google ai studio: invalid client version")
	}
	return c, native, compatibilityBaseURL, nil
}

func (c Config) clientHeader() string { return "ordoai-yuri/" + c.ClientVersion }
