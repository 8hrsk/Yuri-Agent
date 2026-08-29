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
