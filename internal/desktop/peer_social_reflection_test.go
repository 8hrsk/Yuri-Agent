package desktop

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func TestPeerSocialReflectionCreatesDirectionalOpinionsAndAffectAtomically(t *testing.T) {
	bridge, initiatorID, peerID, parent := newPeerDialogueTestBridge(t)
	bridge.config.Persona.AutoEvolution = true
	dialogue := storeCompletedPeerDialogueForReflection(t, bridge, initiatorID, peerID, parent, "dialogue-social")
	backend := &delegationBackendStub{batches: [][]agent.ModelEvent{
		{{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"changed","reason":"helpful exchange","evidence_ids":["dialogue-social-message-1"],"relationship":{"dimensions":{"trust":0.05,"respect":0.04},"opinions":[{"subject":"` + peerID.String() + `","topic":"collaboration","claim":"Считает Миру внимательной и полезной собеседницей","label":"opinion","confidence":0.8,"reason":"Мира дала спокойный предметный ответ","evidence_ids":["dialogue-social-message-1"]}],"reason":"constructive reply","confidence":0.8},"affect":{"dimensions":{"joy":0.04},"reason":"pleasant exchange","confidence":0.75}}`}, {Type: agent.ModelEventCompleted, Usage: agent.Usage{TotalTokens: 100}}},
		{{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"changed","reason":"clear request","evidence_ids":["dialogue-social-message-0"],"relationship":{"dimensions":{"respect":0.03},"opinions":[{"subject":"` + initiatorID.String() + `","topic":"communication","claim":"Считает Юри прямой и понятной собеседницей","label":"inference","confidence":0.72,"reason":"Запрос был конкретным","evidence_ids":["dialogue-social-message-0"]}],"reason":"clear interaction","confidence":0.72},"affect":{"dimensions":{"sympathy":0.03},"reason":"friendly exchange","confidence":0.7}}`}, {Type: agent.ModelEventCompleted, Usage: agent.Usage{TotalTokens: 90}}},
	}}
	writes, err := bridge.reflectOnPeerDialogueParticipants(context.Background(), backend, "test-model", dialogue)
	if err != nil || writes != 2 {
		t.Fatalf("social reflection writes=%d err=%v", writes, err)
	}
	left, err := bridge.repositories.PeerSocial.GetRelationship(context.Background(), initiatorID, peerID)
	if err != nil {
		t.Fatal(err)
	}
	right, err := bridge.repositories.PeerSocial.GetRelationship(context.Background(), peerID, initiatorID)
	if err != nil {
		t.Fatal(err)
	}
	if left.ID == right.ID || left.Version != 2 || right.Version != 2 || len(left.Opinions) != 1 || left.Opinions[0].Subject != peerID.String() || len(right.Opinions) != 1 || right.Opinions[0].Subject != initiatorID.String() {
		t.Fatalf("directional relationships left=%#v right=%#v", left, right)
	}
	for _, id := range []domain.ID{initiatorID, peerID} {
		ownerRelationship, _ := bridge.repositories.Relationship.Get(context.Background(), id)
		persona, _ := bridge.repositories.Persona.Get(context.Background(), id)
		affect, _ := bridge.repositories.Affect.Get(context.Background(), id)
		if ownerRelationship.Version != 1 || persona.Version != 1 || affect.Version != 2 {
			t.Fatalf("wrong target changed for %s: owner=%d persona=%d affect=%d", id, ownerRelationship.Version, persona.Version, affect.Version)
		}
		if _, err := bridge.repositories.PeerSocial.GetReflection(context.Background(), dialogue.ID, id); err != nil {
			t.Fatalf("missing marker for %s: %v", id, err)
		}
	}
	requests := backend.snapshot()
	if len(requests) != 2 {
		t.Fatalf("model requests=%d", len(requests))
	}
	for _, request := range requests {
		if len(request.Tools) != 0 || request.Metadata["purpose"] != "peer_social_reflection" || !strings.Contains(request.Messages[0].Content, "Never propose persona changes") {
			t.Fatalf("unsafe social request=%#v", request)
		}
	}
	messages, err := bridge.repositories.PeerDialogueMessages.ListByDialogue(context.Background(), dialogue.ID, initiatorID)
	if err != nil {
		t.Fatal(err)
	}
	turnBackend := &reflectionBackendStub{events: []agent.ModelEvent{{Type: agent.ModelEventTextDelta, Delta: "Учту это в следующем ответе."}, {Type: agent.ModelEventCompleted, Usage: agent.Usage{TotalTokens: 12}}}}
	if _, _, _, err := bridge.runPeerDialogueTurn(context.Background(), dialogue, messages, initiatorID, peerID, turnBackend, "test-model"); err != nil {
		t.Fatal(err)
	}
	peerPrompt := turnBackend.request.Messages[0].Content
	if !strings.Contains(peerPrompt, left.Opinions[0].Text()) || strings.Contains(peerPrompt, right.Opinions[0].Text()) {
		t.Fatalf("directional opinion context leaked or missing: %q", peerPrompt)
	}
	if second, err := bridge.reflectOnPeerDialogueParticipants(context.Background(), backend, "test-model", dialogue); err != nil || second != 0 || len(backend.snapshot()) != 2 {
		t.Fatalf("idempotent social reflection=%d err=%v requests=%d", second, err, len(backend.snapshot()))
	}
	// Force the final idempotency insert to conflict after both candidate
	// revisions were validated. The whole transaction must roll back.
	duplicate, _ := bridge.repositories.PeerSocial.GetReflection(context.Background(), dialogue.ID, initiatorID)
	nextRelationship := left
	nextRelationship.Version++
	nextRelationship.Dimensions = copyFloatMap(left.Dimensions)
	nextRelationship.Dimensions[domain.RelationshipDimensionTrust] += .01
	nextRelationship.Reason, nextRelationship.UpdatedAt = "duplicate must roll back", time.Now().UTC()
	currentAffect, _ := bridge.repositories.Affect.Get(context.Background(), initiatorID)
	nextAffect := currentAffect
	nextAffect.Version++
	nextAffect.Emotions = copyFloatMap(affectValues(currentAffect))
	nextAffect.Emotions[domain.EmotionJoy] += .01
	nextAffect.Reason, nextAffect.UpdatedAt, nextAffect.AsOf = "duplicate must roll back", nextRelationship.UpdatedAt, nextRelationship.UpdatedAt
	err = bridge.repositories.PeerSocial.Apply(context.Background(), storage.PeerSocialMutation{Record: duplicate, Relationship: &nextRelationship, ExpectedRelationship: left.Version, Affect: &nextAffect, ExpectedAffect: currentAffect.Version})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate marker error=%v", err)
	}
	leftAfter, _ := bridge.repositories.PeerSocial.GetRelationship(context.Background(), initiatorID, peerID)
	affectAfter, _ := bridge.repositories.Affect.Get(context.Background(), initiatorID)
	if leftAfter.Version != left.Version || affectAfter.Version != currentAffect.Version {
		t.Fatalf("duplicate partially committed relationship=%d affect=%d", leftAfter.Version, affectAfter.Version)
	}
}

