package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

// Client implements agent.ModelBackend for a single OpenAI-compatible
// endpoint. Responses is the default/primary API style; Chat Completions is
// available for compatible servers that do not expose /responses.
type Client struct {
	config Config
}

func New(config Config) (*Client, error) {
	normalized, err := config.normalized()
	if err != nil {
		return nil, err
	}
	return &Client{config: normalized}, nil
}

// NewClient is an explicit alias useful to callers that prefer constructor
// names matching other provider adapters.
func NewClient(config Config) (*Client, error) { return New(config) }

func (c *Client) Config() Config {
	if c == nil {
		return Config{}
	}
	copyConfig := c.config
	// Do not expose the credential through an introspection helper. The client
	// retains it only to construct the Authorization header at the boundary.
	copyConfig.APIKey = ""
	if c.config.ExtraHeaders != nil {
		copyConfig.ExtraHeaders = make(map[string]string, len(c.config.ExtraHeaders))
		for key, value := range c.config.ExtraHeaders {
			copyConfig.ExtraHeaders[key] = value
		}
	}
	return copyConfig
}

type Capabilities struct {
	Responses       bool
	ChatCompletions bool
	Streaming       bool
	Tools           bool
	Vision          bool
	JSONSchema      bool
}

func (c *Client) Capabilities() Capabilities {
	return Capabilities{
		Responses: true, ChatCompletions: true, Streaming: true,
		Tools: true, Vision: true, JSONSchema: true,
	}
}

func (c *Client) Start(ctx context.Context, request agent.ModelRequest) (agent.ModelStream, error) {
	if c == nil {
		return nil, providerError(ErrorKindRequest, "start", 0, "client is nil", false, 0)
	}
	if ctx == nil {
		return nil, providerError(ErrorKindRequest, "start", 0, "context is nil", false, 0)
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.Model) == "" {
		request.Model = c.config.Model
	}
	if err := request.Valid(); err != nil {
		return nil, providerError(ErrorKindRequest, "start", 0, err.Error(), false, 0)
	}
	if err := c.validateRequiredCapabilities(ctx, request); err != nil {
		return nil, err
	}
	body, style, err := c.marshalRequest(request)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > c.config.MaxRequestBytes {
		return nil, providerError(ErrorKindRequest, "start", 0, fmt.Sprintf("request exceeds %d bytes", c.config.MaxRequestBytes), false, 0)
	}

	// The request context must outlive the request itself because the SSE body
	// is read through it. A plain deadline would therefore kill a healthy long
	// generation mid-stream, so the budget is split: Timeout covers connect,
	// request, and first byte; StreamIdleTimeout covers the silence between
	// chunks afterwards and is re-armed by every byte received.
	streamCtx, cancel := context.WithCancel(ctx)
	deadline := newStreamDeadline(cancel, c.config.Timeout, c.config.StreamIdleTimeout)
	response, err := c.do(streamCtx, style, body)
	if err != nil {
		deadline.stop()
		cancel()
		// A caller-side cancellation keeps its context error so the runtime can
		// still tell an interrupted run from a provider timeout.
		if contextError(ctx) == nil && deadline.expired() {
			return nil, deadline.err()
		}
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		deadline.stop()
		cancel()
		defer response.Body.Close()
		message, readErr := readErrorBody(response.Body, c.config.MaxResponseBytes)
		if readErr != nil {
			message = readErr.Error()
		}
		retryAfter := parseRetryAfter(response.Header.Get("Retry-After"))
		return nil, providerError(ErrorKindHTTP, "start", response.StatusCode, message, isRetryableStatus(response.StatusCode), retryAfter, c.config.APIKey)
	}

	// From here the deadline owns the context: every byte read re-arms the idle
	// budget, and stream.Close calls release, which both disarms the watchdog
	// and cancels the context on every exit path.
	response.Body = &activityBody{body: response.Body, deadline: deadline}
	if isEventStream(response.Header.Get("Content-Type")) {
		return newSSEStream(response, deadline.release, style, c.config.MaxResponseBytes, c.config.MaxEvents, c.config.MaxLineBytes, c.config.APIKey), nil
	}
	// A number of OpenAI-compatible gateways ignore stream=true and return a
	// normal JSON response. Convert it to the same normalized stream contract
	// instead of leaking a decoder detail to the runtime.
	return newJSONStream(response, deadline.release, style, c.config.MaxResponseBytes, c.config.APIKey), nil
}

