package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
)

type ErrorKind string

const (
	ErrorKindRequest       ErrorKind = "request"
	ErrorKindHTTP          ErrorKind = "http"
	ErrorKindNetwork       ErrorKind = "network"
	ErrorKindDecode        ErrorKind = "decode"
	ErrorKindStream        ErrorKind = "stream"
	ErrorKindResponseLimit ErrorKind = "response_limit"
	// ErrorKindTimeout marks an abort by one of the adapter's own budgets:
	// the first-byte deadline or the stream idle deadline. It is distinct from
	// a caller cancellation, which keeps its context error.
	ErrorKindTimeout ErrorKind = "timeout"
)

// ProviderError deliberately contains status and a short sanitized message,
// never a raw response body or request URL containing credentials.
type ProviderError struct {
	Kind       ErrorKind
	StatusCode int
	Operation  string
	Message    string
	Retryable  bool
	RetryAfter time.Duration
}

func (e *ProviderError) Error() string {
	if e == nil {
		return "openai: provider error"
	}
	operation := e.Operation
	if operation == "" {
		operation = "request"
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("openai: %s failed with HTTP %d: %s", operation, e.StatusCode, e.Message)
	}
	if e.Message == "" {
		return fmt.Sprintf("openai: %s failed", operation)
	}
	return fmt.Sprintf("openai: %s failed: %s", operation, e.Message)
}

func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return nil
}

func (e *ProviderError) InferenceFailure() agent.InferenceFailure {
	if e == nil {
		return agent.InferenceFailure{Kind: domain.RunFailureUnknown}
	}
	message := strings.ToLower(e.Message)
	kind, retryable := domain.RunFailureUnknown, e.Retryable
	switch {
	case e.StatusCode == 401 || e.StatusCode == 403 || strings.Contains(message, "unauthorized") || strings.Contains(message, "invalid api key"):
		kind, retryable = domain.RunFailureAuthentication, false
	case e.StatusCode == 402 || strings.Contains(message, "insufficient credit") || strings.Contains(message, "insufficient balance") || strings.Contains(message, "quota") || strings.Contains(message, "usage limit"):
		kind, retryable = domain.RunFailureQuotaExhausted, false
	case e.StatusCode == 429 || strings.Contains(message, "rate limit") || strings.Contains(message, "too many requests"):
		kind, retryable = domain.RunFailureRateLimit, true
	case e.StatusCode == 413 || strings.Contains(message, "context length") || strings.Contains(message, "context window") || strings.Contains(message, "maximum context") || strings.Contains(message, "too many tokens"):
		kind, retryable = domain.RunFailureContextLimit, false
	case e.StatusCode == 404 || strings.Contains(message, "model not found") || strings.Contains(message, "unknown model") || strings.Contains(message, "no endpoints found") || strings.Contains(message, "model is unavailable"):
		kind, retryable = domain.RunFailureModelUnavailable, false
	case e.Kind == ErrorKindTimeout || e.StatusCode == 408:
		kind, retryable = domain.RunFailureTimeout, true
	case e.Kind == ErrorKindNetwork || e.StatusCode >= 500:
		kind, retryable = domain.RunFailureTransient, true
	case e.Kind == ErrorKindRequest || e.StatusCode == 400 || e.StatusCode == 422:
		kind, retryable = domain.RunFailureInvalidRequest, false
	}
	return agent.InferenceFailure{Kind: kind, Retryable: retryable, RetryAfter: e.RetryAfter}
}

func isRetryableStatus(status int) bool {
	return status == 408 || status == 409 || status == 425 || status == 429 || status >= 500
}

func providerError(kind ErrorKind, operation string, status int, message string, retryable bool, retryAfter time.Duration, secrets ...string) *ProviderError {
	return &ProviderError{
		Kind: kind, Operation: operation, StatusCode: status,
		Message: sanitize(message, secrets...), Retryable: retryable,
		RetryAfter: retryAfter,
	}
}

func parseErrorBody(body []byte, secrets ...string) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		if envelope.Error.Message != "" {
			return sanitize(envelope.Error.Message, secrets...)
		}
		if envelope.Message != "" {
			return sanitize(envelope.Message, secrets...)
		}
	}
	return sanitize(string(body), secrets...)
}

var (
	bearerPattern     = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]+`)
	keyPattern        = regexp.MustCompile(`(?i)(?:sk|rk)-[A-Za-z0-9_-]{8,}`)
	jsonSecretPattern = regexp.MustCompile(`(?i)("(?:api[_-]?key|token|secret|authorization)"\s*:\s*")[^"]+(")`)
)

func sanitize(value string, secrets ...string) string {
	value = strings.TrimSpace(value)
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	value = bearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = keyPattern.ReplaceAllString(value, "[REDACTED]")
	value = jsonSecretPattern.ReplaceAllString(value, `${1}[REDACTED]${2}`)
	if len(value) > 512 {
		value = value[:512] + "…"
	}
	if value == "" {
		return "provider returned an empty error"
	}
	return value
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
