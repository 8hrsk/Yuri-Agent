package desktop

import (
	"context"
	"errors"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestPeerBudgetRecommendationIsReadOnlyAndBounded(t *testing.T) {
	bridge := newOpenAIBridgeSmoke(t, "http://127.0.0.1:9/v1", "sk-peer-recommendation-test")
	agents, err := bridge.repositories.Agents.List(context.Background())
	if err != nil || len(agents) != 1 {
		t.Fatalf("initial roster = %#v err=%v", agents, err)
	}
	initiator := agents[0]
	peer, err := bridge.CreateAgent(CreateAgentInput{Name: "Мира", Gender: "female"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.SetActiveAgent(SelectAgentInput{ID: initiator.ID.String()}); err != nil {
		t.Fatal(err)
	}

	view, err := bridge.RecommendPeerDialogueBudget(RecommendPeerDialogueBudgetInput{PeerAgentID: peer.ID, Purpose: "Коротко сверить план"})
	if err != nil {
		t.Fatal(err)
	}
	if view.Basis != "purpose_only" || view.SampleCount != 0 || view.Confidence != "low" {
		t.Fatalf("recommendation provenance = %#v", view)
	}
	if view.Ceiling.MaxTurns != 4 || view.Ceiling.MaxTokens != 8_000 || view.Ceiling.MaxDurationSeconds != 90 {
		t.Fatalf("recommendation ceiling = %#v", view.Ceiling)
	}
	if view.Recommended.MaxTurns != 2 || view.Recommended.MaxTokens != 5_000 || view.Recommended.MaxDurationSeconds != 50 {
		t.Fatalf("recommendation = %#v", view.Recommended)
	}
	dialogues, err := bridge.repositories.PeerDialogues.ListByParticipant(context.Background(), initiator.ID)
	if err != nil || len(dialogues) != 0 {
		t.Fatalf("read-only preview created dialogues = %#v err=%v", dialogues, err)
	}
}

func TestPeerBudgetRecommendationRejectsSelf(t *testing.T) {
	bridge := newOpenAIBridgeSmoke(t, "http://127.0.0.1:9/v1", "sk-peer-recommendation-self-test")
	agents, err := bridge.repositories.Agents.List(context.Background())
	if err != nil || len(agents) != 1 {
		t.Fatalf("initial roster = %#v err=%v", agents, err)
	}
	if _, err := bridge.RecommendPeerDialogueBudget(RecommendPeerDialogueBudgetInput{PeerAgentID: agents[0].ID.String(), Purpose: "Проверить"}); err == nil {
		t.Fatal("expected self-dialogue recommendation rejection")
	}
	if _, err := bridge.repositories.PeerDialogues.Get(context.Background(), domain.ID("missing")); err == nil {
		t.Fatal("unexpected dialogue created for rejected preview")
	}
}

func TestRecommendedPeerBudgetRejectsModifiedDraftAtStart(t *testing.T) {
	bridge := newOpenAIBridgeSmoke(t, "http://127.0.0.1:9/v1", "sk-peer-recommendation-stale-test")
	agents, err := bridge.repositories.Agents.List(context.Background())
	if err != nil || len(agents) != 1 {
		t.Fatalf("initial roster = %#v err=%v", agents, err)
	}
	initiator := agents[0]
	peer, err := bridge.CreateAgent(CreateAgentInput{Name: "Мира", Gender: "female"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.SetActiveAgent(SelectAgentInput{ID: initiator.ID.String()}); err != nil {
		t.Fatal(err)
	}
	purpose := "Сверить план"
	recommendation, err := bridge.RecommendPeerDialogueBudget(RecommendPeerDialogueBudgetInput{PeerAgentID: peer.ID, Purpose: purpose})
	if err != nil {
		t.Fatal(err)
	}
	_, err = bridge.StartPeerDialogue(StartPeerDialogueInput{
		PeerAgentID: peer.ID, Purpose: purpose, Message: "Проверь.", BudgetSource: "recommendation",
		MaxTurns: recommendation.Recommended.MaxTurns, MaxTokens: recommendation.Recommended.MaxTokens - 1,
		MaxDurationSeconds: recommendation.Recommended.MaxDurationSeconds,
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("modified recommendation error = %v", err)
	}
	runs, err := bridge.repositories.Runs.ListByAgent(context.Background(), initiator.ID)
	if err != nil || len(runs) != 0 {
		t.Fatalf("rejected recommendation created runs = %#v err=%v", runs, err)
	}
}
