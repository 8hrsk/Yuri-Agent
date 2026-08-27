// Package observability configures structured, redacted application logging.
package observability

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

// LoggerOptions controls the process logger without exposing provider secrets.
type LoggerOptions struct {
	Level  slog.Level
	Format string
	Output io.Writer
}

// NewLogger builds a structured logger that redacts attributes with secret keys.
func NewLogger(options LoggerOptions) *slog.Logger {
	replace := func(_ []string, attribute slog.Attr) slog.Attr {
		if isSensitiveKey(attribute.Key) {
			return slog.String(attribute.Key, "[REDACTED]")
		}
		return attribute
	}
	handlerOptions := &slog.HandlerOptions{Level: options.Level, ReplaceAttr: replace}
	var handler slog.Handler
	if strings.EqualFold(options.Format, "json") {
		handler = slog.NewJSONHandler(options.Output, handlerOptions)
	} else {
		handler = slog.NewTextHandler(options.Output, handlerOptions)
	}
	return slog.New(&correlationHandler{Handler: handler})
}

type correlationKey struct{}

// WithCorrelationID propagates a stable run/request identifier through logs.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationKey{}, id)
}

type correlationHandler struct {
	slog.Handler
}

func (h *correlationHandler) Handle(ctx context.Context, record slog.Record) error {
	if id, ok := ctx.Value(correlationKey{}).(string); ok && id != "" {
		record.AddAttrs(slog.String("correlation_id", id))
	}
	return h.Handler.Handle(ctx, record)
}

func (h *correlationHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	return &correlationHandler{Handler: h.Handler.WithAttrs(attributes)}
}

func (h *correlationHandler) WithGroup(name string) slog.Handler {
	return &correlationHandler{Handler: h.Handler.WithGroup(name)}
}

func isSensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, fragment := range []string{"api_key", "access_token", "refresh_token", "authorization", "password", "secret", "cookie"} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}
