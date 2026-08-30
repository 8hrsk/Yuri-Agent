package desktop

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// gateInteractiveBackend mimics the Codex app-server transport shape: the first
// turn keeps the stream open and only continues once the runtime answers the
// tool request through RespondToolResult. Every later (nested) turn returns a
// plain, non-interactive stream so the wrapper is exercised in both modes.
type gateInteractiveBackend struct {
	toolName   string
	arguments  string
	nestedText string

	mu     sync.Mutex
	starts int
}

func (backend *gateInteractiveBackend) Start(_ context.Context, _ agent.ModelRequest) (agent.ModelStream, error) {
	backend.mu.Lock()
	backend.starts++
	first := backend.starts == 1
	backend.mu.Unlock()
	if !first || backend.toolName == "" {
		text := backend.nestedText
		if text == "" {
			text = "вложенный результат"
		}
		return &gateTextStream{text: text}, nil
	}
	return &gateInteractiveStream{
		toolName: backend.toolName, arguments: backend.arguments,
		answered: make(chan agent.ToolResult, 1),
	}, nil
}

type gateInteractiveStream struct {
	toolName  string
	arguments string
	answered  chan agent.ToolResult
	step      int
}

func (stream *gateInteractiveStream) Recv(ctx context.Context) (agent.ModelEvent, error) {
	stream.step++
	switch stream.step {
	case 1:
		return agent.ModelEvent{
			Type: agent.ModelEventToolCallDone, ToolCallID: "gate-call-1",
			ToolName: stream.toolName, Arguments: stream.arguments,
		}, nil
	case 2:
		select {
		case result := <-stream.answered:
			return agent.ModelEvent{Type: agent.ModelEventTextDelta, Delta: "инструмент вернул: " + result.Content}, nil
		case <-ctx.Done():
			return agent.ModelEvent{}, ctx.Err()
		}
	case 3:
		return agent.ModelEvent{Type: agent.ModelEventCompleted}, nil
	default:
		return agent.ModelEvent{}, io.EOF
	}
}

func (stream *gateInteractiveStream) Close() error { return nil }

func (stream *gateInteractiveStream) RespondToolResult(_ context.Context, _ string, result agent.ToolResult) error {
	select {
	case stream.answered <- result:
		return nil
	default:
		return io.ErrClosedPipe
	}
}

var _ agent.InteractiveToolStream = (*gateInteractiveStream)(nil)

type gateTextStream struct {
	text string
	step int
}

func (stream *gateTextStream) Recv(context.Context) (agent.ModelEvent, error) {
	stream.step++
	switch stream.step {
	case 1:
		return agent.ModelEvent{Type: agent.ModelEventTextDelta, Delta: stream.text}, nil
	case 2:
		return agent.ModelEvent{Type: agent.ModelEventCompleted}, nil
	default:
		return agent.ModelEvent{}, io.EOF
	}
}

func (stream *gateTextStream) Close() error { return nil }

type gateEchoTool struct{}

