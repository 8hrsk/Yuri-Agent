package sqlite

import (
	"errors"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestDelegationPersistsBoundedAnonymousChildMetadata(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 14, 0, 0, 0, time.UTC)
	conversation := Conversation{ID: "delegation-conversation", AgentID: "owner", Title: "delegation", CreatedAt: now, UpdatedAt: now}
	if err := repositories.Conversations.Create(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	parent, err := domain.NewRunForAgent("owner", "parent-run", domain.RunKindInteractive, conversation.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Create(ctx, parent); err != nil {
		t.Fatal(err)
	}
	child, err := domain.NewRunForAgent("owner", "child-run", domain.RunKindSubagent, "", now)
	if err != nil {
		t.Fatal(err)
	}
	child.ParentRunID = parent.ID
	if err := repositories.Runs.Create(ctx, child); err != nil {
		t.Fatal(err)
	}
	delegation, err := domain.NewDelegation("delegation-1", child.ID, "owner", parent.ID, `{"task":"summarize"}`, "request-1", "hash-1", now)
	if err != nil {
		t.Fatal(err)
	}
	delegation.Budget.MaxSteps = 4
	if err := repositories.Delegations.Create(ctx, delegation); err != nil {
		t.Fatal(err)
	}
	loaded, err := repositories.Delegations.GetForPrincipal(ctx, "owner", delegation.ID)
	if err != nil || loaded.ChildRunID != child.ID || loaded.Depth != 1 || loaded.ScopeJSON != delegation.ScopeJSON {
		t.Fatalf("loaded delegation = %#v, err = %v", loaded, err)
	}
	if _, err := repositories.Delegations.GetForPrincipal(ctx, "other-agent", delegation.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-principal get error = %v", err)
	}
	if _, err := repositories.Delegations.FindByIdempotencyKey(ctx, "owner", parent.ID, "request-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.Delegations.FindByIdempotencyKey(ctx, "owner", parent.ID, "request-1"); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Delegations.Create(ctx, delegation); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate idempotency error = %v", err)
	}
	if err := delegation.Transition(domain.DelegationStatusQueued, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Delegations.Save(ctx, delegation); err != nil {
		t.Fatal(err)
	}
	if got, err := repositories.Delegations.ListByParent(ctx, "owner", parent.ID); err != nil || len(got) != 1 || got[0].Status != domain.DelegationStatusQueued {
		t.Fatalf("list delegations = %#v, err = %v", got, err)
	}
}

func TestDelegationRejectsWrongParentOwnerDepthAndChildKind(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	conversation := Conversation{ID: "delegation-owner-conversation", AgentID: "owner", Title: "delegation", CreatedAt: now, UpdatedAt: now}
	if err := repositories.Conversations.Create(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	parent, _ := domain.NewRunForAgent("owner", "owner-parent", domain.RunKindInteractive, conversation.ID, now)
	if err := repositories.Runs.Create(ctx, parent); err != nil {
		t.Fatal(err)
	}
	child, _ := domain.NewRunForAgent("owner", "owner-child", domain.RunKindSubagent, "", now)
	child.ParentRunID = parent.ID
	if err := repositories.Runs.Create(ctx, child); err != nil {
		t.Fatal(err)
	}
	badDepth, _ := domain.NewDelegation("bad-depth", child.ID, "owner", parent.ID, `{}`, "bad-depth", "hash-depth", now)
	badDepth.Depth = 2
	if err := repositories.Delegations.Create(ctx, badDepth); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("depth error = %v", err)
	}
	badPrincipal, _ := domain.NewDelegation("bad-principal", child.ID, "other-agent", parent.ID, `{}`, "bad-principal", "hash-principal", now)
	if err := repositories.Delegations.Create(ctx, badPrincipal); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("principal error = %v", err)
	}
	wrongKind, _ := domain.NewRunForAgent("owner", "owner-not-child", domain.RunKindBackground, conversation.ID, now)
	if err := repositories.Runs.Create(ctx, wrongKind); err != nil {
		t.Fatal(err)
	}
	badChild, _ := domain.NewDelegation("bad-child", wrongKind.ID, "owner", parent.ID, `{}`, "bad-child", "hash-child", now)
	if err := repositories.Delegations.Create(ctx, badChild); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("child kind error = %v", err)
	}
}

