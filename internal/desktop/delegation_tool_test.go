package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
)

type delegationBackendStub struct {
	mu       sync.Mutex
	requests []agent.ModelRequest
	events   []agent.ModelEvent
	block    bool
}

func (backend *delegationBackendStub) Start(_ context.Context, request agent.ModelRequest) (agent.ModelStream, error) {
	backend.mu.Lock()
	backend.requests = append(backend.requests, request)
	backend.mu.Unlock()
	if backend.block {
		return delegationBlockingStream{}, nil
	}
	return &delegationStreamStub{events: append([]agent.ModelEvent(nil), backend.events...)}, nil
}

func (backend *delegationBackendStub) snapshot() []agent.ModelRequest {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return append([]agent.ModelRequest(nil), backend.requests...)
}

type delegationStreamStub struct {
	events []agent.ModelEvent
	index  int
}

func (stream *delegationStreamStub) Recv(context.Context) (agent.ModelEvent, error) {
	if stream.index >= len(stream.events) {
		return agent.ModelEvent{}, io.EOF
	}
	event := stream.events[stream.index]
	stream.index++
	return event, nil
}
func (*delegationStreamStub) Close() error { return nil }

type delegationBlockingStream struct{}

func (delegationBlockingStream) Recv(ctx context.Context) (agent.ModelEvent, error) {
	<-ctx.Done()
	return agent.ModelEvent{}, ctx.Err()
}
func (delegationBlockingStream) Close() error { return nil }

func TestDelegationToolCreatesAnonymousToollessChildAndIsIdempotent(t *testing.T) {
	bridge, agentID, parent := newDelegationTestBridge(t)
	backend := &delegationBackendStub{events: []agent.ModelEvent{
		{Type: agent.ModelEventTextDelta, Delta: "Краткий проверенный результат."},
		{Type: agent.ModelEventCompleted, Usage: agent.Usage{TotalTokens: 15}},
	}}
	tool := delegationAgentTool{bridge: bridge, backend: backend, model: "test-model", principalAgentID: agentID, parentRunID: parent.ID}
	call := agent.ToolCall{ID: "delegate-call-1", Name: delegationToolID, Arguments: json.RawMessage(`{"task":"Сравни варианты","context":"Только публичные факты"}`)}

	first, err := tool.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	second, err := tool.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if first.Content != second.Content || len(backend.snapshot()) != 1 {
		t.Fatalf("idempotent result mismatch or backend repeated: first=%q second=%q requests=%d", first.Content, second.Content, len(backend.snapshot()))
	}
	requests := backend.snapshot()
	request := requests[0]
	if len(request.Tools) != 0 || len(request.Messages) != 2 || request.Metadata["purpose"] != "anonymous_subagent" {
		t.Fatalf("unsafe child request: %#v", request)
	}
	joined := request.Messages[0].Content + request.Messages[1].Content
	for _, forbidden := range []string{"Секретное имя", "приватная-привычка", "Мира"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("private named-agent state leaked into child request: %q", joined)
		}
	}
	delegations, err := bridge.repositories.Delegations.ListByParent(context.Background(), agentID, parent.ID)
	if err != nil || len(delegations) != 1 {
		t.Fatalf("delegations=%#v err=%v", delegations, err)
	}
	stored := delegations[0]
	if stored.Status != domain.DelegationStatusCompleted || stored.ResultText != "Краткий проверенный результат." || stored.Depth != 1 {
		t.Fatalf("stored delegation=%#v", stored)
	}
	child, err := bridge.repositories.Runs.Get(context.Background(), stored.ChildRunID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Kind != domain.RunKindSubagent || child.ParentRunID != parent.ID || !child.ConversationID.Empty() || child.AgentID != agentID || child.State != domain.RunStateCompleted {
		t.Fatalf("anonymous child=%#v", child)
	}
	if agents, err := bridge.repositories.Agents.List(context.Background()); err != nil || len(agents) != 1 {
		t.Fatalf("delegation created an identity: agents=%#v err=%v", agents, err)
	}
	if redacted := redactedDelegationArguments(call.Arguments, 4096); strings.Contains(redacted, "Сравни") || !strings.Contains(redacted, "task_sha256") {
		t.Fatalf("delegation arguments were not redacted: %s", redacted)
	}

	changed := call
	changed.Arguments = json.RawMessage(`{"task":"Другая задача"}`)
	if _, err := tool.Execute(context.Background(), changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("changed idempotent request error=%v, want conflict", err)
	}
	unknown := agent.ToolCall{ID: "unknown-field", Name: delegationToolID, Arguments: json.RawMessage(`{"task":"Задача","agent_id":"peer"}`)}
	if _, err := tool.Execute(context.Background(), unknown); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("unknown argument error=%v, want invalid argument", err)
	}
}

