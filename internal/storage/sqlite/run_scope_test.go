package sqlite

import (
	"errors"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestRunRepositoryPersistsAndFiltersAgentOwnership(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	for _, conversation := range []Conversation{
		{ID: "conversation-run-a", AgentID: "agent-a", Title: "A", CreatedAt: now, UpdatedAt: now},
		{ID: "conversation-run-b", AgentID: "agent-b", Title: "B", CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)},
	} {
		if err := repositories.Conversations.Create(ctx, conversation); err != nil {
			t.Fatal(err)
		}
	}

	runA, err := domain.NewRunForAgent("agent-a", "run-a", domain.RunKindInteractive, "conversation-run-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Create(ctx, runA); err != nil {
		t.Fatalf("create agent A run: %v", err)
	}
	runB, err := domain.NewRunForAgent("agent-b", "run-b", domain.RunKindBackground, "conversation-run-b", now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Create(ctx, runB); err != nil {
		t.Fatalf("create agent B run: %v", err)
	}

	// Callers from before Stage 8.2 can still use NewRun. The repository
	// resolves the owner from the conversation before crossing the DB boundary.
	legacy, err := domain.NewRun("run-legacy-a", domain.RunKindInteractive, "conversation-run-a", now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Create(ctx, legacy); err != nil {
		t.Fatalf("create legacy run: %v", err)
	}
	storedLegacy, err := repositories.Runs.Get(ctx, legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedLegacy.AgentID != "agent-a" {
		t.Fatalf("legacy stored AgentID = %q, want agent-a", storedLegacy.AgentID)
	}

	storedA, err := repositories.Runs.Get(ctx, runA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedA.AgentID != "agent-a" {
		t.Fatalf("stored AgentID = %q, want agent-a", storedA.AgentID)
	}
	aRuns, err := repositories.Runs.ListByAgent(ctx, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(aRuns) != 2 || aRuns[0].ID != runA.ID || aRuns[1].ID != legacy.ID {
		t.Fatalf("agent-a runs = %#v", aRuns)
	}
	bRuns, err := repositories.Runs.ListByAgent(ctx, "agent-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(bRuns) != 1 || bRuns[0].ID != runB.ID {
		t.Fatalf("agent-b runs = %#v", bRuns)
	}
	if _, err := repositories.Runs.ListByAgent(ctx, ""); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("empty ListByAgent error = %v", err)
	}
}

func TestRunRepositoryRejectsCrossAgentOwnership(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, conversation := range []Conversation{
		{ID: "conversation-owner-a", AgentID: "agent-a", Title: "A", CreatedAt: now, UpdatedAt: now},
		{ID: "conversation-owner-b", AgentID: "agent-b", Title: "B", CreatedAt: now, UpdatedAt: now},
	} {
		if err := repositories.Conversations.Create(ctx, conversation); err != nil {
			t.Fatal(err)
		}
	}
	wrongConversation, err := domain.NewRunForAgent("agent-b", "run-wrong-conversation", domain.RunKindInteractive, "conversation-owner-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Create(ctx, wrongConversation); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cross-agent conversation error = %v, want ErrConflict", err)
	}

	parent, err := domain.NewRunForAgent("agent-b", "run-parent-b", domain.RunKindBackground, "conversation-owner-b", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Create(ctx, parent); err != nil {
		t.Fatal(err)
	}
	wrongParent, err := domain.NewRunForAgent("agent-a", "run-wrong-parent", domain.RunKindSubagent, "", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	wrongParent.ParentRunID = parent.ID
	if err := repositories.Runs.Create(ctx, wrongParent); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cross-agent parent error = %v, want ErrConflict", err)
	}

	withoutOwner, err := domain.NewRun("run-without-owner", domain.RunKindBackground, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Runs.Create(ctx, withoutOwner); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("run without owner error = %v, want ErrInvalidArgument", err)
	}
}

func TestRunAgentDatabaseTriggersRejectMissingAndMismatchedOwners(t *testing.T) {
	database, ctx := testDatabase(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := database.ExecContext(ctx, `
		INSERT INTO agent_runs(id, kind, state, version, created_at, updated_at)
		VALUES ('raw-run-missing-agent', 'interactive', 'created', 1, ?, ?)`, now, now); err == nil {
		t.Fatal("agent_runs trigger accepted missing agent_id")
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO conversations(id, agent_id, title, created_at, updated_at)
		VALUES ('raw-run-conversation-a', 'agent-a', 'A', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO agent_runs(id, agent_id, kind, conversation_id, state, version, created_at, updated_at)
		VALUES ('raw-run-wrong-agent', 'agent-b', 'interactive', 'raw-run-conversation-a', 'created', 1, ?, ?)`, now, now); err == nil {
		t.Fatal("agent_runs trigger accepted mismatched conversation owner")
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO agent_runs(id, agent_id, kind, state, version, created_at, updated_at)
		VALUES ('raw-run-root', 'agent-a', 'interactive', 'created', 1, ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO agent_runs(id, agent_id, kind, conversation_id, parent_run_id, state, version, created_at, updated_at)
		VALUES ('raw-run-subagent-conversation', 'agent-a', 'subagent', 'raw-run-conversation-a', 'raw-run-root', 'created', 1, ?, ?)`, now, now); err == nil {
		t.Fatal("agent_runs trigger accepted subagent conversation")
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO agent_runs(id, agent_id, kind, parent_run_id, state, version, created_at, updated_at)
		VALUES ('raw-run-non-subagent-child', 'agent-a', 'background', 'raw-run-root', 'created', 1, ?, ?)`, now, now); err == nil {
		t.Fatal("agent_runs trigger accepted non-subagent parent")
	}
}
