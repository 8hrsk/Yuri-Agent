// Package agent contains the provider-neutral agent loop used by interactive
// and background runs. Provider adapters implement ModelBackend; the runtime
// owns tool execution, budgets, cancellation, and the event stream.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// Role is the role of a message sent to a model backend.
type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

func (r Role) Valid() bool {
	switch r {
	case RoleSystem, RoleDeveloper, RoleUser, RoleAssistant, RoleTool:
		return true
	default:
		return false
	}
}

// Message is the provider-neutral conversation representation. Content is
// deliberately text for the first vertical slice; richer content parts can
// be added without changing the backend or runtime contracts.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

func (m Message) Valid() bool {
	if !m.Role.Valid() {
		return false
	}
	if m.Role == RoleTool && strings.TrimSpace(m.ToolCallID) == "" {
		return false
	}
	return strings.TrimSpace(m.Content) != "" || len(m.ToolCalls) > 0
}

// ToolCall is an intent emitted by a model. It is never an authorization to
// execute the action; the runtime still applies ToolAuthorizer immediately
// before invoking the registered tool.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (c ToolCall) Valid() bool {
	return strings.TrimSpace(c.ID) != "" && strings.TrimSpace(c.Name) != "" && len(c.Arguments) > 0
}

// ToolDescriptor is the model-visible contract for one local tool.
type ToolDescriptor struct {
	Name         string               `json:"name"`
	Description  string               `json:"description,omitempty"`
	InputSchema  json.RawMessage      `json:"input_schema"`
	Risk         domain.RiskLevel     `json:"risk"`
	Capabilities domain.CapabilitySet `json:"capabilities,omitempty"`
}

func (d ToolDescriptor) Valid() bool {
	if strings.TrimSpace(d.Name) == "" || !d.Risk.Valid() || len(d.InputSchema) == 0 || !json.Valid(d.InputSchema) {
		return false
	}
	if err := d.Capabilities.Validate(); err != nil {
		return false
	}
	return true
}

// Tool is implemented by local built-in tools and, later, by plugin-backed
// tools. The runtime provides a bounded context to Execute.
type Tool interface {
	Descriptor() ToolDescriptor
	Execute(context.Context, ToolCall) (ToolResult, error)
}

// ApprovalAwareTool keeps the durable approval decision attached to the
// execution path without exposing it as model-controlled JSON. Runtime calls
// ExecuteApproved only after ApprovalHandler returned true; direct Execute
// remains fail-closed for tools that enforce approval internally.
type ApprovalAwareTool interface {
	Tool
	ExecuteApproved(context.Context, ToolCall) (ToolResult, error)
}

