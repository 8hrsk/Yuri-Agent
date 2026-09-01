package domain

import (
	"errors"
	"testing"
	"time"
)

func TestPeerDialogueLifecycleAndBudget(t *testing.T) {
	now := time.Date(2026, 8, 29, 18, 0, 0, 0, time.UTC)
	budget := PeerDialogueBudget{MaxTurns: 2, MaxTokens: 2400, MaxDurationSeconds: 90, CooldownSeconds: 300}
	dialogue, err := NewPeerDialogue("dialogue-1", "agent-a", "agent-b", "run-root", "Обсудить план", "call-1", "sha256:abc", budget, now)
	if err != nil {
		t.Fatal(err)
	}
	if dialogue.PairKey != AgentPairKey("agent-b", "agent-a") {
		t.Fatal("pair key is not symmetric")
	}
	if dialogue.Budget.MinTurns != 1 {
		t.Fatalf("legacy omitted minimum = %d, want 1", dialogue.Budget.MinTurns)
	}
	if err := dialogue.Transition(PeerDialogueRunning, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.RecordTurn(100, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.RecordTurn(200, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.RecordTurn(1, now.Add(4*time.Second)); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("overflow error=%v", err)
	}
	if err := dialogue.Transition(PeerDialogueCompleted, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPeerDialogueBudgetHasBoundedSemanticMinimum(t *testing.T) {
	valid := PeerDialogueBudget{MinTurns: 2, MaxTurns: 8, MaxTokens: 16_000, MaxDurationSeconds: 300, CooldownSeconds: 24 * 60 * 60}
	if !valid.Valid() {
		t.Fatal("maximum bounded peer budget should be valid")
	}
	for _, invalid := range []PeerDialogueBudget{
		{MinTurns: 0, MaxTurns: 4, MaxTokens: 100, MaxDurationSeconds: 10},
		{MinTurns: 3, MaxTurns: 2, MaxTokens: 100, MaxDurationSeconds: 10},
		{MinTurns: 1, MaxTurns: 9, MaxTokens: 100, MaxDurationSeconds: 10},
	} {
		if invalid.Valid() {
			t.Fatalf("invalid peer budget accepted: %#v", invalid)
		}
	}
}

func TestPeerDialogueCompleteRequiresMinimumAndPersistsReason(t *testing.T) {
	now := time.Date(2026, 8, 29, 18, 0, 0, 0, time.UTC)
	dialogue, err := NewPeerDialogue("dialogue-semantic", "agent-a", "agent-b", "run-root", "Обсудить план", "call-semantic", "sha256:semantic", PeerDialogueBudget{
		MinTurns: 2, MaxTurns: 4, MaxTokens: 2400, MaxDurationSeconds: 90, CooldownSeconds: 300,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := dialogue.Transition(PeerDialogueRunning, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.RecordTurn(100, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.Complete(PeerDialogueCompletionSemantic, now.Add(3*time.Second)); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("semantic completion before minimum error = %v", err)
	}
	if dialogue.Status != PeerDialogueRunning || dialogue.CompletionReason != "" {
		t.Fatalf("failed semantic completion mutated dialogue: %#v", dialogue)
	}
	if err := dialogue.RecordTurn(100, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.Complete(PeerDialogueCompletionSemantic, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if dialogue.Status != PeerDialogueCompleted || dialogue.CompletionReason != PeerDialogueCompletionSemantic || dialogue.FinishedAt.IsZero() {
		t.Fatalf("semantic completion = %#v", dialogue)
	}
	if err := dialogue.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPeerDialogueTransitionKeepsLegacyImplicitCompletion(t *testing.T) {
	now := time.Now().UTC()
	dialogue, err := NewPeerDialogue("dialogue-implicit", "agent-a", "agent-b", "run-root", "purpose", "key", "hash", PeerDialogueBudget{
		MinTurns: 1, MaxTurns: 1, MaxTokens: 1000, MaxDurationSeconds: 30, CooldownSeconds: 0,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := dialogue.Transition(PeerDialogueRunning, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.RecordTurn(1, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.Transition(PeerDialogueCompleted, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if dialogue.CompletionReason != PeerDialogueCompletionImplicit {
		t.Fatalf("legacy transition reason = %q", dialogue.CompletionReason)
	}
}

func TestPeerDialogueRecordsFailedUsageWithoutInventingTurn(t *testing.T) {
	now := time.Now().UTC()
	budget := PeerDialogueBudget{MaxTurns: 1, MaxTokens: 8000, MaxDurationSeconds: 30, CooldownSeconds: 60}
	dialogue, err := NewPeerDialogue("dialogue-failed-usage", "agent-a", "agent-b", "run-a", "diagnose", "key", "hash", budget, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := dialogue.Transition(PeerDialogueRunning, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.RecordFailedUsage(1375, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if dialogue.TokensUsed != 1375 || dialogue.TurnCount != 0 {
		t.Fatalf("failed usage dialogue = %#v", dialogue)
	}
	if err := dialogue.RecordFailedUsage(7000, now.Add(3*time.Second)); err == nil {
		t.Fatal("usage beyond the dialogue budget was accepted")
	}
}

func TestPeerDialogueRejectsSelfAndRequiresFailureReason(t *testing.T) {
	now := time.Now().UTC()
	budget := PeerDialogueBudget{MaxTurns: 2, MaxTokens: 1000, MaxDurationSeconds: 30, CooldownSeconds: 60}
	if _, err := NewPeerDialogue("dialogue", "same", "same", "run", "purpose", "call", "hash", budget, now); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("self dialogue error=%v", err)
	}
	dialogue, err := NewPeerDialogue("dialogue", "a", "b", "run", "purpose", "call", "hash", budget, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := dialogue.Transition(PeerDialogueRunning, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.Transition(PeerDialogueFailed, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.Validate(); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("missing failure validation=%v", err)
	}
	dialogue.Failure = "safe failure"
	if err := dialogue.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPeerDialogueAutonomousTriggerProvenance(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	budget := PeerDialogueBudget{MaxTurns: 2, MaxTokens: 1000, MaxDurationSeconds: 30, CooldownSeconds: 60}
	dialogue, err := NewPeerDialogue("dialogue", "a", "b", "run", "purpose", "call", "hash", budget, now)
	if err != nil {
		t.Fatal(err)
	}
	if dialogue.TriggerKind != PeerDialogueTriggerAgentTool || dialogue.TriggerReason == "" {
		t.Fatalf("default trigger provenance = %q %q", dialogue.TriggerKind, dialogue.TriggerReason)
	}
	if err := dialogue.MarkAutonomous("Нужна независимая проверка плана."); err != nil {
		t.Fatal(err)
	}
	if dialogue.TriggerKind != PeerDialogueTriggerAutonomous || dialogue.TriggerReason != "Нужна независимая проверка плана." {
		t.Fatalf("autonomous trigger provenance = %q %q", dialogue.TriggerKind, dialogue.TriggerReason)
	}
	if err := dialogue.MarkAutonomous(" "); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty autonomous reason error = %v", err)
	}
}
