package antigravity

import (
	"context"
	"errors"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

func TestDisabledBackendReturnsStableUnsupportedContract(t *testing.T) {
	status := Status()
	if status.Available || status.ErrorCode != ErrorCodeUnsupportedAuthMode || status.Alternative != AlternativeOpenAICompatible {
		t.Fatalf("status = %#v", status)
	}
	stream, err := (DisabledBackend{}).Start(context.Background(), agent.ModelRequest{})
	if stream != nil || !errors.Is(err, ErrUnsupportedAuthMode) {
		t.Fatalf("Start() = %#v, %v", stream, err)
	}
	typed, ok := err.(*UnsupportedAuthModeError)
	if !ok || typed.Code() != ErrorCodeUnsupportedAuthMode || typed.Alternative() != AlternativeOpenAICompatible {
		t.Fatalf("typed error = %#v", err)
	}
}
