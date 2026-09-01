package openai

import (
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestProviderErrorMapsToProviderNeutralInferenceFailures(t *testing.T) {
	tests := []struct {
		name      string
		err       *ProviderError
		kind      domain.RunFailureKind
		retryable bool
	}{
		{"authentication", &ProviderError{StatusCode: 401, Message: "unauthorized", Retryable: true}, domain.RunFailureAuthentication, false},
		{"quota before rate", &ProviderError{StatusCode: 429, Message: "quota exceeded"}, domain.RunFailureQuotaExhausted, false},
		{"rate limit", &ProviderError{StatusCode: 429, Message: "too many requests", RetryAfter: 7 * time.Second}, domain.RunFailureRateLimit, true},
		{"context", &ProviderError{StatusCode: 400, Message: "maximum context length exceeded"}, domain.RunFailureContextLimit, false},
		{"model", &ProviderError{StatusCode: 404, Message: "model not found"}, domain.RunFailureModelUnavailable, false},
		{"timeout", &ProviderError{Kind: ErrorKindTimeout}, domain.RunFailureTimeout, true},
		{"transient", &ProviderError{StatusCode: 503}, domain.RunFailureTransient, true},
		{"invalid request", &ProviderError{Kind: ErrorKindRequest}, domain.RunFailureInvalidRequest, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failure := test.err.InferenceFailure()
			if failure.Kind != test.kind || failure.Retryable != test.retryable {
				t.Fatalf("failure = %#v, want kind=%s retryable=%v", failure, test.kind, test.retryable)
			}
			if test.err.RetryAfter > 0 && failure.RetryAfter != test.err.RetryAfter {
				t.Fatalf("retry after = %v", failure.RetryAfter)
			}
		})
	}
}
