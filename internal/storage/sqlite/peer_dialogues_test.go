package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

type peerDialogueFixture struct {
	database *sql.DB
	ctx      context.Context
	repos    *Repositories
	now      time.Time
	profiles map[domain.ID]domain.AgentProfile
	runs     map[domain.ID]domain.AgentRun
}

func newPeerDialogueFixture(t *testing.T, agentIDs ...domain.ID) *peerDialogueFixture {
	t.Helper()
	database, ctx := testDatabase(t)
	repos, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &peerDialogueFixture{
		database: database,
		ctx:      ctx,
		repos:    repos,
		now:      time.Date(2026, 8, 29, 20, 0, 0, 0, time.UTC),
		profiles: make(map[domain.ID]domain.AgentProfile),
		runs:     make(map[domain.ID]domain.AgentRun),
	}
	for index, agentID := range agentIDs {
		profile, err := domain.NewAgentProfile(agentID, "Agent "+string(agentID), 20+index, "female", "", fixture.now.Add(time.Duration(index)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if err := repos.Agents.Create(ctx, profile); err != nil {
			t.Fatal(err)
		}
		fixture.profiles[agentID] = profile

		conversation := Conversation{
			ID:        domain.ID("conversation-" + string(agentID)),
			AgentID:   agentID,
			Title:     "Peer fixture " + string(agentID),
			CreatedAt: fixture.now,
			UpdatedAt: fixture.now,
		}
		if err := repos.Conversations.Create(ctx, conversation); err != nil {
			t.Fatal(err)
		}
		run, err := domain.NewRunForAgent(agentID, domain.ID("trigger-"+string(agentID)), domain.RunKindBackground, conversation.ID, fixture.now)
		if err != nil {
			t.Fatal(err)
		}
		run.Budget = domain.RunBudget{MaxSteps: 4, MaxTokens: 2000, MaxToolOutputBytes: 4096, MaxDurationSeconds: 60}
		if err := repos.Runs.Create(ctx, run); err != nil {
			t.Fatal(err)
		}
		fixture.runs[agentID] = run
	}
	return fixture
}

func (f *peerDialogueFixture) newDialogue(t *testing.T, id string, initiator, peer domain.ID, trigger domain.ID, key string, at time.Time) (domain.PeerDialogue, domain.PeerDialogueMessage) {
	t.Helper()
	budget := domain.PeerDialogueBudget{MaxTurns: 3, MaxTokens: 4000, MaxDurationSeconds: 60, CooldownSeconds: 120}
	dialogue, err := domain.NewPeerDialogue(domain.ID(id), initiator, peer, trigger, "сверить план", key, "sha256:"+id, budget, at)
	if err != nil {
		t.Fatal(err)
	}
	initial := domain.PeerDialogueMessage{
		ID:               domain.ID("message-" + id + "-0"),
		DialogueID:       dialogue.ID,
		Sequence:         0,
		SenderAgentID:    initiator,
		RecipientAgentID: peer,
		SourceRunID:      trigger,
		Content:          "Проверь эту задачу.",
		CreatedAt:        at,
	}
	return dialogue, initial
}

func TestPeerDialoguePersistsOwnerRecommendationProvenance(t *testing.T) {
	fixture := newPeerDialogueFixture(t, "agent-a", "agent-b")
	dialogue, initial := fixture.newDialogue(t, "dialogue-recommended", "agent-a", "agent-b", fixture.runs["agent-a"].ID, "request-recommended", fixture.now)
	snapshot := domain.PeerBudgetRecommendationSnapshot{
		Budget: dialogue.Budget, Basis: domain.PeerBudgetRecommendationSimilarHistory, SampleCount: 3,
	}
	if err := dialogue.SetOwnerBudgetProvenance(domain.PeerDialogueBudgetOwnerRecommendation, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repos.CreatePeerDialogueWithMessage(fixture.ctx, dialogue, initial); err != nil {
		t.Fatal(err)
	}
	loaded, err := fixture.repos.PeerDialogues.Get(fixture.ctx, dialogue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.BudgetOrigin != domain.PeerDialogueBudgetOwnerRecommendation || loaded.Recommendation != snapshot {
		t.Fatalf("stored recommendation provenance = %#v", loaded)
	}
}

func TestPeerDialogueAggregateScopesParticipantsAndAppendsAlternatingTurns(t *testing.T) {
	fixture := newPeerDialogueFixture(t, "agent-a", "agent-b", "agent-c")
	dialogue, initial := fixture.newDialogue(t, "dialogue-1", "agent-a", "agent-b", fixture.runs["agent-a"].ID, "request-1", fixture.now)
	if err := fixture.repos.CreatePeerDialogueWithMessage(fixture.ctx, dialogue, initial); err != nil {
		t.Fatal(err)
	}

	loaded, err := fixture.repos.PeerDialogues.Get(fixture.ctx, dialogue.ID)
	if err != nil || loaded.PairKey != domain.AgentPairKey("agent-b", "agent-a") {
		t.Fatalf("loaded dialogue = %#v, err = %v", loaded, err)
	}
	if _, err := fixture.repos.PeerDialogues.GetForParticipant(fixture.ctx, "agent-c", dialogue.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-participant get error = %v", err)
	}
	if _, err := fixture.repos.PeerDialogues.GetForParticipant(fixture.ctx, "agent-a", dialogue.ID); err != nil {
		t.Fatal(err)
	}
	if listed, err := fixture.repos.PeerDialogues.ListByParticipant(fixture.ctx, "agent-b"); err != nil || len(listed) != 1 || listed[0].ID != dialogue.ID {
		t.Fatalf("participant list = %#v, err = %v", listed, err)
	}
	if listed, err := fixture.repos.PeerDialogues.ListByParticipant(fixture.ctx, "agent-c"); err != nil || len(listed) != 0 {
		t.Fatalf("unrelated participant list = %#v, err = %v", listed, err)
	}

	messages, err := fixture.repos.PeerDialogueMessages.ListByDialogue(fixture.ctx, dialogue.ID, "agent-a")
	if err != nil || len(messages) != 1 || messages[0].Sequence != 0 || messages[0].Content != initial.Content {
		t.Fatalf("initial messages = %#v, err = %v", messages, err)
	}
	if _, err := fixture.repos.PeerDialogueMessages.ListByDialogue(fixture.ctx, dialogue.ID, "agent-c"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-participant message list error = %v", err)
	}

	if err := dialogue.Transition(domain.PeerDialogueRunning, fixture.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repos.PeerDialogues.Save(fixture.ctx, dialogue); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.RecordTurn(250, fixture.now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	turnOne := domain.PeerDialogueMessage{
		ID:               "message-dialogue-1-1",
		DialogueID:       dialogue.ID,
		Sequence:         1,
		SenderAgentID:    "agent-b",
		RecipientAgentID: "agent-a",
		SourceRunID:      fixture.runs["agent-b"].ID,
		Content:          "Согласна, добавлю проверку.",
		CreatedAt:        fixture.now.Add(2 * time.Second),
	}
	if err := fixture.repos.AppendPeerDialogueTurn(fixture.ctx, dialogue, turnOne); err != nil {
		t.Fatal(err)
	}
	if got, err := fixture.repos.PeerDialogues.Get(fixture.ctx, dialogue.ID); err != nil || got.TurnCount != 1 || got.TokensUsed != 250 || got.Version != dialogue.Version {
		t.Fatalf("after first turn = %#v, err = %v", got, err)
	}

	if err := dialogue.RecordTurn(180, fixture.now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	turnTwo := domain.PeerDialogueMessage{
		ID:               "message-dialogue-1-2",
		DialogueID:       dialogue.ID,
		Sequence:         2,
		SenderAgentID:    "agent-a",
		RecipientAgentID: "agent-b",
		SourceRunID:      fixture.runs["agent-a"].ID,
		Content:          "Отлично.",
		CreatedAt:        fixture.now.Add(3 * time.Second),
	}
	badTurn := turnTwo
	badTurn.ID = "message-dialogue-1-2-bad"
	badTurn.SourceRunID = fixture.runs["agent-b"].ID
	if err := fixture.repos.AppendGeneratedTurn(fixture.ctx, dialogue, badTurn); err == nil || !strings.Contains(strings.ToLower(err.Error()), "source run") {
		t.Fatalf("bad generated source error = %v", err)
	}
	if got, err := fixture.repos.PeerDialogues.Get(fixture.ctx, dialogue.ID); err != nil || got.TurnCount != 1 || got.Version != 3 {
		t.Fatalf("atomic append rollback = %#v, err = %v", got, err)
	}
	// The failed append did not persist the optimistic mutation, so continue
	// from the durable aggregate before retrying the valid turn.
	dialogue, err = fixture.repos.PeerDialogues.Get(fixture.ctx, dialogue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := dialogue.RecordTurn(180, fixture.now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	turnTwo.CreatedAt = fixture.now.Add(3 * time.Second)
	if err := fixture.repos.AppendGeneratedTurn(fixture.ctx, dialogue, turnTwo); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.Transition(domain.PeerDialogueCompleted, fixture.now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repos.PeerDialogues.Save(fixture.ctx, dialogue); err != nil {
		t.Fatal(err)
	}
	messages, err = fixture.repos.PeerDialogueMessages.ListByDialogue(fixture.ctx, dialogue.ID, "agent-b")
	if err != nil || len(messages) != 3 || messages[1].SenderAgentID != "agent-b" || messages[2].SenderAgentID != "agent-a" {
		t.Fatalf("alternating messages = %#v, err = %v", messages, err)
	}

	if found, err := fixture.repos.PeerDialogues.FindByIdempotencyKey(fixture.ctx, "agent-a", fixture.runs["agent-a"].ID, "request-1"); err != nil || found.ID != dialogue.ID {
		t.Fatalf("idempotency lookup = %#v, err = %v", found, err)
	}
	if recent, err := fixture.repos.PeerDialogues.HasRecentPair(fixture.ctx, dialogue.PairKey, fixture.now.Add(-time.Second)); err != nil || !recent {
		t.Fatalf("recent pair = %v, err = %v", recent, err)
	}
	if recent, err := fixture.repos.PeerDialogues.HasRecentPair(fixture.ctx, dialogue.PairKey, fixture.now.Add(time.Second)); err != nil || recent {
		t.Fatalf("future pair = %v, err = %v", recent, err)
	}

	// Audit is deliberately untouched by the peer-dialogue aggregate. The
	// content is available only through the participant-scoped message reader.
	var auditCount int
	if err := fixture.database.QueryRowContext(fixture.ctx, "SELECT COUNT(*) FROM audit_events").Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 0 {
		t.Fatalf("peer dialogue unexpectedly wrote audit rows: %d", auditCount)
	}
}

func TestPeerDialogueRejectsActivePairDuplicateAndScopedIdempotency(t *testing.T) {
	fixture := newPeerDialogueFixture(t, "agent-a", "agent-b", "agent-c")
	first, initial := fixture.newDialogue(t, "dialogue-active", "agent-a", "agent-b", fixture.runs["agent-a"].ID, "same-key", fixture.now)
	if err := fixture.repos.CreatePeerDialogueWithMessage(fixture.ctx, first, initial); err != nil {
		t.Fatal(err)
	}
	reverse, reverseInitial := fixture.newDialogue(t, "dialogue-reverse", "agent-b", "agent-a", fixture.runs["agent-b"].ID, "reverse-key", fixture.now.Add(time.Second))
	if err := fixture.repos.CreatePeerDialogueWithMessage(fixture.ctx, reverse, reverseInitial); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("reverse active pair error = %v", err)
	}
	if _, err := fixture.repos.PeerDialogues.Get(fixture.ctx, reverse.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("reverse orphan lookup = %v", err)
	}
	if err := first.Transition(domain.PeerDialogueRunning, fixture.now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repos.PeerDialogues.Save(fixture.ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := first.RecordTurn(50, fixture.now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repos.AppendPeerDialogueTurn(fixture.ctx, first, domain.PeerDialogueMessage{
		ID: "message-dialogue-active-1", DialogueID: first.ID, Sequence: 1,
		SenderAgentID: "agent-b", RecipientAgentID: "agent-a", SourceRunID: fixture.runs["agent-b"].ID,
		Content: "Подтверждаю.", CreatedAt: fixture.now.Add(3 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.Transition(domain.PeerDialogueCompleted, fixture.now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repos.PeerDialogues.Save(fixture.ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repos.CreatePeerDialogueWithMessage(fixture.ctx, reverse, reverseInitial); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cooldown pair error = %v", err)
	}

	secondTrigger, err := domain.NewRunForAgent("agent-a", "trigger-agent-a-2", domain.RunKindBackground, "conversation-agent-a", fixture.now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.repos.Runs.Create(fixture.ctx, secondTrigger); err != nil {
		t.Fatal(err)
	}
	// A different trigger run is a different idempotency scope, even for the
	// same initiator. The pair is different here so the active-pair index does
	// not obscure that invariant.
	third, thirdInitial := fixture.newDialogue(t, "dialogue-scoped", "agent-a", "agent-c", secondTrigger.ID, "same-key", fixture.now.Add(2*time.Second))
	if err := fixture.repos.CreatePeerDialogueWithMessage(fixture.ctx, third, thirdInitial); err != nil {
		t.Fatal(err)
	}
	duplicate, duplicateInitial := fixture.newDialogue(t, "dialogue-duplicate", "agent-a", "agent-c", fixture.runs["agent-a"].ID, "same-key", fixture.now.Add(3*time.Second))
	if err := fixture.repos.CreatePeerDialogueWithMessage(fixture.ctx, duplicate, duplicateInitial); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate scoped idempotency error = %v", err)
	}
	if _, err := fixture.repos.PeerDialogues.Get(fixture.ctx, duplicate.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("duplicate orphan lookup = %v", err)
	}
}

func TestPeerDialogueAutonomousLedgerAndTriggerRunLookup(t *testing.T) {
	fixture := newPeerDialogueFixture(t, "agent-a", "agent-b", "agent-c")
	automatic, automaticInitial := fixture.newDialogue(t, "dialogue-auto", "agent-a", "agent-b", fixture.runs["agent-a"].ID, "auto:run", fixture.now)
	if err := automatic.MarkAutonomous("Нужна независимая проверка."); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repos.CreatePeerDialogueWithMessage(fixture.ctx, automatic, automaticInitial); err != nil {
		t.Fatal(err)
	}
	explicit, explicitInitial := fixture.newDialogue(t, "dialogue-tool", "agent-a", "agent-c", fixture.runs["agent-a"].ID, "tool:run", fixture.now.Add(time.Second))
	if err := fixture.repos.CreatePeerDialogueWithMessage(fixture.ctx, explicit, explicitInitial); err != nil {
		t.Fatal(err)
	}

	items, err := fixture.repos.PeerDialogues.ListAutonomousByInitiator(fixture.ctx, "agent-a", fixture.now.Add(-time.Minute), 10)
	if err != nil || len(items) != 1 || items[0].ID != automatic.ID || items[0].TriggerReason != automatic.TriggerReason {
		t.Fatalf("autonomous ledger = %#v, err = %v", items, err)
	}
	if _, err := fixture.database.ExecContext(fixture.ctx, `UPDATE peer_dialogues SET trigger_reason = 'подменено' WHERE id = ?`, automatic.ID.String()); err == nil || !strings.Contains(strings.ToLower(err.Error()), "immutable") {
		t.Fatalf("trigger provenance update error = %v", err)
	}
	exists, err := fixture.repos.PeerDialogues.HasByTriggerRun(fixture.ctx, "agent-a", fixture.runs["agent-a"].ID)
	if err != nil || !exists {
		t.Fatalf("trigger-run lookup = %v, err = %v", exists, err)
	}
	exists, err = fixture.repos.PeerDialogues.HasByTriggerRun(fixture.ctx, "agent-b", fixture.runs["agent-a"].ID)
	if err != nil || exists {
		t.Fatalf("cross-initiator trigger-run lookup = %v, err = %v", exists, err)
	}
}

func TestPeerDialogueSQLiteTriggersValidateRootSourceAndAlternation(t *testing.T) {
	fixture := newPeerDialogueFixture(t, "agent-a", "agent-b", "agent-c")
	dialogue, initial := fixture.newDialogue(t, "dialogue-trigger", "agent-a", "agent-b", fixture.runs["agent-a"].ID, "trigger-key", fixture.now)
	if err := fixture.repos.CreatePeerDialogueWithMessage(fixture.ctx, dialogue, initial); err != nil {
		t.Fatal(err)
	}

	_, err := fixture.database.ExecContext(fixture.ctx, `
		INSERT INTO peer_dialogue_messages(
			id, dialogue_id, sequence, sender_agent_id, recipient_agent_id, source_run_id, content, created_at
		) VALUES ('bad-participants', ?, 1, 'agent-a', 'agent-b', ?, 'bad', ?)`,
		dialogue.ID, fixture.runs["agent-a"].ID, fixture.now.Add(time.Second).Format(time.RFC3339Nano))
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "participants") {
		t.Fatalf("bad participant trigger error = %v", err)
	}
	_, err = fixture.database.ExecContext(fixture.ctx, `
		INSERT INTO peer_dialogue_messages(
			id, dialogue_id, sequence, sender_agent_id, recipient_agent_id, source_run_id, content, created_at
		) VALUES ('bad-source', ?, 1, 'agent-b', 'agent-a', ?, 'bad', ?)`,
		dialogue.ID, fixture.runs["agent-a"].ID, fixture.now.Add(time.Second).Format(time.RFC3339Nano))
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "source run") {
		t.Fatalf("bad source trigger error = %v", err)
	}

	// The same root-run trigger check is enforced by SQLite, independent of
	// the repository's Go validation.
	badDialogue, _ := fixture.newDialogue(t, "dialogue-bad-root", "agent-a", "agent-b", fixture.runs["agent-b"].ID, "bad-root", fixture.now.Add(time.Second))
	_, err = fixture.database.ExecContext(fixture.ctx, `
		INSERT INTO peer_dialogues(
			id, initiator_agent_id, peer_agent_id, trigger_run_id, pair_key, purpose, status,
			max_turns, max_tokens, max_duration_seconds, cooldown_seconds, turn_count, tokens_used,
			idempotency_key, request_hash, failure, version, created_at, updated_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, 'queued', 3, 4000, 60, 120, 0, 0, ?, ?, '', 1, ?, ?, ?)`,
		badDialogue.ID, badDialogue.InitiatorAgentID, badDialogue.PeerAgentID, badDialogue.TriggerRunID, badDialogue.PairKey,
		badDialogue.Purpose, badDialogue.IdempotencyKey, badDialogue.RequestHash,
		badDialogue.CreatedAt.Format(time.RFC3339Nano), badDialogue.UpdatedAt.Format(time.RFC3339Nano), badDialogue.ExpiresAt.Format(time.RFC3339Nano))
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "initiator-owned") {
		t.Fatalf("bad root trigger error = %v", err)
	}
}

func TestPeerDialogueSaveIsOptimisticAndRecoveryClosesInterruptedRows(t *testing.T) {
	fixture := newPeerDialogueFixture(t, "agent-a", "agent-b", "agent-c", "agent-d")
	dialogue, initial := fixture.newDialogue(t, "dialogue-optimistic", "agent-a", "agent-b", fixture.runs["agent-a"].ID, "optimistic", fixture.now)
	if err := fixture.repos.CreatePeerDialogueWithMessage(fixture.ctx, dialogue, initial); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.Transition(domain.PeerDialogueRunning, fixture.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repos.PeerDialogues.Save(fixture.ctx, dialogue); err != nil {
		t.Fatal(err)
	}
	stale := dialogue
	if err := dialogue.Transition(domain.PeerDialogueCancelling, fixture.now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repos.PeerDialogues.Save(fixture.ctx, dialogue); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repos.PeerDialogues.Save(fixture.ctx, stale); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale peer dialogue save error = %v", err)
	}

	queued, queuedInitial := fixture.newDialogue(t, "dialogue-queued", "agent-a", "agent-c", fixture.runs["agent-a"].ID, "queued", fixture.now.Add(10*time.Second))
	if err := fixture.repos.CreatePeerDialogueWithMessage(fixture.ctx, queued, queuedInitial); err != nil {
		t.Fatal(err)
	}
	running, runningInitial := fixture.newDialogue(t, "dialogue-running", "agent-a", "agent-d", fixture.runs["agent-a"].ID, "running", fixture.now.Add(11*time.Second))
	if err := fixture.repos.CreatePeerDialogueWithMessage(fixture.ctx, running, runningInitial); err != nil {
		t.Fatal(err)
	}
	if err := running.Transition(domain.PeerDialogueRunning, fixture.now.Add(12*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repos.PeerDialogues.Save(fixture.ctx, running); err != nil {
		t.Fatal(err)
	}

	if err := fixture.repos.PeerDialogues.RecoverInterrupted(fixture.ctx, fixture.now.Add(30*time.Second), "worker interrupted"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		id      domain.ID
		status  domain.PeerDialogueStatus
		failure string
	}{
		{queued.ID, domain.PeerDialogueCancelled, ""},
		{running.ID, domain.PeerDialogueFailed, "worker interrupted"},
		{dialogue.ID, domain.PeerDialogueFailed, "worker interrupted"},
	} {
		got, err := fixture.repos.PeerDialogues.Get(fixture.ctx, test.id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != test.status || got.Failure != test.failure || got.FinishedAt.IsZero() {
			t.Fatalf("recovered %s = %#v, want status=%s failure=%q", test.id, got, test.status, test.failure)
		}
	}
	if err := fixture.repos.PeerDialogues.RecoverInterrupted(fixture.ctx, fixture.now.Add(time.Minute), "second pass"); err != nil {
		t.Fatal(err)
	}
}

func TestPeerDialoguePersistsSemanticCompletionAndExpandedBounds(t *testing.T) {
	fixture := newPeerDialogueFixture(t, "agent-a", "agent-b")
	dialogue, initial := fixture.newDialogue(t, "dialogue-semantic-storage", "agent-a", "agent-b", fixture.runs["agent-a"].ID, "semantic-storage", fixture.now)
	dialogue.Budget = domain.PeerDialogueBudget{
		MinTurns: 2, MaxTurns: 8, MaxTokens: 16_000, MaxDurationSeconds: 300, CooldownSeconds: 0,
	}
	dialogue.ExpiresAt = fixture.now.Add(5 * time.Minute)
	if err := fixture.repos.CreatePeerDialogueWithMessage(fixture.ctx, dialogue, initial); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.Transition(domain.PeerDialogueRunning, fixture.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repos.PeerDialogues.Save(fixture.ctx, dialogue); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.RecordTurn(200, fixture.now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repos.PeerDialogues.Save(fixture.ctx, dialogue); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.RecordTurn(200, fixture.now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repos.PeerDialogues.Save(fixture.ctx, dialogue); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.Complete(domain.PeerDialogueCompletionSemantic, fixture.now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repos.PeerDialogues.Save(fixture.ctx, dialogue); err != nil {
		t.Fatal(err)
	}
	loaded, err := fixture.repos.PeerDialogues.Get(fixture.ctx, dialogue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Budget.MinTurns != 2 || loaded.Budget.MaxTurns != 8 || loaded.CompletionReason != domain.PeerDialogueCompletionSemantic {
		t.Fatalf("loaded semantic completion = %#v", loaded)
	}

	mutated := loaded
	mutated.Version++
	mutated.UpdatedAt = fixture.now.Add(5 * time.Second)
	mutated.CompletionReason = domain.PeerDialogueCompletionMaxTokens
	if err := fixture.repos.PeerDialogues.Save(fixture.ctx, mutated); err == nil || !strings.Contains(strings.ToLower(err.Error()), "completion reason") {
		t.Fatalf("completion reason mutation error = %v", err)
	}
}

func TestPeerDialogueSchemaDefaultsLegacyMinimum(t *testing.T) {
	fixture := newPeerDialogueFixture(t, "agent-a", "agent-b")
	var minTurns, maxTurns int
	if err := fixture.database.QueryRowContext(fixture.ctx, `
		SELECT min_turns, max_turns FROM peer_dialogues
		WHERE id = ''`).Scan(&minTurns, &maxTurns); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("empty dialogue probe error = %v", err)
	}
	// The migration keeps a default for direct legacy-shaped inserts. This is
	// important for timestamp/backfill tooling that intentionally writes the
	// pre-MinTurns column set before rerunning an older migration.
	dialogue, initial := fixture.newDialogue(t, "dialogue-schema-default", "agent-a", "agent-b", fixture.runs["agent-a"].ID, "schema-default", fixture.now)
	if _, err := fixture.database.ExecContext(fixture.ctx, `
		INSERT INTO peer_dialogues(
			id, initiator_agent_id, peer_agent_id, trigger_run_id, pair_key, purpose, status,
			max_turns, max_tokens, max_duration_seconds, cooldown_seconds, turn_count, tokens_used,
			idempotency_key, request_hash, failure, version, created_at, updated_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, 'queued', 8, 4000, 60, 0, 0, 0, ?, ?, '', 1, ?, ?, ?)`,
		dialogue.ID, dialogue.InitiatorAgentID, dialogue.PeerAgentID, dialogue.TriggerRunID, dialogue.PairKey, dialogue.Purpose,
		dialogue.IdempotencyKey, dialogue.RequestHash, formatTime(dialogue.CreatedAt), formatTime(dialogue.UpdatedAt), formatTime(dialogue.ExpiresAt)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.QueryRowContext(fixture.ctx, `SELECT min_turns, max_turns FROM peer_dialogues WHERE id = ?`, dialogue.ID).Scan(&minTurns, &maxTurns); err != nil {
		t.Fatal(err)
	}
	if minTurns != 1 || maxTurns != 8 {
		t.Fatalf("legacy-shaped insert budget = %d/%d, want 1/8", minTurns, maxTurns)
	}
	_ = initial
}
