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

func TestDesktopToolAuthorizerEnforcesRiskBoundary(t *testing.T) {
	authorizer := desktopToolAuthorizer{}
	tests := []struct {
		risk     domain.RiskLevel
		decision domain.PermissionDecision
	}{
		{domain.RiskLow, domain.PermissionAllow},
		{domain.RiskMedium, domain.PermissionNeedsApproval},
		{domain.RiskHigh, domain.PermissionNeedsApproval},
		{domain.RiskCritical, domain.PermissionDeny},
	}
	for _, test := range tests {
		result, err := authorizer.Authorize(context.Background(), agent.ToolAuthorizationRequest{
			Tool: agent.ToolDescriptor{Name: "test", InputSchema: []byte(`{"type":"object"}`), Risk: test.risk},
		})
		if err != nil || result.Decision != test.decision {
			t.Fatalf("risk %s decision = %s, %v; want %s", test.risk, result.Decision, err, test.decision)
		}
	}
}

func TestDesktopApprovalHandlerExpiresStaleDecision(t *testing.T) {
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
	conversationID := domain.ID("conversation-expired")
	runID := domain.ID("run-expired")
	if err := repositories.Conversations.Create(ctx, storage.Conversation{ID: conversationID, AgentID: "owner", Title: "Expired", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewRun(runID, domain.RunKindInteractive, conversationID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	call := agent.ToolCall{ID: "call-expired", Name: "filesystem.write", Arguments: []byte(`{"operation":"create"}`)}
	id := approvalIDFor(runID, call.ID)
	approval, err := domain.NewApproval(id, runID, "hash", "write", domain.RiskMedium, domain.CapabilityScope{Kind: domain.ScopeResource, Values: []string{"filesystem.write"}}, now.Add(-10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	approval.ToolID = "filesystem.write"
	approval.ExpiresAt = now.Add(-time.Minute)
	if err := repositories.Approvals.Create(ctx, approval); err != nil {
		t.Fatal(err)
	}
	decisions := make(chan approvalResolution, 1)
	decisions <- approvalResolution{decision: "approve"}
	bridge := &Bridge{repositories: repositories, database: database, approvals: map[string]*approvalGate{
		string(id): {decision: decisions, resolved: true},
	}}
	approved, err := (desktopApprovalHandler{bridge: bridge}).Approve(ctx, agent.ApprovalRequest{RunID: runID, Call: call})
	if err != nil {
		t.Fatal(err)
	}
	if approved {
		t.Fatal("expired approval was accepted")
	}
	stored, err := repositories.Approvals.Get(ctx, id)
	if err != nil || stored.Decision != domain.ApprovalExpired {
		t.Fatalf("stored approval = %#v, err = %v", stored, err)
	}
}