func TestDelegationToolBoundsChildrenPerParent(t *testing.T) {
	bridge, agentID, parent := newDelegationTestBridge(t)
	backend := &delegationBackendStub{events: []agent.ModelEvent{
		{Type: agent.ModelEventTextDelta, Delta: "result"}, {Type: agent.ModelEventCompleted},
	}}
	tool := delegationAgentTool{bridge: bridge, backend: backend, model: "test-model", principalAgentID: agentID, parentRunID: parent.ID}
	for index := 0; index < delegationMaxPerParent; index++ {
		call := agent.ToolCall{ID: "bounded-" + string(rune('a'+index)), Name: delegationToolID, Arguments: json.RawMessage(`{"task":"Задача"}`)}
		if _, err := tool.Execute(context.Background(), call); err != nil {
			t.Fatalf("delegation %d error=%v", index, err)
		}
	}
	_, err := tool.Execute(context.Background(), agent.ToolCall{ID: "bounded-overflow", Name: delegationToolID, Arguments: json.RawMessage(`{"task":"Лишняя задача"}`)})
	if !errors.Is(err, domain.ErrNotPermitted) {
		t.Fatalf("overflow error=%v, want not permitted", err)
	}
	if len(backend.snapshot()) != delegationMaxPerParent {
		t.Fatalf("backend requests=%d, want %d", len(backend.snapshot()), delegationMaxPerParent)
	}
}

func TestDelegationToolCancellationPersistsTerminalPair(t *testing.T) {
	bridge, agentID, parent := newDelegationTestBridge(t)
	backend := &delegationBackendStub{block: true}
	tool := delegationAgentTool{bridge: bridge, backend: backend, model: "test-model", principalAgentID: agentID, parentRunID: parent.ID}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := tool.Execute(ctx, agent.ToolCall{ID: "delegate-cancel", Name: delegationToolID, Arguments: json.RawMessage(`{"task":"Жди"}`)})
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for len(backend.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
	delegations, err := bridge.repositories.Delegations.ListByParent(context.Background(), agentID, parent.ID)
	if err != nil || len(delegations) != 1 || delegations[0].Status != domain.DelegationStatusCancelled {
		t.Fatalf("cancelled delegation=%#v err=%v", delegations, err)
	}
	child, err := bridge.repositories.Runs.Get(context.Background(), delegations[0].ChildRunID)
	if err != nil || child.State != domain.RunStateCancelled {
		t.Fatalf("cancelled child=%#v err=%v", child, err)
	}
}

func newDelegationTestBridge(t *testing.T) (*Bridge, domain.ID, domain.AgentRun) {
	t.Helper()
	bridge := newAgentTestBridge(t)
	created, err := bridge.CreateAgent(CreateAgentInput{
		Name: "Секретное имя", Gender: "female", Preferences: "приватная-привычка",
	})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := bridge.NewConversation("Родительский диалог")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	parent, err := domain.NewRunForAgent(domain.ID(created.ID), domain.ID("run-parent-delegation"), domain.RunKindInteractive, domain.ID(conversation.ID), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.repositories.Runs.Create(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	if err := transitionAndSave(context.Background(), bridge.repositories.Runs, &parent, domain.RunStateQueued); err != nil {
		t.Fatal(err)
	}
	if err := transitionAndSave(context.Background(), bridge.repositories.Runs, &parent, domain.RunStateRunning); err != nil {
		t.Fatal(err)
	}
	return bridge, domain.ID(created.ID), parent
}
