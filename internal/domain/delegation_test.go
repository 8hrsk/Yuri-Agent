package domain

import (
	"errors"
	"testing"
	"time"
)

func TestDelegationLifecycleSupportsTwoPhaseCancellation(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	delegation, err := NewDelegation("delegation-1", "child-1", "agent-1", "parent-1", `{}`, "call-1", "sha256:abc", now)
	if err != nil {
		t.Fatal(err)
	}
	delegation.Budget = RunBudget{MaxSteps: 1, MaxTokens: 100, MaxToolCalls: 1, MaxToolOutputBytes: 1024, MaxDurationSeconds: 10}
	for _, status := range []DelegationStatus{DelegationStatusQueued, DelegationStatusRunning, DelegationStatusCancelling, DelegationStatusCancelled} {
		now = now.Add(time.Second)
		if err := delegation.Transition(status, now); err != nil {
			t.Fatalf("Transition(%s) error=%v", status, err)
		}
	}
	if !delegation.Status.Terminal() || delegation.FinishedAt.IsZero() {
		t.Fatalf("terminal delegation=%#v", delegation)
	}
	if err := delegation.Transition(DelegationStatusRunning, now.Add(time.Second)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal transition error=%v", err)
	}
}

func TestDelegationRejectsIdentityAndUnboundedPayloadShapes(t *testing.T) {
	now := time.Now().UTC()
	delegation, err := NewDelegation("delegation-1", "child-1", "agent-1", "parent-1", `{}`, "call-1", "sha256:abc", now)
	if err != nil {
		t.Fatal(err)
	}
	delegation.Depth = 2
	if err := delegation.Validate(); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("depth validation error=%v", err)
	}
	delegation.Depth = 1
	delegation.ResultText = "result before completion"
	if err := delegation.Validate(); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("early result validation error=%v", err)
	}
}
