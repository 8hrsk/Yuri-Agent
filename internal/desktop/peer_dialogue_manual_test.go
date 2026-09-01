package desktop

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestOwnerCanStartPeerDialogueWithNarrowedBudget(t *testing.T) {
	const secret = "sk-peer-manual-test"
	server := newIPv4ProviderTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher := writer.(http.Flusher)
		writeProviderProbeSSE(writer, flusher, "response.created", map[string]any{
			"type": "response.created", "response": map[string]any{"id": "response-peer-manual"},
		})
		writeProviderProbeSSE(writer, flusher, "response.output_text.delta", map[string]any{
			"type": "response.output_text.delta", "response_id": "response-peer-manual", "delta": `{"message":"План понятен.","outcome":"complete"}`,
		})
		writeProviderProbeSSE(writer, flusher, "response.completed", map[string]any{
			"type": "response.completed", "response_id": "response-peer-manual",
			"response": map[string]any{"id": "response-peer-manual", "usage": map[string]any{"input_tokens": 30, "output_tokens": 8, "total_tokens": 38}},
		})
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	bridge := newOpenAIBridgeSmoke(t, server.URL+"/v1", secret)
	bridge.mu.Lock()
	bridge.shuttingDown = false
	bridge.mu.Unlock()
	t.Cleanup(func() {
		bridge.backgroundCancel()
		bridge.background.Wait()
	})
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
	started, err := bridge.StartPeerDialogue(StartPeerDialogueInput{
		PeerAgentID: peer.ID, Purpose: "Проверить ручной запуск", Message: "Посмотри план.",
		MaxTurns: 1, MaxTokens: 2_000, MaxDurationSeconds: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.MinTurns != 1 || started.MaxTurns != 1 || started.MaxTokens != 2_000 || started.MaxDurationSeconds != 30 {
		t.Fatalf("start view = %#v", started)
	}
	dialogue := waitForPeerDialogue(t, bridge, domain.ID(started.ID), domain.PeerDialogueCompleted)
	if dialogue.TriggerReason != "Владелец вручную запустил внутренний диалог из Collaboration." || dialogue.CompletionReason != domain.PeerDialogueCompletionMaxTurns || dialogue.BudgetOrigin != domain.PeerDialogueBudgetOwnerCustom {
		t.Fatalf("manual dialogue = %#v", dialogue)
	}
	triggerRun, err := bridge.repositories.Runs.Get(context.Background(), dialogue.TriggerRunID)
	if err != nil || triggerRun.AgentID != initiator.ID || triggerRun.State != domain.RunStateCompleted || triggerRun.Kind != domain.RunKindBackground {
		t.Fatalf("manual trigger run = %#v err=%v", triggerRun, err)
	}

	secondPeer, err := bridge.CreateAgent(CreateAgentInput{Name: "Аки", Gender: "female"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.SetActiveAgent(SelectAgentInput{ID: initiator.ID.String()}); err != nil {
		t.Fatal(err)
	}
	purpose := "Подробно сравнить архитектурные варианты, риски миграции и план проверки результата"
	recommended, err := bridge.RecommendPeerDialogueBudget(RecommendPeerDialogueBudgetInput{PeerAgentID: secondPeer.ID, Purpose: purpose})
	if err != nil {
		t.Fatal(err)
	}
	recommendedStart, err := bridge.StartPeerDialogue(StartPeerDialogueInput{
		PeerAgentID: secondPeer.ID, Purpose: purpose, Message: "Проведи независимую проверку.", BudgetSource: "recommendation",
		MaxTurns: recommended.Recommended.MaxTurns, MaxTokens: recommended.Recommended.MaxTokens, MaxDurationSeconds: recommended.Recommended.MaxDurationSeconds,
	})
	if err != nil {
		t.Fatal(err)
	}
	recommendedDialogue := waitForPeerDialogue(t, bridge, domain.ID(recommendedStart.ID), domain.PeerDialogueCompleted)
	if recommendedDialogue.BudgetOrigin != domain.PeerDialogueBudgetOwnerRecommendation ||
		recommendedDialogue.Recommendation.Budget != recommendedDialogue.Budget ||
		recommendedDialogue.Recommendation.Basis != domain.PeerBudgetRecommendationPurposeOnly ||
		recommendedDialogue.CompletionReason != domain.PeerDialogueCompletionSemantic {
		t.Fatalf("recommended dialogue provenance = %#v", recommendedDialogue)
	}
}

func TestManualPeerBudgetInputRejectsInvalidDuration(t *testing.T) {
	if err := validateManualPeerBudget(StartPeerDialogueInput{PeerAgentID: "peer", MaxDurationSeconds: 4}); err == nil {
		t.Fatal("expected invalid duration")
	}
}

func TestManualPeerBudgetRejectsUnknownSource(t *testing.T) {
	if err := validateManualPeerBudget(StartPeerDialogueInput{PeerAgentID: "peer", BudgetSource: "automatic"}); err == nil {
		t.Fatal("expected invalid budget source")
	}
}
