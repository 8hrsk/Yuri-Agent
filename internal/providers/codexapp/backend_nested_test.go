package codexapp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// appServerHarness is an in-memory stand-in for the `codex app-server` process.
// It speaks the same newline-delimited JSON-RPC dialect over pipes so a real
// Client, Backend, and agent.Runtime can be exercised end to end.
type appServerHarness struct {
	client *Client

	writeMu  sync.Mutex
	toClient io.Writer

	mu            sync.Mutex
	nextRequestID int
	responses     map[string]chan map[string]any
}

func newAppServerHarness(t *testing.T, script func(harness *appServerHarness, threadID, turnID string)) *appServerHarness {
	t.Helper()
	serverInput, clientInput := io.Pipe()
	clientOutput, serverOutput := io.Pipe()
	client := newClient(clientInput, clientOutput, 0)
	harness := &appServerHarness{
		client: client, toClient: serverOutput,
		responses: make(map[string]chan map[string]any),
	}
	t.Cleanup(func() {
		_ = serverInput.Close()
		_ = serverOutput.Close()
		_ = client.Close()
	})
	go harness.serve(serverInput, script)
	return harness
}

func (harness *appServerHarness) serve(input io.Reader, script func(*appServerHarness, string, string)) {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	threads, turns := 0, 0
	for scanner.Scan() {
		var message map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return
		}
		method, _ := message["method"].(string)
		id, hasID := message["id"]
		if method == "" && hasID {
			key := fmt.Sprint(id)
			harness.mu.Lock()
			waiter := harness.responses[key]
			delete(harness.responses, key)
			harness.mu.Unlock()
			if waiter != nil {
				waiter <- message
			}
			continue
		}
		if !hasID {
			continue
		}
		switch method {
		case "thread/start":
			threads++
			harness.reply(id, map[string]any{"thread": map[string]any{"id": fmt.Sprintf("thread-%d", threads)}})
		case "turn/start":
			turns++
			params, _ := message["params"].(map[string]any)
			threadID, _ := params["threadId"].(string)
			turnID := fmt.Sprintf("turn-%d", turns)
			harness.reply(id, map[string]any{"turn": map[string]any{"id": turnID}})
			go script(harness, threadID, turnID)
		default:
			harness.reply(id, map[string]any{})
		}
	}
}

func (harness *appServerHarness) write(message map[string]any) {
	encoded, err := json.Marshal(message)
	if err != nil {
		return
	}
	harness.writeMu.Lock()
	defer harness.writeMu.Unlock()
	_, _ = harness.toClient.Write(append(encoded, '\n'))
}

func (harness *appServerHarness) reply(id any, result map[string]any) {
	harness.write(map[string]any{"id": id, "result": result})
}

func (harness *appServerHarness) notify(method string, params map[string]any) {
	harness.write(map[string]any{"method": method, "params": params})
}

func (harness *appServerHarness) delta(threadID, turnID, text string) {
	harness.notify("item/agentMessage/delta", map[string]any{
		"threadId": threadID, "turnId": turnID, "itemId": "item-" + turnID, "delta": text,
	})
}

func (harness *appServerHarness) completeTurn(threadID, turnID string) {
	harness.notify("turn/completed", map[string]any{
		"threadId": threadID,
		"turn":     map[string]any{"id": turnID, "status": "completed"},
	})
}

// requestToolCall sends a dynamic tool request and blocks until the client
// answers it, exactly like the app server does for `item/tool/call`.
func (harness *appServerHarness) requestToolCall(threadID, turnID, callID, tool string, arguments map[string]any) map[string]any {
	harness.mu.Lock()
	harness.nextRequestID++
	id := 9000 + harness.nextRequestID
	waiter := make(chan map[string]any, 1)
	harness.responses[strconv.Itoa(id)] = waiter
	harness.mu.Unlock()
	harness.write(map[string]any{
		"id": id, "method": "item/tool/call",
		"params": map[string]any{
			"threadId": threadID, "turnId": turnID, "callId": callID,
			"tool": tool, "arguments": arguments,
		},
	})
	select {
	case reply := <-waiter:
		return reply
	case <-time.After(10 * time.Second):
		return nil
	}
}

