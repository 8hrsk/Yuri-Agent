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

type interactiveBackend struct {
	request ModelRequest
	stream  *interactiveStream
}

func (backend *interactiveBackend) Start(_ context.Context, request ModelRequest) (ModelStream, error) {
	backend.request = request
	backend.stream = &interactiveStream{events: []ModelEvent{
		{Type: ModelEventToolCallDone, ToolCallID: "dynamic-1", ToolName: "echo", Arguments: `{"value":"from codex"}`},
		{Type: ModelEventTextDelta, ResponseID: "message-after-tool", Delta: "Файл прочитан."},
		{Type: ModelEventCompleted},
	}}
	return backend.stream, nil
}

type interactiveStream struct {
	events    []ModelEvent
	index     int
	responses map[string]ToolResult
}

func (stream *interactiveStream) Recv(context.Context) (ModelEvent, error) {
	if stream.index >= len(stream.events) {
		return ModelEvent{}, io.EOF
	}
	if stream.index > 0 && len(stream.responses) == 0 {
		return ModelEvent{}, errors.New("provider resumed before tool result")
	}
	event := stream.events[stream.index]
	stream.index++
	return event, nil
}

func (stream *interactiveStream) RespondToolResult(_ context.Context, callID string, result ToolResult) error {
	if stream.responses == nil {
		stream.responses = make(map[string]ToolResult)
	}
	stream.responses[callID] = result
	return nil
}

func (*interactiveStream) Close() error { return nil }

type echoTool struct {
	calls int
}

