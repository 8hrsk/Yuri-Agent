package agent

import (
	"errors"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestInferenceFailureRetainsTypedMetadataWithoutLosingBackendIdentity(t *testing.T) {
	cause := errors.New("sanitized provider error")
	err := NewInferenceError(domain.RunFailureRateLimit, true, 2500*time.Millisecond, cause)
	if !errors.Is(err, ErrBackend) || !errors.Is(err, cause) {
		t.Fatalf("wrapped error identity = %v", err)
	}
	failure, ok := InferenceFailureFromError(WrapInferenceError(err))
	if !ok || failure.Kind != domain.RunFailureRateLimit || !failure.Retryable || failure.RetryAfter != 2500*time.Millisecond {
		t.Fatalf("failure = %#v, ok=%v", failure, ok)
	}
	if got := DurableFailureInfo(err); got != (domain.RunFailureInfo{Kind: domain.RunFailureRateLimit, Retryable: true, RetryAfterSeconds: 3}) {
		t.Fatalf("durable info = %#v", got)
	}
}

func TestInferenceFailureBoundsHintsAndClassifiesRuntimeBudgets(t *testing.T) {
	err := NewInferenceError(domain.RunFailureKind("not-stable"), true, 48*time.Hour, errors.New("bad"))
	failure, _ := InferenceFailureFromError(err)
	if failure.Kind != domain.RunFailureUnknown || failure.RetryAfter != 24*time.Hour {
		t.Fatalf("normalized failure = %#v", failure)
	}
	if got := DurableFailureInfo(ErrBudgetExceeded); got.Kind != domain.RunFailureBudgetExceeded || got.Retryable {
		t.Fatalf("budget failure = %#v", got)
	}
}

func TestModelCapabilityFailureIsProviderNeutralInvalidRequest(t *testing.T) {
	err := &ModelCapabilityError{Model: "vendor/text-only", Capability: "tools"}
	if !errors.Is(err, ErrModelCapabilityUnsupported) {
		t.Fatalf("capability error identity = %v", err)
	}
	failure, ok := InferenceFailureFromError(WrapInferenceError(err))
	if !ok || failure.Kind != domain.RunFailureInvalidRequest || failure.Retryable {
		t.Fatalf("failure = %#v, ok=%v", failure, ok)
	}
}
