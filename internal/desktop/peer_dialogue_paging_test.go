package desktop

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// seedPeerDialogues gives the bridge's active agent `count` dialogues, each
// with a different peer so every one of them is its own pair — the schema
// allows only one live dialogue per pair. Each carries its opening turn.
//
// It returns the initiator's id.
func seedPeerDialogues(t *testing.T, bridge *Bridge, count int) domain.ID {
	t.Helper()
	ctx := context.Background()
	initiator, err := bridge.CreateAgent(CreateAgentInput{Name: "Инициатор", Gender: "female"})
	if err != nil {
		t.Fatal(err)
	}
	peers := make([]domain.ID, 0, count)
	for index := 0; index < count; index++ {
		peer, createErr := bridge.CreateAgent(CreateAgentInput{Name: fmt.Sprintf("Собеседник %02d", index), Gender: "female"})
		if createErr != nil {
			t.Fatal(createErr)
		}
		peers = append(peers, domain.ID(peer.ID))
	}
	if _, err := bridge.SetActiveAgent(SelectAgentInput{ID: initiator.ID}); err != nil {
		t.Fatal(err)
	}
	conversation, err := bridge.NewConversation("Диалог владельца")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)
	// The schema insists the trigger run is a root run owned by the initiator.
	trigger, err := domain.NewRunForAgent(domain.ID(initiator.ID), "run-peer-parent", domain.RunKindInteractive, domain.ID(conversation.ID), now)
	if err != nil {
		t.Fatal(err)
	}
	trigger.Budget = domain.RunBudget{MaxSteps: 4, MaxTokens: 2000, MaxToolOutputBytes: 4096, MaxDurationSeconds: 60}
	trigger.Inference = domain.RunInferenceRoute{ProviderID: "openrouter", Model: "openrouter/free"}
	trigger.Usage = domain.RunUsage{InputTokens: 120, OutputTokens: 30, TotalTokens: 150}
	if err := bridge.repositories.Runs.Create(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	budget := domain.PeerDialogueBudget{MaxTurns: 3, MaxTokens: 4000, MaxDurationSeconds: 60, CooldownSeconds: 0}
	for index, peer := range peers {
		id := fmt.Sprintf("peer-dialogue-%03d", index)
		at := now.Add(time.Duration(index) * time.Minute)
		dialogue, dialogueErr := domain.NewPeerDialogue(
			domain.ID(id), domain.ID(initiator.ID), peer, trigger.ID,
			"сверить план", "key-"+id, "sha256:"+id, budget, at)
		if dialogueErr != nil {
			t.Fatal(dialogueErr)
		}
		initial := domain.PeerDialogueMessage{
			ID: domain.ID("message-" + id + "-0"), DialogueID: dialogue.ID, Sequence: 0,
			SenderAgentID: domain.ID(initiator.ID), RecipientAgentID: peer, SourceRunID: trigger.ID,
			Content: "Проверь эту задачу.", CreatedAt: at,
		}
		if err := bridge.repositories.PeerDialogues.Create(ctx, dialogue, initial); err != nil {
			t.Fatal(err)
		}
	}
	return domain.ID(initiator.ID)
}