func (c *Client) marshalRequest(request agent.ModelRequest) ([]byte, APIStyle, error) {
	style := c.config.Style
	var payload any
	switch style {
	case APIStyleResponses:
		payload = responsesRequest{Model: request.Model, Input: responsesInput(request.Messages), Tools: responsesTools(request.Tools), ToolChoice: responsesToolChoice(request.ToolChoice), Stream: true, MaxOutputTokens: request.MaxOutputTokens, Temperature: request.Temperature, Metadata: request.Metadata}
	case APIStyleChatCompletions:
		payload = chatRequest{Model: request.Model, Messages: chatMessages(request.Messages), Tools: chatTools(request.Tools), ToolChoice: chatToolChoice(request.ToolChoice), Stream: true, StreamOptions: &chatStreamOptions{IncludeUsage: true}, MaxTokens: request.MaxOutputTokens, Temperature: request.Temperature, Metadata: request.Metadata}
	default:
		return nil, "", providerError(ErrorKindRequest, "marshal", 0, "unsupported API style", false, 0)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", providerError(ErrorKindRequest, "marshal", 0, err.Error(), false, 0)
	}
	return body, style, nil
}

func (c *Client) do(ctx context.Context, style APIStyle, body []byte) (*http.Response, error) {
	path := "/responses"
	if style == APIStyleChatCompletions {
		path = "/chat/completions"
	}
	requestURL := endpoint(c.config.BaseURL, path)
	var lastErr error
	for attempt := 1; attempt <= c.config.MaxAttempts; attempt++ {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
		if err != nil {
			return nil, providerError(ErrorKindRequest, "request", 0, err.Error(), false, 0)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream, application/json")
		req.Header.Set("User-Agent", "yuri-agent/0.1")
		if c.config.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
		}
		if c.config.Organization != "" {
			req.Header.Set("OpenAI-Organization", c.config.Organization)
		}
		if c.config.Project != "" {
			req.Header.Set("OpenAI-Project", c.config.Project)
		}
		for key, value := range c.config.ExtraHeaders {
			if !sensitiveHeader(key) {
				req.Header.Set(key, value)
			}
		}
		response, err := c.config.HTTPClient.Do(req)
		if err == nil {
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return response, nil
			}
			if !isRetryableStatus(response.StatusCode) || attempt == c.config.MaxAttempts {
				return response, nil
			}
			// Consume and close a retryable error response before waiting, so
			// the transport can reuse its connection safely.
			_, _ = readErrorBody(response.Body, c.config.MaxResponseBytes)
			_ = response.Body.Close()
			retryAfter := parseRetryAfter(response.Header.Get("Retry-After"))
			if err := waitBackoff(ctx, attempt, c.config, retryAfter); err != nil {
				return nil, err
			}
			continue
		}
		if isContextError(err) {
			return nil, err
		}
		lastErr = err
		if attempt == c.config.MaxAttempts {
			break
		}
		if err := waitBackoff(ctx, attempt, c.config, 0); err != nil {
			return nil, err
		}
	}
	return nil, providerError(ErrorKindNetwork, "request", 0, lastErrString(lastErr), true, 0)
}

func waitBackoff(ctx context.Context, attempt int, config Config, retryAfter time.Duration) error {
	// An explicit Retry-After is a server instruction, not adapter policy: it is
	// capped by MaxRetryAfter rather than by MaxBackoff, because retrying before
	// the server allowed guarantees another rejection and burns an attempt.
	delay := retryAfter
	if ceiling := config.MaxRetryAfter; ceiling > 0 && delay > ceiling {
		delay = ceiling
	}
	if delay <= 0 {
		delay = config.InitialBackoff
		for i := 1; i < attempt; i++ {
			if delay >= config.MaxBackoff/2 {
				delay = config.MaxBackoff
				break
			}
			delay *= 2
		}
		if delay > config.MaxBackoff {
			delay = config.MaxBackoff
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func contextError(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func lastErrString(err error) string {
	if err == nil {
		return "request failed"
	}
	return err.Error()
}

func readErrorBody(reader io.Reader, maxBytes int64) (string, error) {
	if maxBytes <= 0 {
		maxBytes = defaultMaxResponseBytes
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if int64(len(body)) > maxBytes {
		return "", providerError(ErrorKindResponseLimit, "error response", 0, "error response exceeds configured limit", false, 0)
	}
	if err != nil {
		return "", err
	}
	return parseErrorBody(body), nil
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		delay := time.Until(at)
		if delay > 0 {
			return delay
		}
	}
	return 0
}

func isEventStream(value string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0])), "text/event-stream")
}

func sensitiveHeader(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return key == "authorization" || strings.Contains(key, "api-key") || strings.Contains(key, "token") || strings.Contains(key, "secret")
}