// ToolResult is the safe, model-visible result of a tool invocation.
type ToolResult struct {
	Content  string         `json:"content"`
	IsError  bool           `json:"is_error,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Usage is provider-reported token accounting. A provider may leave fields at
// zero when its endpoint does not return usage in streaming mode.
type Usage struct {
	InputTokens  int64 `json:"input_tokens,omitempty"`
	OutputTokens int64 `json:"output_tokens,omitempty"`
	TotalTokens  int64 `json:"total_tokens,omitempty"`
}

func (u Usage) Add(other Usage) Usage {
	return Usage{
		InputTokens:  u.InputTokens + other.InputTokens,
		OutputTokens: u.OutputTokens + other.OutputTokens,
		TotalTokens:  u.TotalTokens + other.TotalTokens,
	}
}

// ModelRequest is the provider-neutral request passed to an inference
// backend. Metadata is for correlation and non-secret provider hints; API
// credentials must never be placed here.
type ModelRequest struct {
	Model           string            `json:"model"`
	Messages        []Message         `json:"messages"`
	Tools           []ToolDescriptor  `json:"tools,omitempty"`
	MaxOutputTokens int64             `json:"max_output_tokens,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

func (r ModelRequest) Valid() error {
	if strings.TrimSpace(r.Model) == "" {
		return fmt.Errorf("%w: model is required", ErrInvalidRequest)
	}
	if len(r.Messages) == 0 {
		return fmt.Errorf("%w: at least one message is required", ErrInvalidRequest)
	}
	for i, message := range r.Messages {
		if !message.Valid() {
			return fmt.Errorf("%w: invalid message at index %d", ErrInvalidRequest, i)
		}
	}
	seen := make(map[string]struct{}, len(r.Tools))
	for _, tool := range r.Tools {
		if !tool.Valid() {
			return fmt.Errorf("%w: invalid tool %q", ErrInvalidRequest, tool.Name)
		}
		if _, ok := seen[tool.Name]; ok {
			return fmt.Errorf("%w: duplicate tool %q", ErrInvalidRequest, tool.Name)
		}
		seen[tool.Name] = struct{}{}
	}
	if r.MaxOutputTokens < 0 {
		return fmt.Errorf("%w: max output tokens must not be negative", ErrInvalidRequest)
	}
	if r.Temperature != nil && (*r.Temperature < 0 || *r.Temperature > 2) {
		return fmt.Errorf("%w: temperature must be between 0 and 2", ErrInvalidRequest)
	}
	return nil
}

// ModelEventType is a normalized event vocabulary shared by all model
// backends. It intentionally exposes output/tool lifecycle, not hidden model
// reasoning.
type ModelEventType string

const (
	ModelEventStarted         ModelEventType = "started"
	ModelEventTextDelta       ModelEventType = "text_delta"
	ModelEventToolCallStarted ModelEventType = "tool_call_started"
	ModelEventToolCallDelta   ModelEventType = "tool_call_delta"
	ModelEventToolCallDone    ModelEventType = "tool_call_done"
	ModelEventCompleted       ModelEventType = "completed"
)

// ModelEvent is one item from a streaming backend.
type ModelEvent struct {
	Type           ModelEventType `json:"type"`
	ResponseID     string         `json:"response_id,omitempty"`
	Delta          string         `json:"delta,omitempty"`
	ToolCallID     string         `json:"tool_call_id,omitempty"`
	ToolName       string         `json:"tool_name,omitempty"`
	Arguments      string         `json:"arguments,omitempty"`
	ArgumentsDelta string         `json:"arguments_delta,omitempty"`
	FinishReason   string         `json:"finish_reason,omitempty"`
	Usage          Usage          `json:"usage,omitempty"`
}

// ModelStream is intentionally small so an official harness adapter can
// expose the same cancellation and event boundary as a raw model adapter.
type ModelStream interface {
	Recv(context.Context) (ModelEvent, error)
	Close() error
}

// InteractiveToolStream is implemented by model transports that can receive
// a tool result while the model turn is still in progress. This is useful for
// harnesses such as the Codex app server, where a dynamic tool call is sent as
// a server request and the same turn waits for the client response. The
// runtime remains responsible for validating, authorizing, and executing the
// tool before returning its bounded result to the transport.
type InteractiveToolStream interface {
	ModelStream
	RespondToolResult(context.Context, string, ToolResult) error
}

// ModelBackend is the provider-neutral port used by Runtime. Start must not
// execute local tools; it only asks an inference backend for model output.
type ModelBackend interface {
	Start(context.Context, ModelRequest) (ModelStream, error)
}

// ToolAuthorizer makes the policy check immediately before a side effect.
// Implementations can bridge this request to domain.PolicyEngine and the
// application approval service without importing provider code.
type ToolAuthorizationRequest struct {
	RunID  domain.ID
	Tool   ToolDescriptor
	Call   ToolCall
	Action string
}

type ToolAuthorizationResult struct {
	Decision domain.PermissionDecision
	Reason   string
}

type ToolAuthorizer interface {
	Authorize(context.Context, ToolAuthorizationRequest) (ToolAuthorizationResult, error)
}

// ApprovalHandler resolves a medium/high-risk operation when the policy asks
// for an approval. A handler may block while the UI waits for a user decision.
type ApprovalRequest struct {
	RunID  domain.ID
	Tool   ToolDescriptor
	Call   ToolCall
	Action string
	Reason string
}

type ApprovalHandler interface {
	Approve(context.Context, ApprovalRequest) (bool, error)
}

// EventType is the runtime event vocabulary consumed by UI and audit
// adapters. Events contain only redacted tool arguments/results supplied by
// the runtime; hidden chain-of-thought is never emitted.
type EventType string

const (
	EventRunStarted         EventType = "run.started"
	EventModelTextDelta     EventType = "model.text_delta"
	EventToolCallStarted    EventType = "tool.call_started"
	EventToolApprovalNeeded EventType = "tool.approval_needed"
	EventToolStarted        EventType = "tool.started"
	EventToolCompleted      EventType = "tool.completed"
	EventRunCompleted       EventType = "run.completed"
	EventRunFailed          EventType = "run.failed"
)

// RunStatus qualifies a terminal run event. A cancelled run must never be
// reported to a sink as a plain failure: the UI has to finalize the partial
// assistant message differently for an interruption than for an error.
type RunStatus string

const (
	RunStatusCompleted RunStatus = "completed"
	RunStatusFailed    RunStatus = "failed"
	RunStatusCancelled RunStatus = "cancelled"
)

type Event struct {
	Type       EventType   `json:"type"`
	RunID      domain.ID   `json:"run_id,omitempty"`
	Step       int         `json:"step,omitempty"`
	ResponseID string      `json:"response_id,omitempty"`
	Text       string      `json:"text,omitempty"`
	Status     RunStatus   `json:"status,omitempty"`
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
	Usage      Usage       `json:"usage,omitempty"`
	Error      string      `json:"error,omitempty"`
}

type EventSink func(context.Context, Event) error

// RunRequest describes one foreground/background agent execution.
type RunRequest struct {
	RunID          domain.ID
	ConversationID domain.ID
	ModelRequest   ModelRequest
	Budget         domain.RunBudget
	Sink           EventSink
}

type RunResult struct {
	Message   Message
	Steps     int
	Usage     Usage
	ToolCalls []ToolCall
}