func TestPeerSocialReflectionReconcileRepairsOneCompletedDialogue(t *testing.T) {
	bridge, initiatorID, peerID, parent := newPeerDialogueTestBridge(t)
	bridge.config.Persona.AutoEvolution = true
	dialogue := storeCompletedPeerDialogueForReflection(t, bridge, initiatorID, peerID, parent, "dialogue-social-reconcile")
	backend := &delegationBackendStub{batches: [][]agent.ModelEvent{
		{{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"no_change","reason":"insufficient signal"}`}, {Type: agent.ModelEventCompleted, Usage: agent.Usage{TotalTokens: 20}}},
		{{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"no_change","reason":"insufficient signal"}`}, {Type: agent.ModelEventCompleted, Usage: agent.Usage{TotalTokens: 20}}},
	}}
	if _, err := bridge.reconcileCompletedPeerSocialReflections(context.Background(), backend, "test", 10); err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.ID{initiatorID, peerID} {
		if record, err := bridge.repositories.PeerSocial.GetReflection(context.Background(), dialogue.ID, id); err != nil || record.Outcome != "no_change" {
			t.Fatalf("reconciled marker for %s=%#v err=%v", id, record, err)
		}
	}
	requests := len(backend.snapshot())
	if _, err := bridge.reconcileCompletedPeerSocialReflections(context.Background(), backend, "test", 10); err != nil || len(backend.snapshot()) != requests {
		t.Fatalf("reconcile repeated model: requests=%d->%d err=%v", requests, len(backend.snapshot()), err)
	}
}