type approvalAwareTestTool struct {
	directCalls   int
	approvedCalls int
	approvedRunID domain.ID
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

func (t *approvalAwareTestTool) ExecuteApproved(ctx context.Context, _ ToolCall) (ToolResult, error) {
	t.approvedCalls++
	t.approvedRunID, _ = ApprovedRunID(ctx)
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

func TestRuntimeRetriesRequiredToolInsteadOfAcceptingPromise(t *testing.T) {
	backend := &scriptedBackend{streams: [][]ModelEvent{
		{{Type: ModelEventTextDelta, Delta: "Сейчас попробую…"}, {Type: ModelEventCompleted}},
		{
			{Type: ModelEventToolCallStarted, ToolCallID: "call_required", ToolName: "echo"},
			{Type: ModelEventToolCallDelta, ToolCallID: "call_required", ArgumentsDelta: `{"value":"hello"}`},
			{Type: ModelEventCompleted},
		},
		{{Type: ModelEventTextDelta, Delta: "Готово."}, {Type: ModelEventCompleted}},
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
	result, err := runtime.Run(context.Background(), RunRequest{
		RunID: "required-tool-run",
		ModelRequest: ModelRequest{
			Model: "test-model", Messages: []Message{{Role: RoleUser, Content: "выполни действие"}},
			ToolChoice: ToolChoice{Mode: ToolChoiceRequired, Name: "echo"},
		},
		Budget: domain.RunBudget{MaxSteps: 4, MaxTokens: 100, MaxToolCalls: 2, MaxToolOutputBytes: 1000, MaxDurationSeconds: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Message.Content != "Готово." || echo.calls != 1 || len(backend.requests) != 3 {
		t.Fatalf("result=%#v echo=%d requests=%d", result, echo.calls, len(backend.requests))
	}
	if backend.requests[0].ToolChoice.Mode != ToolChoiceRequired || backend.requests[1].ToolChoice.Mode != ToolChoiceRequired {
		t.Fatalf("required choice was not preserved across corrective retry: %#v", backend.requests)
	}
	if backend.requests[2].ToolChoice.Mode != ToolChoiceAuto || backend.requests[2].ToolChoice.Name != "" {
		t.Fatalf("choice after tool execution = %#v, want auto", backend.requests[2].ToolChoice)
	}
	secondMessages := backend.requests[1].Messages
	if len(secondMessages) < 3 || secondMessages[len(secondMessages)-1].Role != RoleDeveloper {
		t.Fatalf("corrective developer message missing: %#v", secondMessages)
	}
}

func TestRuntimeExecutesInteractiveToolInsideOneProviderTurn(t *testing.T) {
	backend := &interactiveBackend{}
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
		RunID: "run-interactive",
		ModelRequest: ModelRequest{
			Model: "codex-default", Messages: []Message{{Role: RoleUser, Content: "прочитай файл"}},
		},
		Budget: domain.RunBudget{MaxSteps: 2, MaxTokens: 100, MaxToolCalls: 2, MaxToolOutputBytes: 1000, MaxDurationSeconds: 2},
		Sink: func(_ context.Context, event Event) error {
			events = append(events, event)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Message.Content != "Файл прочитан." || result.Steps != 1 || len(result.ToolCalls) != 1 || echo.calls != 1 {
		t.Fatalf("result = %#v, echo calls = %d", result, echo.calls)
	}
	response, ok := backend.stream.responses["dynamic-1"]
	if !ok || response.IsError || response.Content != `{"value":"from codex"}` {
		t.Fatalf("tool response = %#v", backend.stream.responses)
	}
	if len(backend.request.Tools) != 1 || backend.request.Tools[0].Name != "echo" {
		t.Fatalf("provider tools = %#v", backend.request.Tools)
	}
	want := []EventType{EventRunStarted, EventToolCallStarted, EventToolStarted, EventToolCompleted, EventModelTextDelta, EventRunCompleted}
	if len(events) != len(want) {
		t.Fatalf("events = %#v", events)
	}
	for index := range want {
		if events[index].Type != want[index] {
			t.Fatalf("event[%d] = %s, want %s", index, events[index].Type, want[index])
		}
	}
	if events[4].ResponseID != "message-after-tool" {
		t.Fatalf("text response id = %q", events[4].ResponseID)
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
	if tool.approvedRunID != "run-approved" {
		t.Fatalf("approved execution run id = %q", tool.approvedRunID)
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

// TestRuntimeDeliversTerminalEventAfterCancellation pins the fix for a run that
// the owner interrupts: emit() short-circuits on a cancelled context, so the
// sink never saw run.failed and the UI kept the partial assistant message in a
// streaming state forever. The terminal event must reach the sink, must say the
// run was cancelled, and must arrive on a context the sink can still use for
// its own persistence work.
func TestRuntimeDeliversTerminalEventAfterCancellation(t *testing.T) {
	backend := &blockingBackend{started: make(chan struct{})}
	runtime, err := NewRuntime(backend, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var terminal *Event
	var sinkCtxLive bool
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		_, runErr := runtime.Run(ctx, RunRequest{
			RunID:        "run-cancelled",
			ModelRequest: ModelRequest{Model: "test-model", Messages: []Message{{Role: RoleUser, Content: "wait"}}},
			Budget:       domain.RunBudget{MaxSteps: 1, MaxTokens: 100, MaxToolOutputBytes: 1000, MaxDurationSeconds: 10},
			Sink: func(sinkCtx context.Context, event Event) error {
				if event.Type != EventRunCompleted && event.Type != EventRunFailed {
					return nil
				}
				mu.Lock()
				copied := event
				terminal = &copied
				sinkCtxLive = sinkCtx.Err() == nil
				mu.Unlock()
				return nil
			},
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
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not stop after cancellation")
	}
	mu.Lock()
	defer mu.Unlock()
	if terminal == nil {
		t.Fatal("cancelled run delivered no terminal event to the sink")
	}
	if terminal.Type != EventRunFailed {
		t.Fatalf("terminal event = %#v, want run.failed", terminal)
	}
	if terminal.Status != RunStatusCancelled {
		t.Fatalf("terminal status = %q, want %q", terminal.Status, RunStatusCancelled)
	}
	if !sinkCtxLive {
		t.Fatal("sink received an already cancelled context and cannot persist the finalized run")
	}
}

// TestRuntimeTerminalEventReportsSuccessStatus keeps the successful path honest
// about its own status now that terminal events carry one.
func TestRuntimeTerminalEventReportsSuccessStatus(t *testing.T) {
	backend := &scriptedBackend{streams: [][]ModelEvent{{
		{Type: ModelEventTextDelta, Delta: "готово"},
		{Type: ModelEventCompleted},
	}}}
	runtime, err := NewRuntime(backend, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	var terminal *Event
	if _, err := runtime.Run(context.Background(), RunRequest{
		RunID:        "run-complete",
		ModelRequest: ModelRequest{Model: "test-model", Messages: []Message{{Role: RoleUser, Content: "привет"}}},
		Budget:       domain.RunBudget{MaxSteps: 1, MaxTokens: 100, MaxToolOutputBytes: 1000, MaxDurationSeconds: 10},
		Sink: func(_ context.Context, event Event) error {
			if event.Type == EventRunCompleted {
				copied := event
				terminal = &copied
			}
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if terminal == nil || terminal.Status != RunStatusCompleted {
		t.Fatalf("terminal event = %#v, want a completed status", terminal)
	}
}
