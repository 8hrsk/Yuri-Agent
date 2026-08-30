package desktop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
	builtintools "github.com/OrdoAI/yuri-agent/internal/tools"
)

func TestFilesystemReadRequestsAccessAndResumesSameCall(t *testing.T) {
	for _, decision := range []string{"allow_once", "allow_always"} {
		t.Run(decision, func(t *testing.T) {
			ctx := context.Background()
			requestedRoot := t.TempDir()
			target := filepath.Join(requestedRoot, "note.txt")
			if err := os.WriteFile(target, []byte("private note for Yuri"), 0o600); err != nil {
				t.Fatal(err)
			}
			canonicalRoot, err := filepath.EvalSymlinks(requestedRoot)
			if err != nil {
				t.Fatal(err)
			}
			canonicalTarget := filepath.Join(canonicalRoot, "note.txt")
			profile := t.TempDir()
			paths := config.Paths{
				ConfigDirectory: filepath.Join(profile, "config"), ConfigFile: filepath.Join(profile, "config", "config.json"),
				DataDirectory: filepath.Join(profile, "data"), DatabaseFile: filepath.Join(profile, "data", "yuri.sqlite3"),
			}
			if err := os.MkdirAll(paths.DataDirectory, 0o700); err != nil {
				t.Fatal(err)
			}
			database, err := storage.Open(ctx, paths.DatabaseFile)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			repositories, err := storage.NewRepositories(database)
			if err != nil {
				t.Fatal(err)
			}
			bridge := &Bridge{
				database: database, repositories: repositories, paths: paths, config: config.Default(paths),
				approvals: make(map[string]*approvalGate), pluginSupervisors: make(map[string]*plugins.Supervisor),
			}
			now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
			conversationID := domain.ID("conversation-read-" + decision)
			runID := domain.ID("run-read-" + decision)
			if err := repositories.Conversations.Create(ctx, storage.Conversation{
				ID: conversationID, AgentID: "owner", Title: "Read", CreatedAt: now, UpdatedAt: now,
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
			arguments, _ := json.Marshal(builtintools.ReadRequest{Operation: builtintools.OperationRead, Path: target})
			backend := &filesystemWriteBackend{streams: [][]agent.ModelEvent{
				{{Type: agent.ModelEventToolCallStarted, ToolCallID: "call-read", ToolName: builtintools.FilesystemReadToolID, Arguments: string(arguments)}, {Type: agent.ModelEventCompleted}},
				{{Type: agent.ModelEventTextDelta, Delta: "прочитано"}, {Type: agent.ModelEventCompleted}},
			}}
			registry, err := bridge.chatTools(now)
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := agent.NewRuntime(backend, registry)
			if err != nil {
				t.Fatal(err)
			}
			runtime.Authorizer = desktopToolAuthorizer{bridge: bridge}
			runtime.Approvals = desktopApprovalHandler{bridge: bridge}
			emitter := newChatEmitter(bridge, string(conversationID), string(runID), "message-read")
			emitter.tools = registry
			finished := make(chan error, 1)
			go func() {
				_, runErr := runtime.Run(ctx, agent.RunRequest{
					RunID: runID, ConversationID: conversationID,
					ModelRequest: agent.ModelRequest{Model: "test", Messages: []agent.Message{{Role: agent.RoleUser, Content: "read"}}},
					Budget:       domain.RunBudget{MaxSteps: 2, MaxTokens: 100, MaxToolCalls: 1, MaxToolOutputBytes: 4096, MaxDurationSeconds: 5}, Sink: emitter.Sink,
				})
				finished <- runErr
			}()

			approvalID := waitForFilesystemApproval(t, bridge)
			var approvalView *ApprovalView
			for _, event := range emitter.Events() {
				if event.Approval != nil {
					approvalView = event.Approval
				}
			}
			if approvalView == nil || approvalView.Kind != "filesystem_access" || !approvalView.CanRemember || approvalView.Path != canonicalTarget || approvalView.PermissionRoot != canonicalRoot {
				t.Fatalf("filesystem approval = %#v", approvalView)
			}
			if err := bridge.ResolveApproval(approvalDecisionInput{ApprovalID: approvalID, Decision: decision}); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("runtime did not resume the approved filesystem call")
			}
			calls, err := repositories.ToolCalls.ListByRun(ctx, runID)
			if err != nil || len(calls) != 1 || calls[0].Status != storage.ToolCallSucceeded {
				t.Fatalf("tool calls = %#v, err = %v", calls, err)
			}
			persisted := len(bridge.AllowedDirectories()) == 1
			if persisted != (decision == "allow_always") {
				t.Fatalf("persisted roots = %#v for %s", bridge.AllowedDirectories(), decision)
			}
		})
	}
}

func TestFilesystemAccessRejectsRelativeAndSymlinkWriteTargets(t *testing.T) {
	if _, err := resolveFilesystemAccess(agent.ToolCall{
		Name: builtintools.FilesystemReadToolID, Arguments: json.RawMessage(`{"path":"relative.txt"}`),
	}); err == nil {
		t.Fatal("relative read path was accepted")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	arguments, _ := json.Marshal(builtintools.WriteRequest{Operation: builtintools.OperationReplace, Path: link, Content: "change"})
	if _, err := resolveFilesystemAccess(agent.ToolCall{Name: builtintools.FilesystemWriteToolID, Arguments: arguments}); err == nil {
		t.Fatal("symlink write target was accepted")
	}
}
