package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
	internaltools "github.com/OrdoAI/yuri-agent/internal/tools"
)

type filesystemWriteBackend struct {
	mu      sync.Mutex
	streams [][]agent.ModelEvent
	index   int
}

func (backend *filesystemWriteBackend) Start(context.Context, agent.ModelRequest) (agent.ModelStream, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.index >= len(backend.streams) {
		return nil, errors.New("no scripted stream")
	}
	stream := &filesystemWriteStream{events: backend.streams[backend.index]}
	backend.index++
	return stream, nil
}

type filesystemWriteStream struct {
	events []agent.ModelEvent
	index  int
}

func (stream *filesystemWriteStream) Recv(ctx context.Context) (agent.ModelEvent, error) {
	select {
	case <-ctx.Done():
		return agent.ModelEvent{}, ctx.Err()
	default:
	}
	if stream.index >= len(stream.events) {
		return agent.ModelEvent{}, io.EOF
	}
	event := stream.events[stream.index]
	stream.index++
	return event, nil
}

func (*filesystemWriteStream) Close() error { return nil }

func TestFilesystemWriteApprovalAuditFlow(t *testing.T) {
	for _, test := range []struct {
		name     string
		decision string
		created  bool
	}{
		{name: "approved", decision: "approve", created: true},
		{name: "denied", decision: "deny", created: false},
	} {
		t.Run(test.name, func(t *testing.T) {
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
				pluginSupervisors: make(map[string]*plugins.Supervisor),
				config:            config.Config{AllowedDirectories: []string{root}},
			}
			now := time.Date(2026, 8, 28, 14, 0, 0, 0, time.UTC)
			conversationID := domain.ID("conversation-write-" + test.name)
			runID := domain.ID("run-write-" + test.name)
			if err := repositories.Conversations.Create(ctx, storage.Conversation{
				ID: conversationID, AgentID: "owner", Title: "Write", CreatedAt: now, UpdatedAt: now,
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

			target := filepath.Join(root, "approved.txt")
			const secretContent = "private write payload"
			arguments, err := json.Marshal(internaltools.WriteRequest{
				Operation: internaltools.OperationCreate, Path: target, Content: secretContent,
			})
			if err != nil {
				t.Fatal(err)
			}
			backend := &filesystemWriteBackend{streams: [][]agent.ModelEvent{
				{
					{Type: agent.ModelEventToolCallStarted, ToolCallID: "call-write", ToolName: internaltools.FilesystemWriteToolID, Arguments: string(arguments)},
					{Type: agent.ModelEventCompleted},
				},
				{{Type: agent.ModelEventTextDelta, Delta: "done"}, {Type: agent.ModelEventCompleted}},
			}}
			registry, err := bridge.chatTools(now)
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := agent.NewRuntime(backend, registry)
			if err != nil {
				t.Fatal(err)
			}
			runtime.Authorizer = desktopToolAuthorizer{}
			runtime.Approvals = desktopApprovalHandler{bridge: bridge}
			emitter := newChatEmitter(bridge, string(conversationID), string(runID), "message-write")
			emitter.tools = registry

			finished := make(chan error, 1)
			go func() {
				_, runErr := runtime.Run(ctx, agent.RunRequest{
					RunID: runID, ConversationID: conversationID,
					ModelRequest: agent.ModelRequest{Model: "test-model", Messages: []agent.Message{{Role: agent.RoleUser, Content: "write file"}}},
					Budget:       domain.RunBudget{MaxSteps: 2, MaxTokens: 100, MaxToolCalls: 1, MaxToolOutputBytes: 4096, MaxDurationSeconds: 5},
					Sink:         emitter.Sink,
				})
				finished <- runErr
			}()

			approvalID := waitForFilesystemApproval(t, bridge)
			if err := bridge.ResolveApproval(approvalDecisionInput{ApprovalID: approvalID, Decision: test.decision}); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("runtime did not finish after approval resolution")
			}

			content, readErr := os.ReadFile(target)
			if test.created {
				if readErr != nil || string(content) != secretContent {
					t.Fatalf("created content = %q, err = %v", content, readErr)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("denied write changed filesystem: %v", readErr)
			}

			approvals, err := repositories.Approvals.ListByRun(ctx, runID)
			if err != nil || len(approvals) != 1 {
				t.Fatalf("approvals = %#v, err = %v", approvals, err)
			}
			wantDecision := domain.ApprovalDenied
			if test.created {
				wantDecision = domain.ApprovalApproved
			}
			canonicalRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			if approvals[0].Decision != wantDecision || approvals[0].Scope.Kind != domain.ScopeFilesystem || approvals[0].Scope.Values[0] != filepath.Join(canonicalRoot, "approved.txt") {
				t.Fatalf("approval = %#v", approvals[0])
			}
			for _, event := range emitter.Events() {
				if event.Approval != nil {
					if !strings.Contains(event.Approval.Scope, filepath.Join(canonicalRoot, "approved.txt")) ||
						!strings.Contains(event.Approval.Scope, "21 bytes") || !strings.Contains(event.Approval.Scope, "SHA-256") {
						t.Fatalf("approval did not explain the bounded write: %#v", event.Approval)
					}
				}
				if event.ToolCall != nil {
					encoded, _ := json.Marshal(event.ToolCall.Args)
					if strings.Contains(string(encoded), secretContent) {
						t.Fatalf("renderer event leaked file content: %s", encoded)
					}
				}
			}

			calls, err := repositories.ToolCalls.ListByRun(ctx, runID)
			if err != nil {
				t.Fatal(err)
			}
			if test.created {
				if len(calls) != 1 || calls[0].Status != storage.ToolCallSucceeded || calls[0].ApprovalID != approvals[0].ID {
					t.Fatalf("tool calls = %#v", calls)
				}
				if strings.Contains(calls[0].ArgsRedacted, secretContent) || !strings.Contains(calls[0].ArgsRedacted, "content_sha256") {
					t.Fatalf("tool args were not redacted: %s", calls[0].ArgsRedacted)
				}
			} else if len(calls) != 1 || calls[0].Status != storage.ToolCallDenied || calls[0].ApprovalID != approvals[0].ID {
				t.Fatalf("denied tool intent was not retained as failed: %#v", calls)
			}

			audits, err := repositories.Audit.ListByRun(ctx, runID)
			if err != nil {
				t.Fatal(err)
			}
			actions := make(map[string]bool)
			for _, audit := range audits {
				actions[audit.Action] = true
				if strings.Contains(audit.PayloadRedacted, secretContent) {
					t.Fatalf("audit leaked file content: %#v", audit)
				}
			}
			if !actions["approval.requested"] || !actions["approval.resolved"] {
				t.Fatalf("approval audit trail = %#v", audits)
			}
			if !actions["tool.proposed"] || !actions["tool.completed"] {
				t.Fatalf("tool intent audit trail = %#v", audits)
			}
			if test.created && !actions["tool.execute"] {
				t.Fatalf("tool audit trail = %#v", audits)
			}
		})
	}
}

func waitForFilesystemApproval(t *testing.T, bridge *Bridge) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		bridge.mu.RLock()
		for id := range bridge.approvals {
			bridge.mu.RUnlock()
			return id
		}
		bridge.mu.RUnlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("approval was not registered")
	return ""
}