func (gateEchoTool) Descriptor() agent.ToolDescriptor {
	return agent.ToolDescriptor{
		Name: "gate.echo", Description: "echo", Risk: domain.RiskLow,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`),
	}
}

func (gateEchoTool) Execute(_ context.Context, call agent.ToolCall) (agent.ToolResult, error) {
	var input struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(call.Arguments, &input); err != nil {
		return agent.ToolResult{}, err
	}
	return agent.ToolResult{Content: input.Value}, nil
}

// TestGatedStreamKeepsInteractiveToolStreamCapability pins the wrapper defect
// that silently disabled every Codex dynamic tool call: gatedStream embedded the
// agent.ModelStream interface, so the wrapped Codex stream no longer satisfied
// agent.InteractiveToolStream and the runtime fell back to the non-interactive
// path where Close() runs before tools execute.
func TestGatedStreamKeepsInteractiveToolStreamCapability(t *testing.T) {
	backend := gatedBackend{
		backend: &gateInteractiveBackend{toolName: "gate.echo", arguments: `{"value":"ping"}`},
		turns:   make(chan struct{}, 1),
	}
	request := agent.ModelRequest{Model: "test", Messages: []agent.Message{{Role: agent.RoleUser, Content: "тест"}}}
	stream, err := backend.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	interactive, ok := stream.(agent.InteractiveToolStream)
	if !ok {
		t.Fatal("gated stream lost the InteractiveToolStream capability of the wrapped stream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := interactive.Recv(ctx); err != nil {
		t.Fatal(err)
	}
	if err := interactive.RespondToolResult(ctx, "gate-call-1", agent.ToolResult{Content: "pong"}); err != nil {
		t.Fatalf("RespondToolResult was not forwarded to the wrapped stream: %v", err)
	}
	event, err := interactive.Recv(ctx)
	if err != nil || event.Type != agent.ModelEventTextDelta || !strings.Contains(event.Delta, "pong") {
		t.Fatalf("wrapped stream did not observe the tool result: %#v, %v", event, err)
	}
}

// TestGatedStreamDoesNotInventInteractiveCapability keeps the wrapper honest for
// transports that cannot answer a tool result mid-turn.
func TestGatedStreamDoesNotInventInteractiveCapability(t *testing.T) {
	backend := gatedBackend{backend: gateTestBackend{}, turns: make(chan struct{}, 1)}
	request := agent.ModelRequest{Model: "test", Messages: []agent.Message{{Role: agent.RoleUser, Content: "тест"}}}
	stream, err := backend.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if _, ok := stream.(agent.InteractiveToolStream); ok {
		t.Fatal("a non-interactive stream must not satisfy InteractiveToolStream after wrapping")
	}
}

// TestGatedInteractiveRunCompletesWithoutWaitingForTheBudget proves the
// end-to-end consequence: with the capability lost the runtime never answers the
// Codex dynamic tool request, so the run can only end when its duration budget
// expires. The budget here is 30s while the test guard is 2s, so a regression
// fails fast instead of hanging CI.
func TestGatedInteractiveRunCompletesWithoutWaitingForTheBudget(t *testing.T) {
	backend := gatedBackend{
		backend: &gateInteractiveBackend{toolName: "gate.echo", arguments: `{"value":"ping"}`},
		turns:   make(chan struct{}, 1),
	}
	registry := agent.NewToolRegistry()
	if err := registry.Register(gateEchoTool{}); err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewRuntime(backend, registry)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Authorizer = agent.AllowAllAuthorizer{}

	type outcome struct {
		result agent.RunResult
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		result, runErr := runtime.Run(context.Background(), agent.RunRequest{
			RunID: domain.ID("run-gate-interactive"),
			ModelRequest: agent.ModelRequest{
				Model: "codex-default", Messages: []agent.Message{{Role: agent.RoleUser, Content: "вызови инструмент"}},
			},
			Budget: domain.RunBudget{
				MaxSteps: 2, MaxTokens: 4_000, MaxToolCalls: 2,
				MaxToolOutputBytes: 4096, MaxDurationSeconds: 30,
			},
		})
		finished <- outcome{result: result, err: runErr}
	}()
	select {
	case value := <-finished:
		if value.err != nil {
			t.Fatalf("gated interactive run failed: %v", value.err)
		}
		if !strings.Contains(value.result.Message.Content, "инструмент вернул: ping") {
			t.Fatalf("tool result never reached the model turn: %q", value.result.Message.Content)
		}
		if len(value.result.ToolCalls) != 1 || value.result.ToolCalls[0].Name != "gate.echo" {
			t.Fatalf("tool calls = %#v", value.result.ToolCalls)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gated Codex-style run hung on its unanswered tool call instead of completing")
	}
}

// TestDelegationOverInteractiveGatedStreamDoesNotDeadlock exercises the nested
// run path with an interactive transport: the parent turn is still open (and
// holds the single turn slot) while agent.delegate starts the child run on the
// same gated backend.
func TestDelegationOverInteractiveGatedStreamDoesNotDeadlock(t *testing.T) {
	bridge, agentID, parent := newDelegationTestBridge(t)
	stub := &gateInteractiveBackend{
		toolName: delegationToolID, arguments: `{"task":"Вложенная задача"}`,
		nestedText: "результат субагента",
	}
	backend := gatedBackend{backend: stub, turns: make(chan struct{}, 1)}
	registry := agent.NewToolRegistry()
	if err := registry.Register(delegationAgentTool{
		bridge: bridge, backend: backend, model: "test-model",
		principalAgentID: agentID, parentRunID: parent.ID,
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewRuntime(backend, registry)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Authorizer = agent.AllowAllAuthorizer{}

	type outcome struct {
		result agent.RunResult
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		result, runErr := runtime.Run(withModelTurnLease(context.Background()), agent.RunRequest{
			RunID: parent.ID,
			ModelRequest: agent.ModelRequest{
				Model: "test-model", Messages: []agent.Message{{Role: agent.RoleUser, Content: "делегируй"}},
			},
			Budget: domain.RunBudget{
				MaxSteps: 2, MaxTokens: 8_000, MaxToolCalls: 2,
				MaxToolOutputBytes: 16 * 1024, MaxDurationSeconds: 30,
			},
		})
		finished <- outcome{result: result, err: runErr}
	}()
	select {
	case value := <-finished:
		if value.err != nil {
			t.Fatalf("delegated run over the interactive path failed: %v", value.err)
		}
		if !strings.Contains(value.result.Message.Content, "результат субагента") {
			t.Fatalf("delegation result never reached the parent turn: %q", value.result.Message.Content)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("delegated run over the interactive gated path deadlocked")
	}
	delegations, err := bridge.repositories.Delegations.ListByParent(context.Background(), agentID, parent.ID)
	if err != nil || len(delegations) != 1 || delegations[0].Status != domain.DelegationStatusCompleted {
		t.Fatalf("delegations=%#v err=%v", delegations, err)
	}
}
