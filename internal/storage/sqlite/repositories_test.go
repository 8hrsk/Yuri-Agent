package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func testDatabase(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	database, err := Open(context.Background(), filepath.Join(t.TempDir(), "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, context.Background()
}

func TestSQLiteRepositoriesPersistVerticalSliceRecords(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	conversation := Conversation{ID: "conversation-1", AgentID: "owner", Title: "Initial chat", CreatedAt: now, UpdatedAt: now}
	if err := repositories.Conversations.Create(ctx, conversation); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := repositories.Messages.Create(ctx, Message{
		ID: "message-1", ConversationID: conversation.ID, Role: "user", Content: "hello",
		Status: "complete", ProviderMeta: `{"model":"test"}`, CreatedAt: now,
	}); err != nil {
		t.Fatalf("create message: %v", err)
	}
	message, err := repositories.Messages.Get(ctx, "message-1")
	if err != nil || message.Content != "hello" {
		t.Fatalf("get message = %#v, %v", message, err)
	}

	run, err := domain.NewRun("run-1", domain.RunKindInteractive, conversation.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	run.Budget = domain.RunBudget{MaxSteps: 8, MaxTokens: 4096, MaxToolOutputBytes: 1024, MaxDurationSeconds: 60}
	if err := repositories.Runs.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := run.Transition(domain.RunStateQueued, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Save(ctx, run); err != nil {
		t.Fatalf("save queued run: %v", err)
	}
	if err := run.Transition(domain.RunStateRunning, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Save(ctx, run); err != nil {
		t.Fatalf("save run: %v", err)
	}
	if err := repositories.Runs.Save(ctx, run); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale run save error = %v, want conflict", err)
	}

	approval, err := domain.NewApproval("approval-1", run.ID, "sha256:one", "modify file", domain.RiskMedium,
		domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: []string{"/tmp/yuri"}}, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	approval.ToolID = "filesystem.write"
	if err := repositories.Approvals.Create(ctx, approval); err != nil {
		t.Fatalf("create approval: %v", err)
	}
	if err := approval.Approve(domain.ActorUser, "confirmed", now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Approvals.Save(ctx, approval); err != nil {
		t.Fatalf("save approval: %v", err)
	}
	storedApprovals, err := repositories.Approvals.ListByRun(ctx, run.ID)
	if err != nil || len(storedApprovals) != 1 || storedApprovals[0].Decision != domain.ApprovalApproved {
		t.Fatalf("list approvals = %#v, %v", storedApprovals, err)
	}

	call := ToolCall{
		ID: "call-1", RunID: run.ID, ToolID: "filesystem.write", ArgsRedacted: `{"path":"/tmp/yuri/file"}`,
		Risk: domain.RiskMedium, ApprovalID: approval.ID, Status: ToolCallPending, Version: 1,
		IdempotencyKey: "run-1:call-1", CreatedAt: now.Add(5 * time.Second), UpdatedAt: now.Add(5 * time.Second),
	}
	if err := repositories.ToolCalls.Create(ctx, call); err != nil {
		t.Fatalf("create tool call: %v", err)
	}
	call.Status = ToolCallSucceeded
	call.ResultRef = "blob:abc"
	call.Version = 2
	call.UpdatedAt = now.Add(6 * time.Second)
	if err := repositories.ToolCalls.Save(ctx, call); err != nil {
		t.Fatalf("save tool call: %v", err)
	}
	foundCall, err := repositories.ToolCalls.FindByIdempotencyKey(ctx, run.ID, call.IdempotencyKey)
	if err != nil || foundCall.Status != ToolCallSucceeded {
		t.Fatalf("find tool call = %#v, %v", foundCall, err)
	}

	if err := repositories.Audit.Append(ctx, AuditEvent{
		ID: "audit-1", RunID: run.ID, ToolCallID: call.ID, ApprovalID: approval.ID,
		Actor: domain.ActorAgent, Action: "tool.execute", Target: "/tmp/yuri/file",
		Decision: domain.PermissionAllow, PayloadRedacted: `{"status":"ok"}`,
		Duration: 1250 * time.Millisecond, CreatedAt: now.Add(7 * time.Second),
	}); err != nil {
		t.Fatalf("append audit event: %v", err)
	}
	audit, err := repositories.Audit.Get(ctx, "audit-1")
	if err != nil || audit.Duration != 1250*time.Millisecond || audit.PayloadRedacted != `{"status":"ok"}` {
		t.Fatalf("get audit = %#v, %v", audit, err)
	}
	items, err := repositories.Audit.ListByRun(ctx, run.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("list audit = %#v, %v", items, err)
	}
}

func TestSQLiteRepositoriesEnforceTranscriptForeignKeysAndRedactedJSON(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	err = repositories.Messages.Create(ctx, Message{
		ID: "orphan-message", ConversationID: "missing", Role: "user", Content: "x", Status: "complete", CreatedAt: now,
	})
	if err == nil {
		t.Fatal("orphan message unexpectedly created")
	}
	if err := repositories.Conversations.Create(ctx, Conversation{ID: "conversation", AgentID: "owner", Title: "test", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	err = repositories.Messages.Create(ctx, Message{
		ID: "bad-json", ConversationID: "conversation", Role: "user", Content: "x", Status: "complete",
		ProviderMeta: "not-json", CreatedAt: now,
	})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("invalid JSON error = %v, want invalid argument", err)
	}
}
