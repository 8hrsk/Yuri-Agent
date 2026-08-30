package desktop

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

// approvalWaitFixture builds a bridge with one running run and one pending
// approval registered exactly the way createApproval registers it.
func approvalWaitFixture(t *testing.T, name string, expiresIn time.Duration) (*Bridge, *domain.AgentRun, domain.ID, agent.ToolCall) {
	t.Helper()
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
	now := time.Now().UTC()
	conversationID := domain.ID("conversation-" + name)
	runID := domain.ID("run-" + name)
	if err := repositories.Conversations.Create(ctx, storage.Conversation{
		ID: conversationID, AgentID: "owner", Title: name, CreatedAt: now, UpdatedAt: now,
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
	if err := transitionAndSave(ctx, repositories.Runs, &run, domain.RunStateQueued); err != nil {
		t.Fatal(err)
	}
	if err := transitionAndSave(ctx, repositories.Runs, &run, domain.RunStateRunning); err != nil {
		t.Fatal(err)
	}
	call := agent.ToolCall{ID: "call-" + name, Name: "filesystem.write", Arguments: []byte(`{"operation":"create"}`)}
	id := approvalIDFor(runID, call.ID)
	approval, err := domain.NewApproval(id, runID, "hash", "filesystem.create /tmp/x", domain.RiskMedium,
		domain.CapabilityScope{Kind: domain.ScopeResource, Values: []string{"filesystem.write"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	approval.ToolID = "filesystem.write"
	approval.ExpiresAt = now.Add(expiresIn)
	if err := repositories.Approvals.Create(ctx, approval); err != nil {
		t.Fatal(err)
	}
	bridge := &Bridge{database: database, repositories: repositories, approvals: make(map[string]*approvalGate)}
	bridge.registerApproval(id)
	return bridge, &run, id, call
}

// TestApprovalExpiresWithoutAnAnswer pins the proactive half of M-15. The card
// promises the owner five minutes; before this the deadline was only checked on
// the owner's click, so an approval nobody ever answered parked the run until
// its own duration budget ran out — twice the advertised window.
func TestApprovalExpiresWithoutAnAnswer(t *testing.T) {
	ctx := context.Background()
	bridge, run, id, call := approvalWaitFixture(t, "expiry", 150*time.Millisecond)

	type outcome struct {
		approved bool
		err      error
	}
	finished := make(chan outcome, 1)
	go func() {
		approved, err := (desktopApprovalHandler{bridge: bridge}).Approve(
			withApprovalRunState(ctx, run), agent.ApprovalRequest{RunID: run.ID, Call: call})
		finished <- outcome{approved: approved, err: err}
	}()

	select {
	case value := <-finished:
		if value.err != nil || value.approved {
			t.Fatalf("unanswered approval = %v, err = %v", value.approved, value.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unanswered approval never expired; the run is blocked until its duration budget")
	}

	stored, err := bridge.repositories.Approvals.Get(ctx, id)
	if err != nil || stored.Decision != domain.ApprovalExpired || stored.DecidedBy != domain.ActorSystem {
		t.Fatalf("stored approval = %#v, err = %v", stored, err)
	}
	audits, err := bridge.repositories.Audit.ListByRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	expired := false
	for _, event := range audits {
		if event.Action == "approval.expired" {
			expired = true
		}
	}
	if !expired {
		t.Fatalf("expiry left no audit trail: %#v", audits)
	}
	bridge.mu.RLock()
	leaked := bridge.approvals[string(id)]
	bridge.mu.RUnlock()
	if leaked != nil {
		t.Fatal("expired approval kept its gate registered")
	}
	// The tool call is refused, not aborted, so the run has to be running again.
	if run.State != domain.RunStateRunning {
		t.Fatalf("run after expiry = %s, want %s", run.State, domain.RunStateRunning)
	}
}

// TestApprovalWaitRecordsWaitingApprovalState pins the third part of M-15. The
// state existed in the domain model and failChatRun already branched on it, but
// no desktop path ever set it, so a run parked on an approval card looked, in
// durable state, exactly like one still talking to the provider.
func TestApprovalWaitRecordsWaitingApprovalState(t *testing.T) {
	ctx := context.Background()
	bridge, run, id, call := approvalWaitFixture(t, "waiting", 30*time.Second)

	// In production Approve runs on the run goroutine itself, so the run struct
	// has a single owner. Driving it from a second goroutine here means the test
	// may only touch the struct after the channel hands ownership back; the
	// durable row is the safe thing to poll in the meantime.
	runID := run.ID
	finished := make(chan bool, 1)
	go func() {
		approved, err := (desktopApprovalHandler{bridge: bridge}).Approve(
			withApprovalRunState(ctx, run), agent.ApprovalRequest{RunID: run.ID, Call: call})
		if err != nil {
			t.Error(err)
		}
		finished <- approved
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		stored, err := bridge.repositories.Runs.Get(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.State == domain.RunStateWaitingApproval {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("run blocked on the owner was never recorded as waiting_approval: %s", stored.State)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := bridge.ResolveApproval(approvalDecisionInput{ApprovalID: string(id), Decision: "approve"}); err != nil {
		t.Fatal(err)
	}
	select {
	case approved := <-finished:
		if !approved {
			t.Fatal("approved decision was not delivered")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("approval wait did not finish after the owner answered")
	}

	stored, err := bridge.repositories.Runs.Get(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != domain.RunStateRunning {
		t.Fatalf("run after the decision = %s, want %s", stored.State, domain.RunStateRunning)
	}
	// The in-memory run the chat goroutine keeps has to stay version-aligned
	// with the row, or its own completing transition would lose the optimistic
	// version race and fail a run that actually succeeded.
	if run.State != domain.RunStateRunning || run.Version != stored.Version {
		t.Fatalf("in-memory run = %s v%d, stored = %s v%d", run.State, run.Version, stored.State, stored.Version)
	}
}
