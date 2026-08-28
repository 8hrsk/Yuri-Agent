// Package antigravity defines the fail-closed boundary for a future official
// Antigravity integration. It intentionally contains no OAuth, token-cache,
// browser-cookie, or network implementation.
package antigravity

import (
	"context"
	"errors"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

const (
	ProviderID                   = "antigravity"
	ErrorCodeUnsupportedAuthMode = "unsupported_auth_mode"
	AlternativeOpenAICompatible  = "openai-compatible-api-key"
)

var ErrUnsupportedAuthMode = errors.New("antigravity: unsupported auth mode")

// Availability is safe provider metadata exposed to application/UI layers.
// It never contains endpoints, client identifiers, credentials, or tokens.
type Availability struct {
	Available   bool   `json:"available"`
	ErrorCode   string `json:"errorCode"`
	Message     string `json:"message"`
	Alternative string `json:"alternative"`
}

func Status() Availability {
	return Availability{
		Available:   false,
		ErrorCode:   ErrorCodeUnsupportedAuthMode,
		Message:     "Antigravity OAuth недоступен: официальный разрешённый contract для стороннего приложения отсутствует. Используйте официальный API key через совместимый endpoint либо официальный клиент отдельно от Yuri.",
		Alternative: AlternativeOpenAICompatible,
	}
}

// UnsupportedAuthModeError is stable and machine-readable without exposing
// vendor payloads. errors.Is(err, ErrUnsupportedAuthMode) remains available
// to non-UI callers.
type UnsupportedAuthModeError struct {
	availability Availability
}

func NewUnsupportedAuthModeError() *UnsupportedAuthModeError {
	return &UnsupportedAuthModeError{availability: Status()}
}

func (err *UnsupportedAuthModeError) Error() string {
	if err == nil {
		return ErrUnsupportedAuthMode.Error()
	}
	return err.availability.Message
}

func (*UnsupportedAuthModeError) Unwrap() error { return ErrUnsupportedAuthMode }

func (err *UnsupportedAuthModeError) Code() string {
	if err == nil {
		return ErrorCodeUnsupportedAuthMode
	}
	return err.availability.ErrorCode
}

func (err *UnsupportedAuthModeError) Alternative() string {
	if err == nil {
		return AlternativeOpenAICompatible
	}
	return err.availability.Alternative
}

// DisabledBackend implements the inference port only to make accidental use
// fail explicitly. It performs no I/O and cannot start an OAuth flow.
type DisabledBackend struct{}

var _ agent.ModelBackend = DisabledBackend{}

func (DisabledBackend) Start(context.Context, agent.ModelRequest) (agent.ModelStream, error) {
	return nil, NewUnsupportedAuthModeError()
}
