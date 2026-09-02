package slowmode

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrInvalidConfig      = errors.New("slow mode: invalid config")
	ErrInvalidRequest     = errors.New("slow mode: invalid request")
	ErrImpossibleRequest  = errors.New("slow mode: request exceeds quota envelope")
	ErrDailyQuota         = errors.New("slow mode: daily quota exhausted")
	ErrInteractiveReserve = errors.New("slow mode: interactive daily reserve reached")
)

// AdmissionError is safe, actionable rejection metadata.
type AdmissionError struct {
	Kind      error
	Dimension string
	Requested int64
	Limit     int64
	ResetAt   time.Time
}

func (err *AdmissionError) Error() string {
	if err == nil || err.Kind == nil {
		return ErrInvalidRequest.Error()
	}
	if !err.ResetAt.IsZero() {
		return fmt.Sprintf("%v; resets at %s", err.Kind, err.ResetAt.Format(time.RFC3339))
	}
	if err.Dimension != "" {
		return fmt.Sprintf("%v: %s request=%d limit=%d", err.Kind, err.Dimension, err.Requested, err.Limit)
	}
	return err.Kind.Error()
}

func (err *AdmissionError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Kind
}
