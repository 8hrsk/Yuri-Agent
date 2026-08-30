package desktop

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
	internaltools "github.com/OrdoAI/yuri-agent/internal/tools"
)

// TestCreateApprovalUsesToolDescribedScope covers the consumer half of
// ToolApprovalScoper: the approval card must show what the call actually does,
// not the generic capability list every scheduling call shares.
func TestCreateApprovalUsesToolDescribedScope(t *testing.T) {
	ctx := context.Background()
	database, err := storage.Open(ctx, filepath.Join(t.TempDir(), "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &Bridge{database: database, repositories: repositories, approvals: make(map[string]*approvalGate)}
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	conversationID := domain.ID("conversation-approval-scope")
	runID := domain.ID("run-approval-scope")
	if err := repositories.Conversations.Create(ctx, storage.Conversation{
		ID: conversationID, AgentID: "owner", Title: "Scope", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewRun(runID, domain.RunKindInteractive, conversationID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Create(ctx, run); err != nil {
		t.Fatal(err)
	}

	registry := agent.NewToolRegistry()
	if err := registry.Register(scheduleAgentTool{bridge: bridge}); err != nil {
		t.Fatal(err)
	}
	emitter := newChatEmitter(bridge, string(conversationID), string(runID), "message-scope")
	emitter.tools = registry

	arguments := json.RawMessage(`{"title":"t","prompt":"p","type":"interval","intervalSeconds":900,` +
		`"timezone":"UTC","misfirePolicy":"skip","budget":{"maxTokens":4000}}`)
	call := agent.ToolCall{ID: "call-schedule", Name: (scheduleAgentTool{}).Descriptor().Name, Arguments: arguments}
	view, err := emitter.createApproval(ctx, agent.Event{Type: agent.EventToolApprovalNeeded, ToolCall: &call})
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"900", "UTC", "in_app", "4000"} {
		if !strings.Contains(view.Scope, fragment) {
			t.Fatalf("approval scope %q is missing %q", view.Scope, fragment)
		}
	}
}

// TestCreateApprovalFallsBackToCapabilities keeps the generic path intact for
// tools that cannot describe one specific call.
func TestCreateApprovalFallsBackToCapabilities(t *testing.T) {
	ctx := context.Background()
	database, err := storage.Open(ctx, filepath.Join(t.TempDir(), "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &Bridge{database: database, repositories: repositories, approvals: make(map[string]*approvalGate)}
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	conversationID := domain.ID("conversation-approval-generic")
	runID := domain.ID("run-approval-generic")
	if err := repositories.Conversations.Create(ctx, storage.Conversation{
		ID: conversationID, AgentID: "owner", Title: "Scope", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewRun(runID, domain.RunKindInteractive, conversationID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	emitter := newChatEmitter(bridge, string(conversationID), string(runID), "message-generic")
	call := agent.ToolCall{ID: "call-generic", Name: "plugin.tool", Arguments: json.RawMessage(`{}`)}
	view, err := emitter.createApproval(ctx, agent.Event{Type: agent.EventToolApprovalNeeded, ToolCall: &call})
	if err != nil {
		t.Fatal(err)
	}
	if view.Scope != "plugin tool" {
		t.Fatalf("generic approval scope = %q", view.Scope)
	}
}

// TestCreateApprovalPassesTheRefinedActionToTheConstructor pins the small
// cleanup that goes with M-15. createApproval used to hand domain.NewApproval
// the generic "execute tool <name>" and correct the record afterwards, so the
// write path only survived because nothing in the constructor looked at that
// argument. The refined action is now the one the constructor receives.
func TestCreateApprovalPassesTheRefinedActionToTheConstructor(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	database, err := storage.Open(ctx, filepath.Join(t.TempDir(), "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &Bridge{
		database: database, repositories: repositories, approvals: make(map[string]*approvalGate),
		config: config.Config{AllowedDirectories: []string{root}},
	}
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	conversationID := domain.ID("conversation-approval-action")
	runID := domain.ID("run-approval-action")
	if err := repositories.Conversations.Create(ctx, storage.Conversation{
		ID: conversationID, AgentID: "owner", Title: "Action", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewRun(runID, domain.RunKindInteractive, conversationID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	registry, err := bridge.chatTools(now)
	if err != nil {
		t.Fatal(err)
	}
	emitter := newChatEmitter(bridge, string(conversationID), string(runID), "message-action")
	emitter.tools = registry

	target := filepath.Join(root, "note.txt")
	arguments, err := json.Marshal(internaltools.WriteRequest{
		Operation: internaltools.OperationCreate, Path: target, Content: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	call := agent.ToolCall{ID: "call-action", Name: internaltools.FilesystemWriteToolID, Arguments: arguments}
	if _, err := emitter.createApproval(ctx, agent.Event{Type: agent.EventToolApprovalNeeded, ToolCall: &call}); err != nil {
		t.Fatal(err)
	}
	stored, err := repositories.Approvals.Get(ctx, approvalIDFor(runID, call.ID))
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	want := "filesystem." + string(internaltools.OperationCreate) + " " + filepath.Join(canonicalRoot, "note.txt")
	if stored.Action != want {
		t.Fatalf("persisted approval action = %q, want %q", stored.Action, want)
	}
}
