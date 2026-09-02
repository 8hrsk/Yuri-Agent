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

type approvedRunContextKey struct{}

func withApprovedRunID(ctx context.Context, runID domain.ID) context.Context {
	return context.WithValue(ctx, approvedRunContextKey{}, runID)
}

// ApprovedRunID identifies the durable approval scope attached to the current
// ExecuteApproved call. Tools can use it to bind a side effect to the exact
// approval record instead of re-deriving a broader scope after the owner click.
func ApprovedRunID(ctx context.Context) (domain.ID, bool) {
	runID, ok := ctx.Value(approvedRunContextKey{}).(domain.ID)
	return runID, ok && !runID.Empty()
}

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
	Role       Role          `json:"role"`
	Content    string        `json:"content,omitempty"`
	Parts      []ContentPart `json:"parts,omitempty"`
	Name       string        `json:"name,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
}

func (m Message) Valid() bool {
	if !m.Role.Valid() {
		return false
	}
	if m.Role == RoleTool && strings.TrimSpace(m.ToolCallID) == "" {
		return false
	}
	for _, part := range m.Parts {
		if !part.Valid() {
			return false
		}
	}
	return strings.TrimSpace(m.Content) != "" || len(m.Parts) > 0 || len(m.ToolCalls) > 0
}

type ContentPartType string

const (
	ContentPartImage ContentPartType = "image"
)

// ContentPart carries bounded non-text input without teaching the agent loop
// about any provider's request schema. Data is standard base64 and is populated
// only after the desktop boundary has validated and content-addressed the blob.
type ContentPart struct {
	Type      ContentPartType `json:"type"`
	Name      string          `json:"name,omitempty"`
	MediaType string          `json:"media_type"`
	Data      string          `json:"data"`
}

func (part ContentPart) Valid() bool {
	return part.Type == ContentPartImage && strings.HasPrefix(part.MediaType, "image/") && strings.TrimSpace(part.Data) != ""
}

// ToolCall is an intent emitted by a model. It is never an authorization to
// execute the action; the runtime still applies ToolAuthorizer immediately
// before invoking the registered tool.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	// ProviderExtras carries opaque, provider-issued tool-call metadata that
	// must be echoed on a continuation request. It is deliberately excluded
	// from Yuri's public/event JSON and is never interpreted by the runtime.
	// Gemini 3 thought signatures are the first such use.
	ProviderExtras json.RawMessage `json:"-"`
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
	ToolChoice      ToolChoice        `json:"tool_choice,omitempty"`
	MaxOutputTokens int64             `json:"max_output_tokens,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type ToolChoiceMode string

const (
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceRequired ToolChoiceMode = "required"
	ToolChoiceNone     ToolChoiceMode = "none"
)

// ToolChoice is provider-neutral. Required with an empty Name requires any
// available tool; Required with Name forces that exact tool until the model
// emits a tool call. Runtime resets it to auto after tool execution so an
// agent loop cannot be trapped into repeating the same side effect.
type ToolChoice struct {
	Mode ToolChoiceMode `json:"mode,omitempty"`
	Name string         `json:"name,omitempty"`
}

func (choice ToolChoice) Valid(tools []ToolDescriptor) error {
	if choice.Mode == "" {
		if strings.TrimSpace(choice.Name) != "" {
			return fmt.Errorf("%w: tool choice name requires a mode", ErrInvalidRequest)
		}
		return nil
	}
	if choice.Mode != ToolChoiceAuto && choice.Mode != ToolChoiceRequired && choice.Mode != ToolChoiceNone {
		return fmt.Errorf("%w: unsupported tool choice %q", ErrInvalidRequest, choice.Mode)
	}
	name := strings.TrimSpace(choice.Name)
	if name == "" {
		return nil
	}
	if choice.Mode != ToolChoiceRequired {
		return fmt.Errorf("%w: named tool choice must be required", ErrInvalidRequest)
	}
	for _, tool := range tools {
		if tool.Name == name {
			return nil
		}
	}
	return fmt.Errorf("%w: required tool %q is unavailable", ErrInvalidRequest, name)
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
	if err := r.ToolChoice.Valid(r.Tools); err != nil {
		return err
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
	// ToolCallProviderExtras is an opaque transport continuation token. The
	// runtime keeps it attached to the tool call without exposing it as model
	// reasoning or sending it to the desktop event stream.
	ToolCallProviderExtras json.RawMessage `json:"-"`
	FinishReason           string          `json:"finish_reason,omitempty"`
	Usage                  Usage           `json:"usage,omitempty"`
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
	Type        EventType             `json:"type"`
	RunID       domain.ID             `json:"run_id,omitempty"`
	Step        int                   `json:"step,omitempty"`
	ResponseID  string                `json:"response_id,omitempty"`
	Text        string                `json:"text,omitempty"`
	Status      RunStatus             `json:"status,omitempty"`
	ToolCall    *ToolCall             `json:"tool_call,omitempty"`
	ToolResult  *ToolResult           `json:"tool_result,omitempty"`
	Usage       Usage                 `json:"usage,omitempty"`
	Error       string                `json:"error,omitempty"`
	FailureInfo domain.RunFailureInfo `json:"failure_info,omitempty"`
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
