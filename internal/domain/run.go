package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// RunKind identifies the source of work without coupling the lifecycle to a
// particular feature implementation. Reflection and scheduled work can use
// the same lifecycle as an interactive run.
type RunKind string

const (
	RunKindInteractive RunKind = "interactive"
	RunKindBackground  RunKind = "background"
	RunKindReflection  RunKind = "reflection"
	RunKindSubagent    RunKind = "subagent"
)

func (k RunKind) Valid() bool {
	switch k {
	case RunKindInteractive, RunKindBackground, RunKindReflection, RunKindSubagent:
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

// RunInferenceRoute is the non-secret provider/model identity captured before
// a model-backed run starts. It is historical provenance, not a live pointer
// to the agent profile: changing an agent's current route must never rewrite
// an existing run.
type RunInferenceRoute struct {
	ProviderID string `json:"provider_id,omitempty"`
	Model      string `json:"model,omitempty"`
}

func (r RunInferenceRoute) Valid() bool {
	providerID, model := strings.TrimSpace(r.ProviderID), strings.TrimSpace(r.Model)
	if providerID == "" {
		return model == ""
	}
	return utf8.RuneCountInString(providerID) <= 128 && utf8.RuneCountInString(model) <= 256 &&
		!strings.ContainsRune(providerID, '\x00') && !strings.ContainsRune(model, '\x00')
}

// RunUsage contains provider-reported token accounting. Zero is a valid
// value for providers which do not report usage.
type RunUsage struct {
	InputTokens  int64 `json:"input_tokens,omitempty"`
	OutputTokens int64 `json:"output_tokens,omitempty"`
	TotalTokens  int64 `json:"total_tokens,omitempty"`
}

// RunFailureKind is a stable, provider-neutral reason suitable for durable
// history and UI decisions. It deliberately carries no upstream response body.
type RunFailureKind string

const (
	RunFailureUnknown          RunFailureKind = "unknown"
	RunFailureAuthentication   RunFailureKind = "authentication"
	RunFailureRateLimit        RunFailureKind = "rate_limit"
	RunFailureQuotaExhausted   RunFailureKind = "quota_exhausted"
	RunFailureContextLimit     RunFailureKind = "context_limit"
	RunFailureModelUnavailable RunFailureKind = "model_unavailable"
	RunFailureTimeout          RunFailureKind = "timeout"
	RunFailureTransient        RunFailureKind = "transient"
	RunFailureInvalidRequest   RunFailureKind = "invalid_request"
	RunFailureBudgetExceeded   RunFailureKind = "budget_exceeded"
)

func (kind RunFailureKind) Valid() bool {
	switch kind {
	case "", RunFailureUnknown, RunFailureAuthentication, RunFailureRateLimit,
		RunFailureQuotaExhausted, RunFailureContextLimit, RunFailureModelUnavailable,
		RunFailureTimeout, RunFailureTransient, RunFailureInvalidRequest, RunFailureBudgetExceeded:
		return true
	default:
		return false
	}
}

// RunFailureInfo is safe operational metadata. RetryAfterSeconds is a bounded
// provider hint, not a promise that Yuri will retry automatically.
type RunFailureInfo struct {
	Kind              RunFailureKind `json:"kind,omitempty"`
	Retryable         bool           `json:"retryable,omitempty"`
	RetryAfterSeconds int64          `json:"retry_after_seconds,omitempty"`
}

func (info RunFailureInfo) Valid() bool {
	if !info.Kind.Valid() || info.RetryAfterSeconds < 0 || info.RetryAfterSeconds > 24*60*60 {
		return false
	}
	return info.Kind != "" || (!info.Retryable && info.RetryAfterSeconds == 0)
}

func (u RunUsage) Valid() bool {
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.TotalTokens < 0 {
		return false
	}
	if u.InputTokens > int64(^uint64(0)>>1)-u.OutputTokens {
		return false
	}
	return u.TotalTokens == 0 || u.TotalTokens >= u.InputTokens+u.OutputTokens
}

func (b RunBudget) Valid() bool {
	return b.MaxSteps >= 0 && b.MaxTokens >= 0 && b.MaxToolCalls >= 0 &&
		b.MaxToolOutputBytes >= 0 && b.MaxDurationSeconds >= 0
}

// AgentRun is the domain representation of an execution. Provider/model names
// are retained only as non-secret inference provenance; credentials, adapter
// configuration, tool, UI, and storage implementation details stay outside.
type AgentRun struct {
	ID ID `json:"id"`
	// AgentID identifies the named top-level agent that owns this execution.
	// It is intentionally optional in the legacy NewRun constructor so callers
	// that predate agent scoping can still construct a run; persistence adapters
	// resolve it from the owned conversation before writing the row.
	AgentID        ID                `json:"agent_id,omitempty"`
	Kind           RunKind           `json:"kind"`
	ConversationID ID                `json:"conversation_id"`
	ParentRunID    ID                `json:"parent_run_id,omitempty"`
	State          RunState          `json:"state"`
	Budget         RunBudget         `json:"budget"`
	Inference      RunInferenceRoute `json:"inference,omitempty"`
	// InitialInference preserves the primary route when an explicitly enabled
	// fallback replaces Inference. It lets durable history reconstruct the
	// visible hand-off without reading provider payloads or mutable settings.
	InitialInference RunInferenceRoute `json:"initial_inference,omitempty"`
	// InferenceRouteSwitches counts explicit, owner-configured fallback
	// switches made before a run produced visible output or a side effect.
	// The storage trigger permits at most one guarded switch while running;
	// ordinary saves cannot mutate an attributed route.
	InferenceRouteSwitches uint8          `json:"inference_route_switches,omitempty"`
	Usage                  RunUsage       `json:"usage,omitempty"`
	FailureInfo            RunFailureInfo `json:"failure_info,omitempty"`
	CreatedAt              time.Time      `json:"created_at"`
	UpdatedAt              time.Time      `json:"updated_at"`
	StartedAt              time.Time      `json:"started_at,omitempty"`
	FinishedAt             time.Time      `json:"finished_at,omitempty"`
	Failure                string         `json:"failure,omitempty"`
	Version                uint64         `json:"version"`
}

// SwitchInferenceRoute performs the one guarded route change allowed for an
// in-flight run. It deliberately does not change lifecycle state: the caller
// remains responsible for recording the provider failure and final outcome.
// Persistence adapters enforce the same invariant in SQLite so this method is
// not a substitute for optimistic concurrency.
func (r *AgentRun) SwitchInferenceRoute(route RunInferenceRoute, now time.Time) error {
	if r == nil {
		return fmt.Errorf("%w: nil run", ErrInvalidArgument)
	}
	if r.State != RunStateRunning {
		return fmt.Errorf("%w: inference route switch requires a running run", ErrInvalidTransition)
	}
	if !route.Valid() || strings.TrimSpace(route.ProviderID) == "" || strings.TrimSpace(route.Model) == "" {
		return fmt.Errorf("%w: invalid fallback inference route", ErrInvalidArgument)
	}
	if r.InferenceRouteSwitches >= 1 {
		return fmt.Errorf("%w: inference route may switch only once", ErrConflict)
	}
	if r.Usage != (RunUsage{}) || r.Failure != "" || r.FailureInfo != (RunFailureInfo{}) {
		return fmt.Errorf("%w: inference route cannot switch after usage or failure", ErrInvalidTransition)
	}
	if strings.TrimSpace(r.InitialInference.ProviderID) == "" {
		r.InitialInference = RunInferenceRoute{
			ProviderID: strings.TrimSpace(r.Inference.ProviderID),
			Model:      strings.TrimSpace(r.Inference.Model),
		}
	}
	if strings.TrimSpace(r.Inference.ProviderID) == strings.TrimSpace(route.ProviderID) && strings.TrimSpace(r.Inference.Model) == strings.TrimSpace(route.Model) {
		return fmt.Errorf("%w: fallback inference route must differ from current route", ErrInvalidArgument)
	}
	if now.IsZero() {
		return fmt.Errorf("%w: inference route switch timestamp is required", ErrInvalidArgument)
	}
	r.Inference = RunInferenceRoute{ProviderID: strings.TrimSpace(route.ProviderID), Model: strings.TrimSpace(route.Model)}
	r.InferenceRouteSwitches++
	r.UpdatedAt = now.UTC()
	r.Version++
	return nil
}

// ValidateShape enforces the bounded execution hierarchy used by the
// delegation layer. Anonymous subagents are always one level below a named
// root run and never own a conversation of their own.
func (r AgentRun) ValidateShape() error {
	if r.Kind == RunKindSubagent {
		if r.ConversationID != "" || r.ParentRunID.Empty() {
			return fmt.Errorf("%w: subagent runs require a parent and no conversation", ErrInvalidArgument)
		}
		return nil
	}
	if !r.ParentRunID.Empty() {
		return fmt.Errorf("%w: only subagent runs may have a parent", ErrInvalidArgument)
	}
	return nil
}

func NewRun(id ID, kind RunKind, conversationID ID, now time.Time) (AgentRun, error) {
	return newRun(id, "", kind, conversationID, now)
}

// NewRunForAgent constructs a run owned by one named top-level agent. The
// legacy NewRun constructor remains available for callers that do not yet
// carry agent context; storage adapters infer that context from the run's
// conversation when possible.
func NewRunForAgent(agentID ID, id ID, kind RunKind, conversationID ID, now time.Time) (AgentRun, error) {
	if agentID.Empty() {
		return AgentRun{}, fmt.Errorf("%w: agent id is required", ErrInvalidArgument)
	}
	return newRun(id, agentID, kind, conversationID, now)
}

func newRun(id ID, agentID ID, kind RunKind, conversationID ID, now time.Time) (AgentRun, error) {
	if id.Empty() || !kind.Valid() {
		return AgentRun{}, fmt.Errorf("%w: run id and kind are required", ErrInvalidArgument)
	}
	if now.IsZero() {
		return AgentRun{}, fmt.Errorf("%w: run timestamp is required", ErrInvalidArgument)
	}
	now = now.UTC()
	return AgentRun{
		ID:             id,
		AgentID:        agentID,
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
	return r.FailWithInfo(reason, RunFailureInfo{}, now)
}

func (r *AgentRun) FailWithInfo(reason string, info RunFailureInfo, now time.Time) error {
	if r == nil {
		return fmt.Errorf("%w: nil run", ErrInvalidArgument)
	}
	if reason == "" || !info.Valid() {
		return fmt.Errorf("%w: failure reason is required", ErrInvalidArgument)
	}
	if err := r.Transition(RunStateFailed, now); err != nil {
		return err
	}
	r.Failure = reason
	r.FailureInfo = info
	return nil
}

func (r AgentRun) IsTerminal() bool { return r.State.Terminal() }
