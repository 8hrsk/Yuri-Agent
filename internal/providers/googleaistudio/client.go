package googleaistudio

import (
	"context"
	"errors"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/providers/openai"
)

// Client is a Gemini Developer API client. Inference is intentionally
// delegated to the existing, battle-tested OpenAI Chat Completions adapter;
// Google-specific REST operations remain in this package.
type Client struct {
	config        Config
	nativeBaseURL string
	transport     *openai.Client
}

func New(config Config) (*Client, error) {
	normalized, nativeBaseURL, compatibilityBaseURL, err := config.normalized()
	if err != nil {
		return nil, err
	}
	transport, err := openai.New(openai.Config{
		BaseURL:          compatibilityBaseURL,
		APIKey:           normalized.APIKey,
		Model:            normalized.Model,
		Style:            openai.APIStyleChatCompletions,
		HTTPClient:       normalized.HTTPClient,
		Timeout:          normalized.Timeout,
		MaxResponseBytes: normalized.MaxResponseBytes,
		// Slow mode accounts one upstream inference attempt per admission.
		// Hidden transport retries would consume extra RPM/RPD outside that
		// reservation and can amplify a Free Tier 429.
		MaxAttempts: 1,
		ExtraHeaders: map[string]string{
			"x-goog-api-client": normalized.clientHeader(),
		},
	})
	if err != nil {
		return nil, err
	}
	return &Client{config: normalized, nativeBaseURL: nativeBaseURL, transport: transport}, nil
}

// NewClient is an explicit alias consistent with the other provider
// adapters.
func NewClient(config Config) (*Client, error) { return New(config) }

// NewBackend is convenient for composition roots that should expose only the
// provider-neutral ModelBackend port. Call New when model discovery or exact
// countTokens support is also needed.
func NewBackend(config Config) (agent.ModelBackend, error) { return New(config) }

// Config returns a secret-free copy of the current adapter settings.
func (c *Client) Config() Config {
	if c == nil {
		return Config{}
	}
	copy := c.config
	copy.APIKey = ""
	return copy
}

func (c *Client) Capabilities() openai.Capabilities {
	if c == nil || c.transport == nil {
		return openai.Capabilities{}
	}
	return c.transport.Capabilities()
}

// Start satisfies agent.ModelBackend. The compatibility endpoint requires
// Chat Completions; Responses must never be selected for this provider.
func (c *Client) Start(ctx context.Context, request agent.ModelRequest) (agent.ModelStream, error) {
	if c == nil || c.transport == nil {
		return nil, &Error{Operation: "start", Message: "client is nil"}
	}
	if strings.TrimSpace(request.Model) == "" {
		request.Model = c.config.Model
	}
	// Metadata contains Yuri-local routing and pacing hints. Google's
	// compatibility surface does not document Chat Completions metadata, so
	// keep these control fields on the local side of the provider boundary.
	request.Metadata = nil
	stream, err := c.transport.Start(ctx, request)
	if err == nil {
		return stream, nil
	}
	var providerError *openai.ProviderError
	if errors.As(err, &providerError) {
		return nil, errorFromOpenAI(providerError)
	}
	return nil, err
}

// NativeBaseURL is intentionally secret-free. It is useful to the desktop
// bridge when displaying diagnostics but is not needed by normal callers.
func (c *Client) NativeBaseURL() string {
	if c == nil {
		return ""
	}
	return c.nativeBaseURL
}
