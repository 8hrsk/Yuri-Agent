package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type ErrorKind string

const (
	ErrorKindRequest       ErrorKind = "request"
	ErrorKindHTTP          ErrorKind = "http"
	ErrorKindNetwork       ErrorKind = "network"
	ErrorKindDecode        ErrorKind = "decode"
	ErrorKindStream        ErrorKind = "stream"
	ErrorKindResponseLimit ErrorKind = "response_limit"
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
