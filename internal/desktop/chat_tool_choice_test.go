package desktop

import (
	"context"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func TestDirectPeerActionRequiresPeerDialogueTool(t *testing.T) {
	roster := []domain.AgentProfile{
		{ID: "emily", Name: "Emily"},
		{ID: "yuri", Name: "Yuri"},
	}
	if !directPeerAction("Напиши Yuri, что я скоро вернусь", "emily", roster) {
		t.Fatal("explicit peer action was not recognized")
	}
	if directPeerAction("Как ты думаешь, что любит Yuri?", "emily", roster) {
		t.Fatal("ordinary conversation about a peer must remain automatic")
	}
}

func TestShortRetryAfterFailedPeerCallRequiresSameTool(t *testing.T) {
	bridge := newAgentTestBridge(t)
	owner, err := bridge.CreateAgent(CreateAgentInput{Name: "Emily", Gender: "female"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.CreateAgent(CreateAgentInput{Name: "Yuri", Gender: "female"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	conversationID := domain.ID("peer-retry-conversation")
	ownerID := domain.ID(owner.ID)
	if err := bridge.ensureConversation(ctx, conversationID, "Напиши Yuri", now, ownerID); err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewRunForAgent(ownerID, "failed-peer-run", domain.RunKindInteractive, conversationID, now)
	if err != nil {
		t.Fatal(err)
	}
	run.Budget = domain.RunBudget{MaxSteps: 8, MaxTokens: 1000, MaxToolCalls: 4, MaxToolOutputBytes: 4096, MaxDurationSeconds: 60}
	if err := bridge.repositories.Runs.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := bridge.repositories.ToolCalls.Create(ctx, storage.ToolCall{
		ID: "failed-peer-call", RunID: run.ID, ToolID: peerDialogueToolID, ArgsRedacted: "{}",
		Risk: domain.RiskLow, Status: storage.ToolCallFailed, Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	roster, err := bridge.repositories.Agents.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	choice, err := bridge.chatToolChoice(ctx, conversationID, ownerID, "Давай. Не стесняйся", roster)
	if err != nil {
		t.Fatal(err)
	}
	if choice != (agent.ToolChoice{Mode: agent.ToolChoiceRequired, Name: peerDialogueToolID}) {
		t.Fatalf("tool choice = %#v", choice)
	}
	choice, err = bridge.chatToolChoice(ctx, conversationID, ownerID, "Расскажи анекдот", roster)
	if err != nil {
		t.Fatal(err)
	}
	if choice.Mode != "" {
		t.Fatalf("ordinary follow-up unexpectedly forced a tool: %#v", choice)
	}
}
