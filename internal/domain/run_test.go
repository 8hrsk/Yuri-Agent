package domain

import (
	"errors"
	"testing"
	"time"
)

func TestRunLifecycle(t *testing.T) {
	base := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	run, err := NewRun("run-1", RunKindInteractive, "conversation-1", base)
	if err != nil {
		t.Fatalf("NewRun() error = %v", err)
	}

	transitions := []struct {
		state RunState
		at    time.Time
	}{
		{RunStateQueued, base.Add(time.Second)},
		{RunStateRunning, base.Add(2 * time.Second)},
		{RunStateCompleted, base.Add(3 * time.Second)},
	}
	for _, transition := range transitions {
		if err := run.Transition(transition.state, transition.at); err != nil {
			t.Fatalf("Transition(%s) error = %v", transition.state, err)
		}
	}
	if !run.IsTerminal() || run.FinishedAt != base.Add(3*time.Second) {
		t.Fatalf("terminal run = %#v", run)
	}
	if run.StartedAt != base.Add(2*time.Second) {
		t.Fatalf("StartedAt = %v", run.StartedAt)
	}
	if err := run.Transition(RunStateRunning, base.Add(4*time.Second)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal transition error = %v, want ErrInvalidTransition", err)
	}
}

func TestRunApprovalAndCancellationTransitions(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	run, err := NewRun("run-1", RunKindBackground, "conversation-1", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []RunState{RunStateQueued, RunStateRunning, RunStateWaitingApproval, RunStateRunning, RunStateCancelling, RunStateCancelled} {
		now = now.Add(time.Second)
		if err := run.Transition(state, now); err != nil {
			t.Fatalf("Transition(%s) error = %v", state, err)
		}
	}
	if run.State != RunStateCancelled {
		t.Fatalf("state = %s", run.State)
	}
}

func TestRunRejectsInvalidInputs(t *testing.T) {
	now := time.Now()
	if _, err := NewRun("", RunKindInteractive, "conversation-1", now); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty id error = %v", err)
	}
	if _, err := NewRun("run-1", RunKind("unknown"), "conversation-1", now); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unknown kind error = %v", err)
	}
	if _, err := NewRun("run-1", RunKindInteractive, "conversation-1", time.Time{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("zero time error = %v", err)
	}
}

func TestRunForAgentCapturesOwnerAndKeepsLegacyConstructor(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	run, err := NewRunForAgent("agent-a", "run-agent-a", RunKindInteractive, "conversation-a", now)
	if err != nil {
		t.Fatalf("NewRunForAgent() error = %v", err)
	}
	if run.AgentID != "agent-a" {
		t.Fatalf("AgentID = %q, want agent-a", run.AgentID)
	}
	legacy, err := NewRun("run-legacy", RunKindBackground, "conversation-a", now)
	if err != nil {
		t.Fatalf("legacy NewRun() error = %v", err)
	}
	if !legacy.AgentID.Empty() {
		t.Fatalf("legacy NewRun() AgentID = %q, want empty for adapter inference", legacy.AgentID)
	}
	if _, err := NewRunForAgent("", "run-agent-a", RunKindInteractive, "conversation-a", now); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty agent error = %v, want ErrInvalidArgument", err)
	}
}

func TestRunShapeBoundsAnonymousSubagents(t *testing.T) {
	now := time.Now().UTC()
	child, err := NewRunForAgent("agent-a", "child", RunKindSubagent, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.ValidateShape(); err == nil {
		t.Fatal("subagent without parent unexpectedly passed shape validation")
	}
	child.ParentRunID = "parent"
	if err := child.ValidateShape(); err != nil {
		t.Fatal(err)
	}
	child.ConversationID = "conversation"
	if err := child.ValidateShape(); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("subagent conversation shape error = %v", err)
	}
	root, err := NewRunForAgent("agent-a", "root", RunKindBackground, "", now)
	if err != nil {
		t.Fatal(err)
	}
	root.ParentRunID = "parent"
	if err := root.ValidateShape(); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("root parent shape error = %v", err)
	}
}
