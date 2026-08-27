package domain

import (
	"errors"
	"testing"
	"time"
)

func TestApprovalDecidesOnce(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	approval, err := NewApproval("approval-1", "run-1", "sha256:abc", "write file", RiskMedium, CapabilityScope{Kind: ScopeFilesystem, Values: []string{"/tmp/project"}}, now)
	if err != nil {
		t.Fatalf("NewApproval() error = %v", err)
	}
	if !approval.Pending() || approval.Version != 1 {
		t.Fatalf("initial approval = %#v", approval)
	}
	if err := approval.Approve(ActorUser, "confirmed", now.Add(time.Second)); err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if approval.Decision != ApprovalApproved || approval.DecidedBy != ActorUser || approval.Version != 2 {
		t.Fatalf("decided approval = %#v", approval)
	}
	if err := approval.Deny(ActorUser, "changed mind", now.Add(2*time.Second)); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("second decision error = %v", err)
	}
}

func TestApprovalRejectsUnknownDecisionActor(t *testing.T) {
	now := time.Now().UTC()
	approval, err := NewApproval("approval-1", "run-1", "hash", "send", RiskHigh, UnrestrictedScope(), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := approval.Approve(Actor("unknown"), "", now); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unknown actor error = %v", err)
	}
	if approval.Decision != ApprovalPending {
		t.Fatalf("approval changed after invalid actor: %#v", approval)
	}
}
