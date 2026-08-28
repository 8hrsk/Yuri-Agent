package domain

import (
	"fmt"
	"time"
)

// RunKind identifies the source of work without coupling the lifecycle to a
// particular feature implementation. Reflection and scheduled work can use
// the same lifecycle as an interactive run.
type RunKind string

const (
	RunKindInteractive RunKind = "interactive"
	RunKindBackground  RunKind = "background"
	RunKindReflection  RunKind = "reflection"
)

func (k RunKind) Valid() bool {
	switch k {
	case RunKindInteractive, RunKindBackground, RunKindReflection:
		return true
	default:
		return false
	}
}

// RunState is the durable lifecycle of one agent execution.
type RunState string

const (
	RunStateCreated         RunState = "created"
	RunStateQueued          RunState = "queued"
	RunStateRunning         RunState = "running"
	RunStateWaitingApproval RunState = "waiting_approval"
	RunStateCancelling      RunState = "cancelling"
	RunStateCompleted       RunState = "completed"
	RunStateFailed          RunState = "failed"
	RunStateCancelled       RunState = "cancelled"
)

func (s RunState) Terminal() bool {
	switch s {
	case RunStateCompleted, RunStateFailed, RunStateCancelled:
		return true
	default:
		return false
	}
}

func (s RunState) Valid() bool {
	switch s {
	case RunStateCreated, RunStateQueued, RunStateRunning,
		RunStateWaitingApproval, RunStateCancelling, RunStateCompleted,
		RunStateFailed, RunStateCancelled:
		return true
	default:
		return false
	}
}

// RunBudget carries limits that the runtime will enforce. The domain only
// stores the values; it does not execute model or tool calls.
type RunBudget struct {
	MaxSteps           int   `json:"max_steps"`
	MaxTokens          int64 `json:"max_tokens"`
	MaxToolCalls       int   `json:"max_tool_calls"`
	MaxToolOutputBytes int64 `json:"max_tool_output_bytes"`
	MaxDurationSeconds int   `json:"max_duration_seconds"`
}

func (b RunBudget) Valid() bool {
	return b.MaxSteps >= 0 && b.MaxTokens >= 0 && b.MaxToolCalls >= 0 &&
		b.MaxToolOutputBytes >= 0 && b.MaxDurationSeconds >= 0
}

// AgentRun is the domain representation of an execution. It deliberately
// contains no provider, tool, UI, or storage implementation details.
type AgentRun struct {
	ID             ID        `json:"id"`
	Kind           RunKind   `json:"kind"`
	ConversationID ID        `json:"conversation_id"`
	ParentRunID    ID        `json:"parent_run_id,omitempty"`
	State          RunState  `json:"state"`
	Budget         RunBudget `json:"budget"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	FinishedAt     time.Time `json:"finished_at,omitempty"`
	Failure        string    `json:"failure,omitempty"`
	Version        uint64    `json:"version"`
}

func NewRun(id ID, kind RunKind, conversationID ID, now time.Time) (AgentRun, error) {
	if id.Empty() || !kind.Valid() {
		return AgentRun{}, fmt.Errorf("%w: run id and kind are required", ErrInvalidArgument)
	}
	if now.IsZero() {
		return AgentRun{}, fmt.Errorf("%w: run timestamp is required", ErrInvalidArgument)
	}
	now = now.UTC()
	return AgentRun{
		ID:             id,
		Kind:           kind,
		ConversationID: conversationID,
		State:          RunStateCreated,
		CreatedAt:      now,
		UpdatedAt:      now,
		Version:        1,
	}, nil
}

// CanTransition is kept pure so UI, workers, and tests can validate a
// requested state change before mutating durable state.
func (r AgentRun) CanTransition(next RunState) bool {
	if !next.Valid() || r.State.Terminal() {
		return false
	}
	switch r.State {
	case RunStateCreated:
		return next == RunStateQueued || next == RunStateCancelling || next == RunStateCancelled
	case RunStateQueued:
		return next == RunStateRunning || next == RunStateCancelling || next == RunStateCancelled
	case RunStateRunning:
		return next == RunStateWaitingApproval || next == RunStateCancelling || next == RunStateCompleted || next == RunStateFailed
	case RunStateWaitingApproval:
		return next == RunStateRunning || next == RunStateCancelling || next == RunStateCancelled || next == RunStateFailed
	case RunStateCancelling:
		return next == RunStateCancelled || next == RunStateFailed
	default:
		return false
	}
}

// Transition applies a lifecycle transition and updates timestamps. It does
// not execute or cancel external work; the worker owns those side effects.
func (r *AgentRun) Transition(next RunState, now time.Time) error {
	if r == nil {
		return fmt.Errorf("%w: nil run", ErrInvalidArgument)
	}
	if now.IsZero() {
		return fmt.Errorf("%w: transition timestamp is required", ErrInvalidArgument)
	}
	if !r.CanTransition(next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, r.State, next)
	}
	now = now.UTC()
	r.State = next
	r.UpdatedAt = now
	r.Version++
	if next == RunStateRunning && r.StartedAt.IsZero() {
		r.StartedAt = now
	}
	if next.Terminal() {
		r.FinishedAt = now
	}
	return nil
}

func (r *AgentRun) Fail(reason string, now time.Time) error {
	if r == nil {
		return fmt.Errorf("%w: nil run", ErrInvalidArgument)
	}
	if reason == "" {
		return fmt.Errorf("%w: failure reason is required", ErrInvalidArgument)
	}
	if err := r.Transition(RunStateFailed, now); err != nil {
		return err
	}
	r.Failure = reason
	return nil
}

func (r AgentRun) IsTerminal() bool { return r.State.Terminal() }
