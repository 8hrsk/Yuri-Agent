package app

import (
	"context"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func newTestService(t *testing.T) (*Service, *InMemoryRunRepository, *InMemoryApprovalRepository, *InMemoryEventBus) {
	t.Helper()
	runs := NewInMemoryRunRepository()
	approvals := NewInMemoryApprovalRepository()
	events := NewInMemoryEventBus()
	ids := &domain.StaticIDGenerator{IDs: []domain.ID{"run-generated", "event-created", "approval-generated", "event-approval", "event-state", "event-resolved", "event-cancel"}}
	service, err := NewService(Dependencies{
		Runs: runs, Approvals: approvals, Events: events,
		Clock: domain.FixedClock{At: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)}, IDs: ids,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, runs, approvals, events
}

func TestServiceLifecyclePublishesEvents(t *testing.T) {
	service, _, _, events := newTestService(t)
	ctx := context.Background()
	seen := make(chan domain.EventType, 3)
	for _, eventType := range []domain.EventType{domain.EventRunCreated, domain.EventRunStateChanged} {
		if _, err := events.Subscribe(ctx, eventType, func(_ context.Context, event domain.Event) error {
			seen <- event.Type
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	run, err := service.CreateRun(ctx, CreateRunInput{Kind: domain.RunKindInteractive, ConversationID: "conversation-1"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	if run.ID != "run-generated" || run.State != domain.RunStateCreated {
		t.Fatalf("run = %#v", run)
	}
	if _, err := service.TransitionRun(ctx, TransitionRunInput{RunID: run.ID, State: domain.RunStateQueued}); err != nil {
		t.Fatalf("TransitionRun() error = %v", err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-seen:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for event")
		}
	}
}

func TestServiceApprovalLifecycle(t *testing.T) {
	service, _, approvals, events := newTestService(t)
	ctx := context.Background()
	run, err := service.CreateRun(ctx, CreateRunInput{ID: "run-1", Kind: domain.RunKindInteractive, ConversationID: "conversation-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []domain.RunState{domain.RunStateQueued, domain.RunStateRunning} {
		if _, err := service.TransitionRun(ctx, TransitionRunInput{RunID: run.ID, State: state}); err != nil {
			t.Fatal(err)
		}
	}
	approval, err := service.RequestApproval(ctx, RequestApprovalInput{
		RunID: run.ID, ActionHash: "sha256:abc", Action: "write file", ToolID: "filesystem.write",
		Risk: domain.RiskMedium, Scope: domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: []string{"/tmp/project"}},
	})
	if err != nil {
		t.Fatalf("RequestApproval() error = %v", err)
	}
	storedRun, err := service.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedRun.State != domain.RunStateWaitingApproval {
		t.Fatalf("run state = %s", storedRun.State)
	}
	resolved, err := service.ResolveApproval(ctx, approval.ID, domain.ApprovalApproved, domain.ActorUser, "yes")
	if err != nil {
		t.Fatalf("ResolveApproval() error = %v", err)
	}
	if resolved.Decision != domain.ApprovalApproved {
		t.Fatalf("approval = %#v", resolved)
	}
	items, err := approvals.ListByRun(ctx, run.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("ListByRun() = %#v, %v", items, err)
	}
	_ = events
}