// nestedRunTool models agent.delegate: it starts a second run on the same Codex
// backend while the parent turn is still open and waiting for this result.
type nestedRunTool struct {
	backend agent.ModelBackend
	model   string
}

func (tool nestedRunTool) Descriptor() agent.ToolDescriptor {
	return agent.ToolDescriptor{
		Name: "nested_run", Description: "run a nested turn", Risk: domain.RiskLow,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"task":{"type":"string"}},"required":["task"]}`),
	}
}

func (tool nestedRunTool) Execute(ctx context.Context, _ agent.ToolCall) (agent.ToolResult, error) {
	runtime, err := agent.NewRuntime(tool.backend, agent.NewToolRegistry())
	if err != nil {
		return agent.ToolResult{}, err
	}
	result, err := runtime.Run(ctx, agent.RunRequest{
		RunID: domain.ID("run-nested"),
		ModelRequest: agent.ModelRequest{
			Model:    tool.model,
			Messages: []agent.Message{{Role: agent.RoleUser, Content: "вложенная задача"}},
		},
		Budget: domain.RunBudget{
			MaxSteps: 1, MaxTokens: 4_000, MaxToolCalls: 1,
			MaxToolOutputBytes: 4096, MaxDurationSeconds: 20,
		},
	})
	if err != nil {
		return agent.ToolResult{}, err
	}
	return agent.ToolResult{Content: result.Message.Content}, nil
}

// TestNestedTurnOnSharedClientCompletesAndKeepsEventsSeparate covers both
// provider-level defects at once:
//
//   - a nested run started while the parent Codex turn is open must not queue
//     behind a provider-owned capacity-1 turn gate that only the parent can
//     release (the H-2 deadlock, reintroduced one layer lower);
//   - two live turns share one app-server connection, so each turn must observe
//     only its own events. The harness deliberately emits a parent notification
//     while the nested turn is the only active reader.
func TestNestedTurnOnSharedClientCompletesAndKeepsEventsSeparate(t *testing.T) {
	script := func(server *appServerHarness, threadID, turnID string) {
		if threadID == "thread-1" {
			reply := server.requestToolCall(threadID, turnID, "call-1", "nested_run", map[string]any{"task": "вложенная"})
			if reply == nil {
				return
			}
			server.delta(threadID, turnID, "|родитель-финал|")
			server.completeTurn(threadID, turnID)
			return
		}
		// A parent notification arrives while only the nested turn is reading.
		server.delta("thread-1", "turn-1", "|родитель-в-фоне|")
		server.delta(threadID, turnID, "вложенный ответ")
		server.completeTurn(threadID, turnID)
	}
	harness := newAppServerHarness(t, script)

	backend, err := NewBackend(harness.client, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	registry := agent.NewToolRegistry()
	if err := registry.Register(nestedRunTool{backend: backend, model: "codex-default"}); err != nil {
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
			RunID: domain.ID("run-parent"),
			ModelRequest: agent.ModelRequest{
				Model:    "codex-default",
				Messages: []agent.Message{{Role: agent.RoleUser, Content: "делегируй"}},
			},
			Budget: domain.RunBudget{
				MaxSteps: 2, MaxTokens: 16_000, MaxToolCalls: 2,
				MaxToolOutputBytes: 16 * 1024, MaxDurationSeconds: 30,
			},
		})
		finished <- outcome{result: result, err: runErr}
	}()

	select {
	case value := <-finished:
		if value.err != nil {
			t.Fatalf("parent run over the Codex path failed: %v", value.err)
		}
		content := value.result.Message.Content
		if !strings.Contains(content, "|родитель-финал|") {
			t.Fatalf("parent turn lost its own final delta: %q", content)
		}
		if !strings.Contains(content, "|родитель-в-фоне|") {
			t.Fatalf("a parent notification was consumed by the nested turn: %q", content)
		}
		if strings.Contains(content, "вложенный ответ") {
			t.Fatalf("parent turn consumed the nested turn's output: %q", content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nested Codex turn deadlocked on the provider turn gate")
	}
}
