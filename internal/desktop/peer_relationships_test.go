package desktop

import (
	"context"
	"errors"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestPeerRelationshipViewsAreObserverScopedAndRollbackResetAreAppendOnly(t *testing.T) {
	bridge, initiatorID, peerID, parent := newPeerDialogueTestBridge(t)
	bridge.config.Persona.AutoEvolution = true
	dialogue := storeCompletedPeerDialogueForReflection(t, bridge, initiatorID, peerID, parent, "dialogue-peer-view")
	backend := &reflectionBackendStub{events: []agent.ModelEvent{
		{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"changed","reason":"constructive","evidence_ids":["dialogue-peer-view-message-1"],"relationship":{"dimensions":{"trust":0.05},"opinions":[{"subject":"` + peerID.String() + `","topic":"collaboration","claim":"Считает peer надёжным собеседником","label":"opinion","confidence":0.8,"reason":"Предметный ответ","evidence_ids":["dialogue-peer-view-message-1"]}],"reason":"helpful peer","confidence":0.8}}`},
		{Type: agent.ModelEventCompleted, Usage: agent.Usage{TotalTokens: 60}},
	}}
	if changed, err := bridge.reflectOnPeerDialogue(context.Background(), backend, "test", dialogue, initiatorID); err != nil || !changed {
		t.Fatalf("seed social reflection changed=%t err=%v", changed, err)
	}

	listed, err := bridge.ListPeerRelationships(PeerRelationshipListInput{Limit: 10})
	if err != nil || len(listed) != 1 || listed[0].ObserverAgentID != initiatorID.String() || listed[0].PeerAgentID != peerID.String() || listed[0].Version != 2 || len(listed[0].Opinions) != 1 {
		t.Fatalf("initiator relationships=%#v err=%v", listed, err)
	}
	detail, err := bridge.GetPeerRelationship(PeerRelationshipInput{PeerAgentID: peerID.String()})
	if err != nil || len(detail.Versions) != 2 || detail.Versions[0].Version != 2 {
		t.Fatalf("detail=%#v err=%v", detail, err)
	}
	reflectedVersionID := detail.Relationship.CurrentVersionID

	reset, err := bridge.ResetPeerRelationship(PeerRelationshipInput{PeerAgentID: peerID.String()})
	if err != nil || reset.Relationship.Version != 3 || len(reset.Relationship.Opinions) != 0 || reset.Versions[0].Operation != "reset" {
		t.Fatalf("reset=%#v err=%v", reset, err)
	}
	rolledBack, err := bridge.RollbackPeerRelationship(PeerRelationshipRollbackInput{PeerAgentID: peerID.String(), VersionID: reflectedVersionID})
	if err != nil || rolledBack.Relationship.Version != 4 || len(rolledBack.Relationship.Opinions) != 1 || rolledBack.Versions[0].Operation != "rollback" {
		t.Fatalf("rollback=%#v err=%v", rolledBack, err)
	}

	if _, err := bridge.SetActiveAgent(SelectAgentInput{ID: peerID.String()}); err != nil {
		t.Fatal(err)
	}
	peerView, err := bridge.ListPeerRelationships(PeerRelationshipListInput{Limit: 10})
	if err != nil || len(peerView) != 0 {
		t.Fatalf("reverse relationship leaked initiator opinion: %#v err=%v", peerView, err)
	}

	third, err := bridge.CreateAgent(CreateAgentInput{Name: "Сора", Gender: "female"})
	if err != nil {
		t.Fatal(err)
	}
	if third.ID == initiatorID.String() || third.ID == peerID.String() {
		t.Fatal("third agent id collided")
	}
	if empty, err := bridge.ListPeerRelationships(PeerRelationshipListInput{Limit: 10}); err != nil || len(empty) != 0 {
		t.Fatalf("third agent saw peer relationships=%#v err=%v", empty, err)
	}
	if _, err := bridge.GetPeerRelationship(PeerRelationshipInput{PeerAgentID: peerID.String()}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("third agent unscoped get err=%v", err)
	}

	audits, err := bridge.repositories.Audit.List(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	seenReset, seenRollback := false, false
	for _, item := range audits {
		if item.Action == "relationship.reset" && item.Actor == domain.ActorUser {
			seenReset = true
		}
		if item.Action == "relationship.rollback" && item.Actor == domain.ActorUser {
			seenRollback = true
		}
	}
	if !seenReset || !seenRollback {
		t.Fatalf("missing owner audit reset=%t rollback=%t", seenReset, seenRollback)
	}
}
