package agent

import "errors"

var (
	ErrInvalidRequest   = errors.New("agent: invalid request")
	ErrBudgetExceeded   = errors.New("agent: run budget exceeded")
	ErrApprovalRequired = errors.New("agent: tool approval required")
	ErrToolNotFound     = errors.New("agent: tool not found")
	ErrToolArguments    = errors.New("agent: invalid tool arguments")
	ErrBackend          = errors.New("agent: backend error")
	// ErrModelCapabilityUnsupported is returned before an inference request is
	// sent when a provider has authoritative catalog metadata saying that the
	// selected model cannot satisfy a required capability (currently tools).
	// An unknown/manual model ID must not be mapped to this error: gateways may
	// omit /models or expose private models that are still usable.
	ErrModelCapabilityUnsupported = errors.New("agent: selected model does not support required capability")
)

// ModelCapabilityError identifies a known incompatibility between a selected
// model and a capability required by the run. It deliberately carries only
// bounded non-secret identifiers so callers can show an actionable message
// without leaking provider credentials or catalog payloads.
type ModelCapabilityError struct {
	Model      string
	Capability string
}

func (err *ModelCapabilityError) Error() string {
	if err == nil {
		return ErrModelCapabilityUnsupported.Error()
	}
	if err.Model == "" || err.Capability == "" {
		return ErrModelCapabilityUnsupported.Error()
	}
	return "agent: model " + err.Model + " does not support required capability " + err.Capability
}

func (err *ModelCapabilityError) Unwrap() error { return ErrModelCapabilityUnsupported }
