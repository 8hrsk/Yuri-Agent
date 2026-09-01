package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	batches  [][]agent.ModelEvent
	startErr []error
	block    bool
}

func (backend *delegationBackendStub) Start(_ context.Context, request agent.ModelRequest) (agent.ModelStream, error) {
	backend.mu.Lock()
	backend.requests = append(backend.requests, request)
	requestIndex := len(backend.requests) - 1
	events := backend.events
	if requestIndex < len(backend.batches) {
		events = backend.batches[requestIndex]
	}
	var startErr error
	if requestIndex < len(backend.startErr) {
		startErr = backend.startErr[requestIndex]
	}
	backend.mu.Unlock()
	if startErr != nil {
		return nil, startErr
	}
	if backend.block {
		return delegationBlockingStream{}, nil
	}
	return &delegationStreamStub{events: append([]agent.ModelEvent(nil), events...)}, nil
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

type delegationReadToolStub struct {
	name       string
	capability domain.Capability
	mu         sync.Mutex
	calls      []agent.ToolCall
}

func (tool *delegationReadToolStub) Descriptor() agent.ToolDescriptor {
	return agent.ToolDescriptor{
		Name: tool.name, Description: "test read-only tool", InputSchema: json.RawMessage(`{"type":"object"}`),
		Risk: domain.RiskLow, Capabilities: domain.CapabilitySet{tool.capability},
	}
}

func (tool *delegationReadToolStub) Execute(_ context.Context, call agent.ToolCall) (agent.ToolResult, error) {
	tool.mu.Lock()
	tool.calls = append(tool.calls, call)
	tool.mu.Unlock()
	return agent.ToolResult{Content: `{"content":"bounded public result"}`}, nil
}

func (tool *delegationReadToolStub) callCount() int {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	return len(tool.calls)
}

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
	if child.Inference != parent.Inference || child.Usage.TotalTokens != 15 {
		t.Fatalf("anonymous child attribution=%#v usage=%#v", child.Inference, child.Usage)
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

func TestDelegationToolExecutesExplicitReadOnlyScopeAndPersistsChildTrace(t *testing.T) {
	bridge, agentID, parent := newDelegationTestBridge(t)
	readTool := &delegationReadToolStub{name: "web.fetch", capability: domain.CapabilityNetworkHTTP}
	parentTools := agent.NewToolRegistry()
	if err := parentTools.Register(readTool); err != nil {
		t.Fatal(err)
	}
	backend := &delegationBackendStub{batches: [][]agent.ModelEvent{
		{
			{Type: agent.ModelEventToolCallStarted, ToolCallID: "child-fetch", ToolName: "web.fetch", Arguments: `{"url":"https://example.com"}`},
			{Type: agent.ModelEventToolCallDone, ToolCallID: "child-fetch", ToolName: "web.fetch", Arguments: `{"url":"https://example.com"}`},
			{Type: agent.ModelEventCompleted, Usage: agent.Usage{TotalTokens: 10}},
		},
		{
			{Type: agent.ModelEventTextDelta, Delta: "Проверил источник и подготовил результат."},
			{Type: agent.ModelEventCompleted, Usage: agent.Usage{TotalTokens: 12}},
		},
	}}
	tool := delegationAgentTool{
		bridge: bridge, backend: backend, model: "test-model", principalAgentID: agentID,
		parentRunID: parent.ID, conversationID: parent.ConversationID, parentTools: parentTools,
	}
	call := agent.ToolCall{
		ID: "delegate-read-only", Name: delegationToolID,
		Arguments: json.RawMessage(`{"task":"Проверь страницу","tools":["web.fetch"]}`),
	}
	result, err := tool.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "Проверил источник") || readTool.callCount() != 1 {
		t.Fatalf("result=%s calls=%d", result.Content, readTool.callCount())
	}
	requests := backend.snapshot()
	if len(requests) != 2 || len(requests[0].Tools) != 1 || requests[0].Tools[0].Name != "web.fetch" || len(requests[1].Tools) != 1 {
		t.Fatalf("child requests did not retain the explicit tool scope: %#v", requests)
	}
	// A completed idempotent replay returns the durable result even if the
	// parent registry changed after execution; it must not repeat the side
	// effect or reinterpret the old scope through current availability.
	tool.parentTools = agent.NewToolRegistry()
	replayed, err := tool.Execute(context.Background(), call)
	if err != nil || replayed.Content != result.Content || len(backend.snapshot()) != 2 || readTool.callCount() != 1 {
		t.Fatalf("idempotent replay=%q err=%v requests=%d calls=%d", replayed.Content, err, len(backend.snapshot()), readTool.callCount())
	}

	delegations, err := bridge.repositories.Delegations.ListByParent(context.Background(), agentID, parent.ID)
	if err != nil || len(delegations) != 1 {
		t.Fatalf("delegations=%#v err=%v", delegations, err)
	}
	var scope struct {
		Tools        []string `json:"tools"`
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal([]byte(delegations[0].ScopeJSON), &scope); err != nil {
		t.Fatal(err)
	}
	if strings.Join(scope.Tools, ",") != "web.fetch" || strings.Join(scope.Capabilities, ",") != "network.http" {
		t.Fatalf("stored scope=%#v", scope)
	}
	childCalls, err := bridge.repositories.ToolCalls.ListByRun(context.Background(), delegations[0].ChildRunID)
	if err != nil || len(childCalls) != 1 || childCalls[0].ToolID != "web.fetch" || childCalls[0].Status != "succeeded" {
		t.Fatalf("child calls=%#v err=%v", childCalls, err)
	}
	audit, err := bridge.repositories.Audit.ListByRun(context.Background(), delegations[0].ChildRunID, 20)
	if err != nil {
		t.Fatal(err)
	}
	actions := make(map[string]bool, len(audit))
	for _, event := range audit {
		actions[event.Action] = true
	}
	for _, action := range []string{"tool.proposed", "tool.execute", "tool.completed", "delegation.completed"} {
		if !actions[action] {
			t.Fatalf("missing %s in child audit: %#v", action, actions)
		}
	}
	conversations, err := bridge.ListConversations()
	if err != nil || len(conversations) != 1 {
		t.Fatalf("conversations=%#v err=%v", conversations, err)
	}
	var childTrace *RunTraceView
	for index := range conversations[0].Traces {
		if conversations[0].Traces[index].ID == string(delegations[0].ChildRunID) {
			childTrace = &conversations[0].Traces[index]
			break
		}
	}
	if childTrace == nil || childTrace.Kind != string(domain.RunKindSubagent) || childTrace.ParentRunID != string(parent.ID) || len(childTrace.ToolCalls) != 1 {
		t.Fatalf("persisted child trace=%#v", childTrace)
	}
}

func TestDelegationToolRejectsWritableUnavailableAndUnapprovedScopes(t *testing.T) {
	bridge, agentID, parent := newDelegationTestBridge(t)
	parentTools := agent.NewToolRegistry()
	readTool := &delegationReadToolStub{name: "filesystem.read", capability: domain.CapabilityFilesystemRead}
	if err := parentTools.Register(readTool); err != nil {
		t.Fatal(err)
	}
	tool := delegationAgentTool{
		bridge: bridge, backend: &delegationBackendStub{}, model: "test-model", principalAgentID: agentID,
		parentRunID: parent.ID, conversationID: parent.ConversationID, parentTools: parentTools,
	}
	checks := []struct {
		id   string
		args string
	}{
		{id: "delegate-write", args: `{"task":"Измени файл","tools":["filesystem.write"]}`},
		{id: "delegate-search", args: `{"task":"Найди","tools":["web.search"]}`},
		{id: "delegate-no-root", args: `{"task":"Прочитай","tools":["filesystem.read"]}`},
	}
	for _, check := range checks {
		_, err := tool.Execute(context.Background(), agent.ToolCall{ID: check.id, Name: delegationToolID, Arguments: json.RawMessage(check.args)})
		if !errors.Is(err, domain.ErrNotPermitted) {
			t.Fatalf("%s error=%v, want not permitted", check.id, err)
		}
	}
	delegations, err := bridge.repositories.Delegations.ListByParent(context.Background(), agentID, parent.ID)
	if err != nil || len(delegations) != 0 {
		t.Fatalf("rejected scope created delegations=%#v err=%v", delegations, err)
	}

	authorizer := delegationToolAuthorizer{bridge: bridge, allowed: map[string]domain.Capability{"filesystem.read": domain.CapabilityFilesystemRead}}
	decision, err := authorizer.Authorize(context.Background(), agent.ToolAuthorizationRequest{
		Tool: agent.ToolDescriptor{Name: "filesystem.write", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: domain.RiskMedium, Capabilities: domain.CapabilitySet{domain.CapabilityFilesystemWrite}},
		Call: agent.ToolCall{ID: "write", Name: "filesystem.write", Arguments: json.RawMessage(`{"path":"/tmp/x"}`)},
	})
	if err != nil || decision.Decision != domain.PermissionDeny {
		t.Fatalf("write authorization=%#v err=%v", decision, err)
	}
}

func TestDelegationToolFailsClosedWhenChildExceedsToolBudget(t *testing.T) {
	bridge, agentID, parent := newDelegationTestBridge(t)
	readTool := &delegationReadToolStub{name: "web.fetch", capability: domain.CapabilityNetworkHTTP}
	parentTools := agent.NewToolRegistry()
	if err := parentTools.Register(readTool); err != nil {
		t.Fatal(err)
	}
	events := make([]agent.ModelEvent, 0, defaultDelegationBudget.MaxToolCalls+1)
	for index := 0; index <= defaultDelegationBudget.MaxToolCalls; index++ {
		events = append(events, agent.ModelEvent{
			Type: agent.ModelEventToolCallDone, ToolCallID: fmt.Sprintf("child-overflow-%d", index),
			ToolName: "web.fetch", Arguments: `{"url":"https://example.com"}`,
		})
	}
	events = append(events, agent.ModelEvent{Type: agent.ModelEventCompleted})
	backend := &delegationBackendStub{batches: [][]agent.ModelEvent{events}}
	tool := delegationAgentTool{
		bridge: bridge, backend: backend, model: "test-model", principalAgentID: agentID,
		parentRunID: parent.ID, conversationID: parent.ConversationID, parentTools: parentTools,
	}
	_, err := tool.Execute(context.Background(), agent.ToolCall{
		ID: "delegate-over-budget", Name: delegationToolID,
		Arguments: json.RawMessage(`{"task":"Сделай слишком много вызовов","tools":["web.fetch"]}`),
	})
	if !errors.Is(err, agent.ErrBudgetExceeded) {
		t.Fatalf("overflow error=%v, want budget exceeded", err)
	}
	if readTool.callCount() != 0 {
		t.Fatalf("tool executed %d times even though the turn exceeded its budget", readTool.callCount())
	}
	delegations, listErr := bridge.repositories.Delegations.ListByParent(context.Background(), agentID, parent.ID)
	if listErr != nil || len(delegations) != 1 || delegations[0].Status != domain.DelegationStatusFailed {
		t.Fatalf("failed delegation=%#v err=%v", delegations, listErr)
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
	parent.Inference = domain.RunInferenceRoute{ProviderID: "test-provider", Model: "test-model"}
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

// TestDelegationCompletesWhileParentHoldsTheTurnGate is the regression test for
// the agent.delegate self-deadlock. A delegated run executes as a tool call
// inside the parent run, so with a Codex-style interactive turn the parent
// still holds the single modelTurns slot while the child asks for one. The
// child waited for a slot only the parent could release, and the parent waited
// for the child's tool result: the run could only end at the duration budget.
func TestDelegationCompletesWhileParentHoldsTheTurnGate(t *testing.T) {
	bridge, agentID, parent := newDelegationTestBridge(t)
	stub := &delegationBackendStub{events: []agent.ModelEvent{
		{Type: agent.ModelEventTextDelta, Delta: "Результат субагента."},
		{Type: agent.ModelEventCompleted},
	}}
	turns := make(chan struct{}, 1)
	backend := gatedBackend{backend: stub, turns: turns}
	request := agent.ModelRequest{Model: "test-model", Messages: []agent.Message{{Role: agent.RoleUser, Content: "родительский запрос"}}}

	runContext := withModelTurnLease(context.Background())
	parentStream, err := backend.Start(runContext, request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parentStream.Close() }()

	// The gate must still be exclusive for anything outside this run subtree.
	foreign := make(chan struct{})
	go func() {
		stream, startErr := backend.Start(withModelTurnLease(context.Background()), request)
		if startErr == nil {
			_ = stream.Close()
		}
		close(foreign)
	}()
	select {
	case <-foreign:
		t.Fatal("an unrelated run took the slot while the parent turn was open")
	case <-time.After(50 * time.Millisecond):
	}

	tool := delegationAgentTool{bridge: bridge, backend: backend, model: "test-model", principalAgentID: agentID, parentRunID: parent.ID}
	call := agent.ToolCall{ID: "delegate-nested-turn", Name: delegationToolID, Arguments: json.RawMessage(`{"task":"Вложенная задача"}`)}
	type outcome struct {
		result agent.ToolResult
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		result, execErr := tool.Execute(runContext, call)
		finished <- outcome{result: result, err: execErr}
	}()
	select {
	case value := <-finished:
		if value.err != nil {
			t.Fatalf("delegated run failed: %v", value.err)
		}
		if !strings.Contains(value.result.Content, "Результат субагента.") {
			t.Fatalf("delegated result = %q", value.result.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delegated run deadlocked on the parent run's turn slot")
	}

	delegations, err := bridge.repositories.Delegations.ListByParent(context.Background(), agentID, parent.ID)
	if err != nil || len(delegations) != 1 || delegations[0].Status != domain.DelegationStatusCompleted {
		t.Fatalf("delegations=%#v err=%v", delegations, err)
	}
	if err := parentStream.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-foreign:
	case <-time.After(2 * time.Second):
		t.Fatal("unrelated run never got the slot after the parent turn closed")
	}
}

// TestModelTurnLeaseReleasesSlotAfterNestedTurns makes sure reentrancy does not
// leak the slot: once the whole subtree is done the gate is free again.
func TestModelTurnLeaseReleasesSlotAfterNestedTurns(t *testing.T) {
	turns := make(chan struct{}, 1)
	backend := gatedBackend{backend: &delegationBackendStub{}, turns: turns}
	request := agent.ModelRequest{Model: "test-model", Messages: []agent.Message{{Role: agent.RoleUser, Content: "запрос"}}}
	ctx := withModelTurnLease(context.Background())
	outer, err := backend.Start(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	nested, err := backend.Start(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := nested.Close(); err != nil {
		t.Fatal(err)
	}
	if err := outer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case turns <- struct{}{}:
		<-turns
	default:
		t.Fatal("turn slot was not returned after the nested turns finished")
	}
}
