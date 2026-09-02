package googleaistudio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/providers/openai"
)

// ErrorReason distinguishes Google quota exhaustion from a rolling-window
// rate limit. RESOURCE_EXHAUSTED without a usable reason remains explicitly
// ambiguous so slow mode can apply a conservative cooldown rather than claim
// that the daily quota is known to be exhausted.
type ErrorReason string

const (
	ErrorReasonUnknown           ErrorReason = "unknown"
	ErrorReasonRateLimit         ErrorReason = "rate_limit_exceeded"
	ErrorReasonQuotaExhausted    ErrorReason = "quota_exceeded"
	ErrorReasonResourceExhausted ErrorReason = "resource_exhausted"
)

// Error is a sanitized Google API failure. Raw response bodies and the API
// key are intentionally not retained.
type Error struct {
	Operation  string
	StatusCode int
	Status     string
	Reason     ErrorReason
	Message    string
	RetryAfter time.Duration
	Retryable  bool
	cause      error
}

func (e *Error) Error() string {
	if e == nil {
		return "google ai studio: provider error"
	}
	op := e.Operation
	if op == "" {
		op = "request"
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("google ai studio: %s failed with HTTP %d: %s", op, e.StatusCode, e.Message)
	}
	if e.Message == "" {
		return fmt.Sprintf("google ai studio: %s failed", op)
	}
	return fmt.Sprintf("google ai studio: %s failed: %s", op, e.Message)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *Error) InferenceFailure() agent.InferenceFailure {
	if e == nil {
		return agent.InferenceFailure{Kind: domain.RunFailureUnknown}
	}
	message := strings.ToLower(e.Message)
	switch {
	case e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden || strings.Contains(message, "api key not valid") || strings.Contains(message, "permission denied"):
		return agent.InferenceFailure{Kind: domain.RunFailureAuthentication}
	case e.Reason == ErrorReasonQuotaExhausted || strings.Contains(message, "quota exceeded") || strings.Contains(message, "daily limit"):
		return agent.InferenceFailure{Kind: domain.RunFailureQuotaExhausted}
	case e.Reason == ErrorReasonRateLimit || e.Reason == ErrorReasonResourceExhausted || e.StatusCode == http.StatusTooManyRequests:
		return agent.InferenceFailure{Kind: domain.RunFailureRateLimit, Retryable: true, RetryAfter: e.RetryAfter}
	case e.StatusCode == http.StatusRequestTimeout || e.StatusCode == http.StatusGatewayTimeout:
		return agent.InferenceFailure{Kind: domain.RunFailureTimeout, Retryable: true, RetryAfter: e.RetryAfter}
	case e.StatusCode == http.StatusNotFound || strings.Contains(message, "model not found"):
		return agent.InferenceFailure{Kind: domain.RunFailureModelUnavailable}
	case e.StatusCode == http.StatusBadRequest || e.StatusCode == http.StatusUnprocessableEntity:
		return agent.InferenceFailure{Kind: domain.RunFailureInvalidRequest}
	case e.StatusCode >= 500 || e.Retryable:
		return agent.InferenceFailure{Kind: domain.RunFailureTransient, Retryable: true, RetryAfter: e.RetryAfter}
	default:
		return agent.InferenceFailure{Kind: domain.RunFailureUnknown, Retryable: e.Retryable, RetryAfter: e.RetryAfter}
	}
}

// ParseError converts a Google JSON error body into typed, redacted metadata.
// It is exported for a slow-mode coordinator which needs to react differently
// to known daily exhaustion and a short rolling-window rejection.
func ParseError(operation string, statusCode int, body []byte, header http.Header, apiKey string) *Error {
	retryAfter := parseRetryAfter(header.Get("Retry-After"))
	var payload struct {
		Error struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Status  string          `json:"status"`
			Details json.RawMessage `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)
	if payload.Error.Code > 0 && statusCode == 0 {
		statusCode = payload.Error.Code
	}
	message := payload.Error.Message
	if message == "" {
		message = string(body)
	}
	status := strings.ToUpper(strings.TrimSpace(payload.Error.Status))
	reason := googleReason(status, payload.Error.Details, message)
	retryable := statusCode == http.StatusRequestTimeout || statusCode == http.StatusTooManyRequests || statusCode >= 500 || reason == ErrorReasonRateLimit || reason == ErrorReasonResourceExhausted
	return &Error{
		Operation: operation, StatusCode: statusCode, Status: status, Reason: reason,
		Message: sanitize(message, apiKey), RetryAfter: retryAfter, Retryable: retryable,
	}
}

func errorFromOpenAI(source *openai.ProviderError) *Error {
	if source == nil {
		return &Error{Operation: "start", Message: "provider error"}
	}
	// The compatibility transport deliberately exposes only the sanitized
	// message. Native calls retain richer ErrorInfo/QuotaFailure reasons; this
	// fallback makes a safe best-effort classification for streamed inference.
	reason := googleReason("", nil, source.Message)
	return &Error{
		Operation: source.Operation, StatusCode: source.StatusCode, Reason: reason,
		Message: sanitize(source.Message), RetryAfter: source.RetryAfter,
		Retryable: source.Retryable,
		cause:     source,
	}
}

func googleReason(status string, rawDetails []byte, message string) ErrorReason {
	text := strings.ToUpper(status + " " + string(rawDetails) + " " + message)
	switch {
	case strings.Contains(text, "QUOTA_EXCEEDED"), strings.Contains(text, "DAILY") && strings.Contains(text, "QUOTA"):
		return ErrorReasonQuotaExhausted
	case strings.Contains(text, "RATE_LIMIT_EXCEEDED"), strings.Contains(text, "PER_MINUTE"), strings.Contains(text, "RATE LIMIT"):
		return ErrorReasonRateLimit
	case strings.Contains(text, "RESOURCE_EXHAUSTED"):
		return ErrorReasonResourceExhausted
	default:
		return ErrorReasonUnknown
	}
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
		if wait := time.Until(at); wait > 0 {
			return wait
		}
	}
	return 0
}

func nativeContextError(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

var (
	bearerPattern     = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]+`)
	keyPattern        = regexp.MustCompile(`(?i)(?:AIza|sk|rk)[A-Za-z0-9_-]{8,}`)
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
