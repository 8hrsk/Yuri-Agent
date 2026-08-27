package observability

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestLoggerRedactsSecretsAndAddsCorrelationID(t *testing.T) {
	var output bytes.Buffer
	logger := NewLogger(LoggerOptions{Level: slog.LevelDebug, Format: "json", Output: &output})
	ctx := WithCorrelationID(context.Background(), "run-123")
	logger.InfoContext(ctx, "provider connected", "api_key", "never-log-me", "provider", "test")

	value := output.String()
	if strings.Contains(value, "never-log-me") {
		t.Fatalf("secret leaked in log: %s", value)
	}
	if !strings.Contains(value, "[REDACTED]") || !strings.Contains(value, "run-123") {
		t.Fatalf("redaction or correlation id missing: %s", value)
	}
}