// TestPeerDialogueListReadsAreSetBased is the N+1 property for the peer
// dialogue list.
//
// Rendering the page used to cost 1 + 3N round-trips: one read of the dialogue
// page, then per dialogue a read of its turns and a point lookup of each of its
// two participants. At the page limit of 50 that is 151 statements to draw 50
// rows. The pool is deliberately a single connection, so those are not merely
// slow — every one of them is serialized against every writer in the process.
//
// The count is asserted at two page sizes, because "four" and "constant in the
// page size" are different claims and only the second is the property that
// matters.
func TestPeerDialogueListReadsAreSetBased(t *testing.T) {
	bridge := newCountingBridge(t)
	const dialogues = 12
	initiatorID := seedPeerDialogues(t, bridge, dialogues)
	profiles, err := bridge.repositories.Agents.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		providerID, model := "codex", "gpt-5.6"
		if profile.ID == initiatorID {
			providerID, model = "openrouter", "openrouter/free"
		}
		if _, err := bridge.repositories.Agents.UpdateModelRoute(context.Background(), profile.ID, providerID, model, profile.UpdatedAt.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	var views []PeerDialogueView
	err = nil
	queries := countBridgeQueries(func() {
		views, err = bridge.ListPeerDialogues(PeerDialogueListInput{Limit: dialogues})
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != dialogues {
		t.Fatalf("listed %d dialogues, want %d", len(views), dialogues)
	}
	if queries != 4 {
		t.Fatalf("listing %d dialogues issued %d queries, want 4", dialogues, queries)
	}

	narrow := countBridgeQueries(func() {
		_, err = bridge.ListPeerDialogues(PeerDialogueListInput{Limit: 2})
	})
	if err != nil {
		t.Fatal(err)
	}
	if narrow != queries {
		t.Fatalf("listing 2 dialogues cost %d queries and listing %d cost %d: the cost must not scale with the page",
			narrow, dialogues, queries)
	}

	// The collapse must not have cost the page anything it used to carry: both
	// participants are still named, and the opening turn is still attributed.
	for _, view := range views {
		if view.InitiatorName != "Инициатор" || view.PeerName == "" {
			t.Fatalf("dialogue %s lost a participant name: initiator=%q peer=%q", view.ID, view.InitiatorName, view.PeerName)
		}
		if view.InitiatorProviderID != "openrouter" || view.InitiatorModel != "openrouter/free" || view.PeerProviderID != "codex" || view.PeerModel != "gpt-5.6" {
			t.Fatalf("dialogue %s routes = %#v", view.ID, view)
		}
		if len(view.Messages) != 1 {
			t.Fatalf("dialogue %s carried %d turns, want 1", view.ID, len(view.Messages))
		}
		turn := view.Messages[0]
		if turn.SenderName != "Инициатор" || turn.RecipientName != view.PeerName || turn.Content != "Проверь эту задачу." {
			t.Fatalf("dialogue %s turn = %#v", view.ID, turn)
		}
		if turn.ProviderID != "openrouter" || turn.Model != "openrouter/free" || turn.TotalTokens != 150 {
			t.Fatalf("dialogue %s historical turn attribution = %#v", view.ID, turn)
		}
	}
}

// TestPeerDialogueTurnsStayScopedToTheParticipant is the authorization property
// of the batch read, and the reason it reports "not a participant" and
// "participant, but the dialogue has no turns" as different answers.
//
// An inner join would collapse the second into the first and turn damaged
// durable state into a silent absence, which is the outcome the single-dialogue
// read was deliberately written to avoid.
func TestPeerDialogueTurnsStayScopedToTheParticipant(t *testing.T) {
	bridge := newCountingBridge(t)
	initiator := seedPeerDialogues(t, bridge, 2)
	ctx := context.Background()
	ids := []domain.ID{"peer-dialogue-000", "peer-dialogue-001"}

	scoped, err := bridge.repositories.PeerDialogueMessages.ListByDialogues(ctx, ids, initiator)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		turns, ok := scoped[id]
		if !ok {
			t.Fatalf("%s is missing for a participant of it", id)
		}
		if len(turns) != 1 || turns[0].Content != "Проверь эту задачу." {
			t.Fatalf("%s turns = %#v", id, turns)
		}
	}

	// A stranger is a party to neither, so neither is present at all — the
	// caller cannot mistake the refusal for an empty dialogue.
	outsider, err := bridge.repositories.PeerDialogueMessages.ListByDialogues(ctx, ids, "agent-nobody")
	if err != nil {
		t.Fatal(err)
	}
	if len(outsider) != 0 {
		t.Fatalf("a non-participant received %#v", outsider)
	}

	// Damaged state: the dialogue survives, its turns do not. A participant
	// must still see the dialogue, with no turns, rather than have it vanish.
	if _, err := bridge.database.ExecContext(ctx, `DELETE FROM peer_dialogue_messages WHERE dialogue_id = ?`, "peer-dialogue-000"); err != nil {
		t.Fatal(err)
	}
	damaged, err := bridge.repositories.PeerDialogueMessages.ListByDialogues(ctx, ids, initiator)
	if err != nil {
		t.Fatal(err)
	}
	turns, ok := damaged["peer-dialogue-000"]
	if !ok {
		t.Fatal("a dialogue that lost its turns must still be visible to its participant, not absent")
	}
	if len(turns) != 0 {
		t.Fatalf("damaged dialogue turns = %#v", turns)
	}
	if len(damaged["peer-dialogue-001"]) != 1 {
		t.Fatalf("the intact dialogue was disturbed: %#v", damaged["peer-dialogue-001"])
	}
}