func TestPeerSocialReflectionRejectsPersonaOrWrongSubjectWithoutPartialState(t *testing.T) {
	bridge, initiatorID, peerID, parent := newPeerDialogueTestBridge(t)
	bridge.config.Persona.AutoEvolution = true
	dialogue := storeCompletedPeerDialogueForReflection(t, bridge, initiatorID, peerID, parent, "dialogue-social-reject")
	backend := &reflectionBackendStub{events: []agent.ModelEvent{{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"changed","reason":"unsafe","relationship":{"opinions":[{"subject":"owner","topic":"identity","claim":"Неверная цель","label":"opinion","confidence":0.8,"reason":"bad","evidence_ids":["dialogue-social-reject-message-1"]}]}}`}, {Type: agent.ModelEventCompleted, Usage: agent.Usage{TotalTokens: 20}}}}
	if _, err := bridge.reflectOnPeerDialogue(context.Background(), backend, "test", dialogue, initiatorID); err == nil {
		t.Fatal("wrong-subject proposal was accepted")
	}
	relationship, err := bridge.repositories.PeerSocial.GetRelationship(context.Background(), initiatorID, peerID)
	if err != nil {
		t.Fatal(err)
	}
	affect, _ := bridge.repositories.Affect.Get(context.Background(), initiatorID)
	if relationship.Version != 1 || affect.Version != 1 {
		t.Fatalf("rejected reflection partially persisted relationship=%d affect=%d", relationship.Version, affect.Version)
	}
	if _, err := bridge.repositories.PeerSocial.GetReflection(context.Background(), dialogue.ID, initiatorID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("rejected reflection wrote marker: %v", err)
	}
}

func TestPeerSocialReflectionSecretOnlyTranscriptBecomesIdempotentNoChange(t *testing.T) {
	bridge, initiatorID, peerID, parent := newPeerDialogueTestBridge(t)
	bridge.config.Persona.AutoEvolution = true
	dialogue := storeCompletedPeerDialogueForReflection(t, bridge, initiatorID, peerID, parent, "dialogue-social-secret")
	if _, err := bridge.database.Exec(`UPDATE peer_dialogue_messages SET content = 'api_key=sk-secret-value' WHERE dialogue_id = ?`, dialogue.ID.String()); err != nil {
		t.Fatal(err)
	}
	backend := &reflectionBackendStub{}
	changed, err := bridge.reflectOnPeerDialogue(context.Background(), backend, "test", dialogue, initiatorID)
	if err != nil || changed || backend.request.Model != "" {
		t.Fatalf("secret-only reflection changed=%t err=%v request=%#v", changed, err, backend.request)
	}
	record, err := bridge.repositories.PeerSocial.GetReflection(context.Background(), dialogue.ID, initiatorID)
	if err != nil || record.Outcome != "no_change" {
		t.Fatalf("no-change marker=%#v err=%v", record, err)
	}
}

func storeCompletedPeerDialogueForReflection(t *testing.T, bridge *Bridge, initiatorID, peerID domain.ID, parent domain.AgentRun, id domain.ID) domain.PeerDialogue {
	t.Helper()
	now := time.Now().UTC()
	dialogue, err := domain.NewPeerDialogue(id, initiatorID, peerID, parent.ID, "Обсудить совместную работу", id.String()+"-key", "sha256:"+id.String(), defaultPeerDialogueBudget, now)
	if err != nil {
		t.Fatal(err)
	}
	initial := domain.PeerDialogueMessage{ID: domain.ID(id.String() + "-message-0"), DialogueID: id, SenderAgentID: initiatorID, RecipientAgentID: peerID, SourceRunID: parent.ID, Content: "Предлагаю проверить вывод вместе", CreatedAt: now}
	if err := bridge.repositories.CreatePeerDialogueWithMessage(context.Background(), dialogue, initial); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.Transition(domain.PeerDialogueRunning, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := bridge.repositories.PeerDialogues.Save(context.Background(), dialogue); err != nil {
		t.Fatal(err)
	}
	peerRun, err := domain.NewRunForAgent(peerID, domain.ID("run-"+id.String()), domain.RunKindBackground, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.repositories.Runs.Create(context.Background(), peerRun); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.RecordTurn(20, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	response := domain.PeerDialogueMessage{ID: domain.ID(id.String() + "-message-1"), DialogueID: id, Sequence: 1, SenderAgentID: peerID, RecipientAgentID: initiatorID, SourceRunID: peerRun.ID, Content: "Согласна, проверим последовательно и спокойно", CreatedAt: now.Add(2 * time.Second)}
	if err := bridge.repositories.AppendPeerDialogueTurn(context.Background(), dialogue, response); err != nil {
		t.Fatal(err)
	}
	if err := dialogue.Transition(domain.PeerDialogueCompleted, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := bridge.repositories.PeerDialogues.Save(context.Background(), dialogue); err != nil {
		t.Fatal(err)
	}
	return dialogue
}
