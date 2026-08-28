package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

type scriptedBackend struct {
	mu       sync.Mutex
	requests []ModelRequest
	streams  [][]ModelEvent
}

func (b *scriptedBackend) Start(_ context.Context, request ModelRequest) (ModelStream, error) {
	b.mu.Lock()
	b.requests = append(b.requests, request)
	index := len(b.requests) - 1
	b.mu.Unlock()
	if index >= len(b.streams) {
		return nil, errors.New("no scripted stream")
	}
	return &scriptedStream{events: b.streams[index]}, nil
}

type scriptedStream struct {
	events []ModelEvent
	index  int
}

func (s *scriptedStream) Recv(ctx context.Context) (ModelEvent, error) {
	select {
	case <-ctx.Done():
		return ModelEvent{}, ctx.Err()
	default:
	}
	if s.index >= len(s.events) {
		return ModelEvent{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *scriptedStream) Close() error { return nil }

type echoTool struct {
	calls int
}

type approvalAwareTestTool struct {
	directCalls   int
	approvedCalls int
}

func (t *approvalAwareTestTool) Descriptor() ToolDescriptor {
	return ToolDescriptor{
		Name: "approval-aware", Risk: domain.RiskMedium,
		InputSchema:  json.RawMessage(`{"type":"object"}`),
		Capabilities: domain.CapabilitySet{domain.CapabilityFilesystemWrite},
	}
}

func (t *approvalAwareTestTool) Execute(context.Context, ToolCall) (ToolResult, error) {
	t.directCalls++
	return ToolResult{Content: "direct"}, nil
}

func (t *approvalAwareTestTool) ExecuteApproved(context.Context, ToolCall) (ToolResult, error) {
	t.approvedCalls++
	return ToolResult{Content: "approved"}, nil
}

func (t *echoTool) Descriptor() ToolDescriptor {
	return ToolDescriptor{
		Name: "echo", Description: "echo input", Risk: domain.RiskLow,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`),
	}
}

func (t *echoTool) Execute(_ context.Context, call ToolCall) (ToolResult, error) {
	t.calls++
	return ToolResult{Content: string(call.Arguments)}, nil
}

func TestRuntimeRunsToolLoopAndKeepsToolResultsInNextRequest(t *testing.T) {
	backend := &scriptedBackend{streams: [][]ModelEvent{
		{
			{Type: ModelEventTextDelta, Delta: "I will check. "},
			{Type: ModelEventToolCallStarted, ToolCallID: "call_1", ToolName: "echo"},
			{Type: ModelEventToolCallDelta, ToolCallID: "call_1", ArgumentsDelta: `{"value":"hello"}`},
			{Type: ModelEventCompleted},
		},
		{{Type: ModelEventTextDelta, Delta: "Done."}, {Type: ModelEventCompleted}},
	}}
	registry := NewToolRegistry()
	echo := &echoTool{}
	if err := registry.Register(echo); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(backend, registry)
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	result, err := runtime.Run(context.Background(), RunRequest{
		RunID:        domain.ID("run_test"),
		ModelRequest: ModelRequest{Model: "test-model", Messages: []Message{{Role: RoleUser, Content: "please echo"}}},
		Budget:       domain.RunBudget{MaxSteps: 3, MaxTokens: 100, MaxToolOutputBytes: 1000, MaxDurationSeconds: 2},
		Sink: func(_ context.Context, event Event) error {
			events = append(events, event)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if result.Message.Content != "Done." || result.Steps != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if echo.calls != 1 {
		t.Fatalf("tool calls = %d, want 1", echo.calls)
	}
	if len(backend.requests) != 2 {
		t.Fatalf("backend requests = %d, want 2", len(backend.requests))
	}
	last := backend.requests[1].Messages
	if len(last) != 3 || last[2].Role != RoleTool || last[2].ToolCallID != "call_1" {
		t.Fatalf("tool result was not appended: %#v", last)
	}
	if events[len(events)-1].Type != EventRunCompleted {
		t.Fatalf("last event = %s, want %s", events[len(events)-1].Type, EventRunCompleted)
	}
}

func TestRuntimeAppliesAuthorizationAndApprovalBeforeExecute(t *testing.T) {
	backend := &scriptedBackend{streams: [][]ModelEvent{{
		{Type: ModelEventToolCallStarted, ToolCallID: "call_1", ToolName: "echo", Arguments: `{"value":"safe"}`},
		{Type: ModelEventCompleted},
	}}}
	registry := NewToolRegistry()
	echo := &echoTool{}
	if err := registry.Register(echo); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(backend, registry)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Authorizer = authorizerFunc(func(context.Context, ToolAuthorizationRequest) (ToolAuthorizationResult, error) {
		return ToolAuthorizationResult{Decision: domain.PermissionNeedsApproval, Reason: "write access"}, nil
	})
	runtime.Approvals = approvalFunc(func(_ context.Context, request ApprovalRequest) (bool, error) {
		if request.Reason != "write access" {
			t.Fatalf("unexpected approval reason: %q", request.Reason)
		}
		return true, nil
	})
	_, err = runtime.Run(context.Background(), RunRequest{
		ModelRequest: ModelRequest{Model: "test-model", Messages: []Message{{Role: RoleUser, Content: "go"}}},
		Budget:       domain.RunBudget{MaxSteps: 1, MaxTokens: 100, MaxToolOutputBytes: 1000, MaxDurationSeconds: 2},
	})
	if err == nil {
		t.Fatal("expected max-step error after tool turn")
	}
	if !errors.Is(err, ErrBudgetExceeded) || echo.calls != 1 {
		t.Fatalf("unexpected error/calls: %v/%d", err, echo.calls)
	}
}

func TestRuntimeUsesApprovalAwareExecutionOnlyAfterApproval(t *testing.T) {
	backend := &scriptedBackend{streams: [][]ModelEvent{
		{
			{Type: ModelEventToolCallStarted, ToolCallID: "call_1", ToolName: "approval-aware", Arguments: `{}`},
			{Type: ModelEventCompleted},
		},
		{{Type: ModelEventTextDelta, Delta: "done"}, {Type: ModelEventCompleted}},
	}}
	registry := NewToolRegistry()
	tool := &approvalAwareTestTool{}
	if err := registry.Register(tool); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(backend, registry)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Authorizer = authorizerFunc(func(context.Context, ToolAuthorizationRequest) (ToolAuthorizationResult, error) {
		return ToolAuthorizationResult{Decision: domain.PermissionNeedsApproval, Reason: "write"}, nil
	})
	runtime.Approvals = approvalFunc(func(context.Context, ApprovalRequest) (bool, error) { return true, nil })
	if _, err := runtime.Run(context.Background(), RunRequest{
		RunID:        "run-approved",
		ModelRequest: ModelRequest{Model: "test-model", Messages: []Message{{Role: RoleUser, Content: "write"}}},
		Budget:       domain.RunBudget{MaxSteps: 2, MaxTokens: 100, MaxToolCalls: 1, MaxToolOutputBytes: 1000, MaxDurationSeconds: 2},
	}); err != nil {
		t.Fatal(err)
	}
	if tool.directCalls != 0 || tool.approvedCalls != 1 {
		t.Fatalf("direct/approved calls = %d/%d, want 0/1", tool.directCalls, tool.approvedCalls)
	}
}

func TestRuntimeRejectsToolBatchBeyondBudgetBeforeSideEffects(t *testing.T) {
	backend := &scriptedBackend{streams: [][]ModelEvent{{
		{Type: ModelEventToolCallStarted, ToolCallID: "call_1", ToolName: "echo", Arguments: `{"value":"one"}`},
		{Type: ModelEventToolCallStarted, ToolCallID: "call_2", ToolName: "echo", Arguments: `{"value":"two"}`},
		{Type: ModelEventCompleted},
	}}}
	registry := NewToolRegistry()
	echo := &echoTool{}
	if err := registry.Register(echo); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(backend, registry)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Run(context.Background(), RunRequest{
		ModelRequest: ModelRequest{Model: "test-model", Messages: []Message{{Role: RoleUser, Content: "run both"}}},
		Budget:       domain.RunBudget{MaxSteps: 2, MaxTokens: 100, MaxToolCalls: 1, MaxToolOutputBytes: 1000, MaxDurationSeconds: 2},
	})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("Run() error = %v, want ErrBudgetExceeded", err)
	}
	if echo.calls != 0 {
		t.Fatalf("tool calls = %d, want no partial side effects", echo.calls)
	}
}

func TestRuntimeStopsOnCancellation(t *testing.T) {
	backend := &blockingBackend{started: make(chan struct{})}
	runtime, err := NewRuntime(backend, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		_, runErr := runtime.Run(ctx, RunRequest{
			ModelRequest: ModelRequest{Model: "test-model", Messages: []Message{{Role: RoleUser, Content: "wait"}}},
			Budget:       domain.RunBudget{MaxSteps: 1, MaxTokens: 100, MaxToolOutputBytes: 1000, MaxDurationSeconds: 10},
		})
		finished <- runErr
	}()
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("backend was not called")
	}
	cancel()
	select {
	case runErr := <-finished:
		if !errors.Is(runErr, context.Canceled) {
			t.Fatalf("run error = %v, want cancellation", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop after cancellation")
	}
}

type authorizerFunc func(context.Context, ToolAuthorizationRequest) (ToolAuthorizationResult, error)

func (f authorizerFunc) Authorize(ctx context.Context, request ToolAuthorizationRequest) (ToolAuthorizationResult, error) {
	return f(ctx, request)
}

type approvalFunc func(context.Context, ApprovalRequest) (bool, error)

func (f approvalFunc) Approve(ctx context.Context, request ApprovalRequest) (bool, error) {
	return f(ctx, request)
}

type blockingBackend struct{ started chan struct{} }

func (b *blockingBackend) Start(ctx context.Context, _ ModelRequest) (ModelStream, error) {
	if b.started == nil {
		b.started = make(chan struct{})
	}
	select {
	case <-b.started:
	default:
		close(b.started)
	}
	return &blockingStream{ctx: ctx}, nil
}

type blockingStream struct{ ctx context.Context }

func (s *blockingStream) Recv(ctx context.Context) (ModelEvent, error) {
	select {
	case <-ctx.Done():
		return ModelEvent{}, ctx.Err()
	case <-s.ctx.Done():
		return ModelEvent{}, s.ctx.Err()
	}
}

func (s *blockingStream) Close() error { return nil }