func TestCreateAndSaveDelegationWithChildIsAtomic(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	conversation := Conversation{ID: "atomic-conversation", AgentID: "owner", Title: "atomic", CreatedAt: now, UpdatedAt: now}
	if err := repositories.Conversations.Create(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	parent, err := domain.NewRunForAgent("owner", "atomic-parent", domain.RunKindInteractive, conversation.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Create(ctx, parent); err != nil {
		t.Fatal(err)
	}
	child, err := domain.NewRunForAgent("owner", "atomic-child", domain.RunKindSubagent, "", now)
	if err != nil {
		t.Fatal(err)
	}
	child.ParentRunID = parent.ID
	delegation, err := domain.NewDelegation("atomic-delegation", child.ID, "owner", parent.ID, `{"task":"summarize"}`, "atomic-key", "canonical-hash-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.CreateDelegationWithChild(ctx, child, delegation); err != nil {
		t.Fatal(err)
	}
	loadedChild, err := repositories.Runs.Get(ctx, child.ID)
	if err != nil || loadedChild.Kind != domain.RunKindSubagent || loadedChild.ConversationID != "" {
		t.Fatalf("loaded child = %#v, err = %v", loadedChild, err)
	}
	loadedDelegation, err := repositories.Delegations.Get(ctx, delegation.ID)
	if err != nil || loadedDelegation.RequestHash != "canonical-hash-1" {
		t.Fatalf("loaded delegation = %#v, err = %v", loadedDelegation, err)
	}

	if err := child.Transition(domain.RunStateQueued, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := delegation.Transition(domain.DelegationStatusQueued, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repositories.SaveDelegationWithChild(ctx, child, delegation); err != nil {
		t.Fatal(err)
	}
	if err := child.Transition(domain.RunStateRunning, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := delegation.Transition(domain.DelegationStatusRunning, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repositories.SaveDelegationWithChild(ctx, child, delegation); err != nil {
		t.Fatal(err)
	}
	if err := child.Transition(domain.RunStateCompleted, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := delegation.Transition(domain.DelegationStatusCompleted, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	delegation.ResultText = "summary"
	if err := repositories.SaveDelegationWithChild(ctx, child, delegation); err != nil {
		t.Fatal(err)
	}
	if got, err := repositories.Delegations.Get(ctx, delegation.ID); err != nil || got.Status != domain.DelegationStatusCompleted || got.ResultText != "summary" {
		t.Fatalf("terminal delegation = %#v, err = %v", got, err)
	}
}

func TestCreateDelegationWithChildRollsBackChildOnConflict(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	conversation := Conversation{ID: "rollback-conversation", AgentID: "owner", Title: "rollback", CreatedAt: now, UpdatedAt: now}
	if err := repositories.Conversations.Create(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	parent, _ := domain.NewRunForAgent("owner", "rollback-parent", domain.RunKindInteractive, conversation.ID, now)
	if err := repositories.Runs.Create(ctx, parent); err != nil {
		t.Fatal(err)
	}
	firstChild, _ := domain.NewRunForAgent("owner", "rollback-child-1", domain.RunKindSubagent, "", now)
	firstChild.ParentRunID = parent.ID
	first, _ := domain.NewDelegation("rollback-delegation-1", firstChild.ID, "owner", parent.ID, `{}`, "same-key", "hash-a", now)
	if err := repositories.CreateDelegationWithChild(ctx, firstChild, first); err != nil {
		t.Fatal(err)
	}
	secondChild, _ := domain.NewRunForAgent("owner", "rollback-child-2", domain.RunKindSubagent, "", now.Add(time.Second))
	secondChild.ParentRunID = parent.ID
	second, _ := domain.NewDelegation("rollback-delegation-2", secondChild.ID, "owner", parent.ID, `{}`, "same-key", "hash-b", now.Add(time.Second))
	if err := repositories.CreateDelegationWithChild(ctx, secondChild, second); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate request error = %v", err)
	}
	if _, err := repositories.Runs.Get(ctx, secondChild.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("orphan child lookup error = %v", err)
	}
}
