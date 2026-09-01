package agent

import (
	"errors"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// InferenceFailure is provider-neutral operational metadata. Adapter errors
// may implement inferenceFailureCarrier to retain classification through the
// runtime without exposing provider payloads.
type InferenceFailure struct {
	Kind       domain.RunFailureKind
	Retryable  bool
	RetryAfter time.Duration
}

type inferenceFailureCarrier interface {
	InferenceFailure() InferenceFailure
}

type inferenceError struct {
	failure InferenceFailure
	cause   error
}

func (err *inferenceError) Error() string {
	if err == nil || err.cause == nil {
		return ErrBackend.Error()
	}
	return err.cause.Error()
}

func (err *inferenceError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

func (err *inferenceError) Is(target error) bool { return target == ErrBackend }

func (err *inferenceError) InferenceFailure() InferenceFailure {
	if err == nil {
		return InferenceFailure{Kind: domain.RunFailureUnknown}
	}
	return err.failure
}

// WrapInferenceError marks an adapter error as a backend failure while
// retaining only typed classification alongside its already-sanitized cause.
func WrapInferenceError(cause error) error {
	if cause == nil {
		cause = ErrBackend
	}
	failure, ok := InferenceFailureFromError(cause)
	if !ok {
		failure = InferenceFailure{Kind: domain.RunFailureUnknown}
	}
	return &inferenceError{failure: normalizeInferenceFailure(failure), cause: cause}
}

func NewInferenceError(kind domain.RunFailureKind, retryable bool, retryAfter time.Duration, cause error) error {
	if cause == nil {
		cause = ErrBackend
	}
	return &inferenceError{failure: normalizeInferenceFailure(InferenceFailure{Kind: kind, Retryable: retryable, RetryAfter: retryAfter}), cause: cause}
}

func InferenceFailureFromError(err error) (InferenceFailure, bool) {
	if err == nil {
		return InferenceFailure{}, false
	}
	var carrier inferenceFailureCarrier
	if errors.As(err, &carrier) {
		return normalizeInferenceFailure(carrier.InferenceFailure()), true
	}
	if errors.Is(err, ErrBudgetExceeded) {
		return InferenceFailure{Kind: domain.RunFailureBudgetExceeded}, true
	}
	if errors.Is(err, ErrBackend) {
		return InferenceFailure{Kind: domain.RunFailureUnknown}, true
	}
	return InferenceFailure{}, false
}

func normalizeInferenceFailure(failure InferenceFailure) InferenceFailure {
	if !failure.Kind.Valid() || failure.Kind == "" {
		failure.Kind = domain.RunFailureUnknown
	}
	if failure.RetryAfter < 0 {
		failure.RetryAfter = 0
	}
	if failure.RetryAfter > 24*time.Hour {
		failure.RetryAfter = 24 * time.Hour
	}
	return failure
}

func DurableFailureInfo(err error) domain.RunFailureInfo {
	failure, ok := InferenceFailureFromError(err)
	if !ok {
		return domain.RunFailureInfo{}
	}
	seconds := int64(0)
	if failure.RetryAfter > 0 {
		seconds = int64(failure.RetryAfter.Round(time.Second) / time.Second)
		if seconds == 0 {
			seconds = 1
		}
	}
	return domain.RunFailureInfo{Kind: failure.Kind, Retryable: failure.Retryable, RetryAfterSeconds: seconds}
}
