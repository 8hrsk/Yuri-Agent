package desktop

import (
	"context"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/memory"
	"github.com/OrdoAI/yuri-agent/internal/reflection"
)

func TestReflectOnTurnPersistsPersonaRelationshipOpinionAndAffectAtomically(t *testing.T) {
	bridge := newPersonalityTestBridge(t)
	bridge.reflectionRuns = reflection.NewCoordinator()
	provider := &reflectionBackendStub{events: []agent.ModelEvent{
		{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"changed","reason":"repeated cooperative signal","evidence_ids":["message-user"],"relationship":{"dimensions":{"trust":0.05},"opinions":[{"subject":"owner","topic":"communication","claim":"Пользователь предпочитает проверяемые изменения","label":"inference","confidence":0.8,"reason":"Явная просьба продолжать с проверками","evidence_ids":["message-user"]}],"reason":"cooperative interaction","confidence":0.8},"affect":{"dimensions":{"joy":0.05},"reason":"productive turn","confidence":0.8},"persona":{"traits":{"warmth":0.05},"reason":"positive repeated interaction","confidence":0.8}}`},
		{Type: agent.ModelEventCompleted, Usage: agent.Usage{InputTokens: 100, OutputTokens: 80, TotalTokens: 180}},
	}}
	now := time.Now().UTC()
	bridge.reflectOnTurn(context.Background(), provider, "test-model", memory.Turn{
		RunID: "run-reflection", ConversationID: "conversation-1", Now: now,
		Messages: []memory.TranscriptMessage{{ID: "message-user", ConversationID: "conversation-1", Role: string(agent.RoleUser), Content: "Продолжай и обязательно проверяй изменения", CreatedAt: now}},
	})
	snapshot, err := bridge.GetPersonalitySnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CurrentVersion != 2 || snapshot.Relationship.Version != 2 || snapshot.Affect.Version != 2 {
		t.Fatalf("reflection versions = persona:%d relationship:%d affect:%d", snapshot.CurrentVersion, snapshot.Relationship.Version, snapshot.Affect.Version)
	}
	if len(snapshot.Opinions) != 1 || snapshot.Opinions[0].Label != "inference" || snapshot.Opinions[0].Confidence != .8 {
		t.Fatalf("subjective opinions = %#v", snapshot.Opinions)
	}
	if snapshot.LastReflectionAt == "" {
		t.Fatal("last reflection timestamp was not exposed")
	}
	if len(snapshot.Affect.Evidence) != 1 || snapshot.Affect.Evidence[0].MessageID != "message-user" {
		t.Fatalf("affect evidence = %#v", snapshot.Affect.Evidence)
	}
	persona, _ := bridge.repositories.Persona.Get(context.Background(), domain.ID("owner"))
	if persona.AuthorRunID != "run-reflection" || persona.Traits["warmth"] <= .58 {
		t.Fatalf("persisted persona = %#v", persona)
	}
}

func TestReflectOnTurnRespectsDisabledAutoEvolution(t *testing.T) {
	bridge := newPersonalityTestBridge(t)
	bridge.config.Persona.AutoEvolution = false
	provider := &reflectionBackendStub{events: []agent.ModelEvent{{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"no_change","reason":"none"}`}}}
	bridge.reflectOnTurn(context.Background(), provider, "test", memory.Turn{RunID: "run-disabled", ConversationID: "conversation", Now: time.Now().UTC()})
	if provider.request.Model != "" {
		t.Fatal("disabled auto evolution still invoked the model")
	}
}

func TestReflectOnTurnPersistsDecayWhenAnalyzerReturnsNoChange(t *testing.T) {
	bridge := newPersonalityTestBridge(t)
	bridge.reflectionRuns = reflection.NewCoordinator()
	before, err := bridge.repositories.Affect.Get(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	provider := &reflectionBackendStub{events: []agent.ModelEvent{
		{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"no_change","reason":"nothing durable changed"}`},
		{Type: agent.ModelEventCompleted, Usage: agent.Usage{InputTokens: 20, OutputTokens: 10, TotalTokens: 30}},
	}}
	bridge.reflectOnTurn(context.Background(), provider, "test-model", memory.Turn{
		RunID: "run-decay", ConversationID: "conversation-1", Now: before.UpdatedAt.Add(24 * time.Hour),
		Messages: []memory.TranscriptMessage{{ID: "message-user", ConversationID: "conversation-1", Role: string(agent.RoleUser), Content: "Привет", CreatedAt: before.UpdatedAt.Add(24 * time.Hour)}},
	})
	after, err := bridge.repositories.Affect.Get(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != before.Version+1 || after.Emotions["joy"] >= before.Emotions["joy"] {
		t.Fatalf("decay was not persisted: before=%#v after=%#v", before, after)
	}
	persona, _ := bridge.repositories.Persona.Get(context.Background(), "owner")
	relationship, _ := bridge.repositories.Relationship.Get(context.Background(), "owner")
	if persona.Version != 1 || relationship.Version != 1 {
		t.Fatalf("no-change decay mutated other targets: persona=%d relationship=%d", persona.Version, relationship.Version)
	}
}

func TestReflectionMutationRejectsSecretLikePersonaAndOpinionContent(t *testing.T) {
	_, err := reflectionMutation(reflection.ReflectionResult{Proposal: reflection.ReflectionProposal{
		Persona: &reflection.PersonaDelta{PromptDelta: "remember api_key=secret"},
	}}, reflectionDomainState{}, nil)
	if err == nil {
		t.Fatal("secret-like persona proposal was accepted")
	}
	_, err = reflectionMutation(reflection.ReflectionResult{Proposal: reflection.ReflectionProposal{
		Relationship: &reflection.RelationshipDelta{Opinions: []reflection.OpinionDelta{{Claim: "password is hunter2"}}},
	}}, reflectionDomainState{}, nil)
	if err == nil {
		t.Fatal("secret-like opinion proposal was accepted")
	}
}
