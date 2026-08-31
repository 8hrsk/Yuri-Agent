package desktop

import (
	"context"
	"encoding/json"
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
	events, err := bridge.repositories.Affect.ListEvents(context.Background(), "owner", 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("affect events = %#v, %v", events, err)
	}
	if events[0].Intensity <= 0 || events[0].Intensity >= .05 || events[0].HalfLifeSeconds <= 0 || events[0].Provenance != "reflection_appraisal" {
		t.Fatalf("bounded affect event = %#v", events[0])
	}
	var metadata map[string]any
	if json.Unmarshal([]byte(events[0].MetadataJSON), &metadata) != nil || metadata["appraisal"] != "bounded_v1" || metadata["raw_delta"] != .05 {
		t.Fatalf("affect appraisal metadata = %q", events[0].MetadataJSON)
	}
}

func TestReflectOnTurnAccumulatesAffectInsideDurableCooldown(t *testing.T) {
	bridge := newPersonalityTestBridge(t)
	bridge.reflectionRuns = reflection.NewCoordinator()
	// The installation fallback is disabled; the stored agent policy still
	// supplies its own 60-minute durable cooldown.
	bridge.config.Persona.ReflectionCooldownMinutes = 0
	before, err := bridge.repositories.Affect.Get(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	firstProvider := &reflectionBackendStub{events: []agent.ModelEvent{
		{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"changed","reason":"warm response","evidence_ids":["message-1"],"persona":{"traits":{"warmth":0.04},"reason":"cooperative turn","confidence":0.8},"affect":{"dimensions":{"joy":0.05},"reason":"pleasant turn","confidence":0.8}}`},
		{Type: agent.ModelEventCompleted},
	}}
	firstAt := before.UpdatedAt.Add(time.Minute)
	bridge.reflectOnTurn(context.Background(), firstProvider, "test-model", memory.Turn{
		RunID: "run-affect-1", ConversationID: "conversation-1", Now: firstAt,
		Messages: []memory.TranscriptMessage{{ID: "message-1", ConversationID: "conversation-1", Role: string(agent.RoleUser), Content: "Спасибо, это помогло", CreatedAt: firstAt}},
	})
	firstPersona, _ := bridge.repositories.Persona.Get(context.Background(), "owner")
	firstAffect, _ := bridge.repositories.Affect.Get(context.Background(), "owner")

	secondProvider := &reflectionBackendStub{events: []agent.ModelEvent{
		{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"changed","reason":"another pleasant event","evidence_ids":["message-2"],"persona":{"traits":{"warmth":0.04},"reason":"repeated cooperation","confidence":0.8},"affect":{"dimensions":{"joy":0.05},"reason":"continued pleasant exchange","confidence":0.8}}`},
		{Type: agent.ModelEventCompleted},
	}}
	secondAt := firstAt.Add(time.Minute)
	bridge.reflectOnTurn(context.Background(), secondProvider, "test-model", memory.Turn{
		RunID: "run-affect-2", ConversationID: "conversation-1", Now: secondAt,
		Messages: []memory.TranscriptMessage{{ID: "message-2", ConversationID: "conversation-1", Role: string(agent.RoleUser), Content: "И это тоже отлично", CreatedAt: secondAt}},
	})
	secondPersona, _ := bridge.repositories.Persona.Get(context.Background(), "owner")
	secondAffect, _ := bridge.repositories.Affect.Get(context.Background(), "owner")
	if secondPersona.Version != firstPersona.Version {
		t.Fatalf("durable persona bypassed cooldown: first=%d second=%d", firstPersona.Version, secondPersona.Version)
	}
	if secondAffect.Version != firstAffect.Version+1 || secondAffect.Emotions[domain.EmotionJoy] <= firstAffect.Emotions[domain.EmotionJoy] {
		t.Fatalf("affect did not accumulate inside durable cooldown: first=%#v second=%#v", firstAffect, secondAffect)
	}
	events, err := bridge.repositories.Affect.ListEvents(context.Background(), "owner", 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("accumulated affect events = %#v, %v", events, err)
	}
}

func TestReflectOnTurnUsesEachAgentsStoredEmotionalDynamics(t *testing.T) {
	run := func(t *testing.T, dynamics domain.EmotionalDynamics, configureTemperament func(*domain.Temperament)) domain.AffectiveEvent {
		t.Helper()
		bridge := newPersonalityTestBridge(t)
		bridge.reflectionRuns = reflection.NewCoordinator()
		seed, err := bridge.repositories.Personalization.Get(context.Background(), "owner")
		if err != nil {
			t.Fatal(err)
		}
		configureTemperament(&seed.Temperament)
		seed.EmotionalDynamics = dynamics
		seed.Version++
		seed.RevisionID = ""
		seed.Operation = domain.PersonalizationOperationUpdate
		seed.Reason = "test emotional dynamics"
		seed.UpdatedAt = seed.UpdatedAt.Add(time.Second)
		if _, err = bridge.repositories.Personalization.AppendVersion(context.Background(), seed, seed.Version-1); err != nil {
			t.Fatal(err)
		}
		before, _ := bridge.repositories.Affect.Get(context.Background(), "owner")
		at := before.UpdatedAt.Add(time.Minute)
		provider := &reflectionBackendStub{events: []agent.ModelEvent{
			{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"changed","reason":"positive appraisal","evidence_ids":["message-profile"],"affect":{"dimensions":{"joy":0.1},"reason":"good news","confidence":0.8}}`},
			{Type: agent.ModelEventCompleted},
		}}
		bridge.reflectOnTurn(context.Background(), provider, "test-model", memory.Turn{
			RunID: "run-profile-affect", ConversationID: "conversation-profile", Now: at,
			Messages: []memory.TranscriptMessage{{ID: "message-profile", ConversationID: "conversation-profile", Role: string(agent.RoleUser), Content: "У нас отличные новости", CreatedAt: at}},
		})
		events, err := bridge.repositories.Affect.ListEvents(context.Background(), "owner", 1)
		if err != nil || len(events) != 1 {
			t.Fatalf("profile affect event = %#v, %v", events, err)
		}
		return events[0]
	}
	lowDynamics := domain.EmotionalDynamics{Reactivity: 0, ResponseIntensity: 0, RecoverySpeed: 1, PositivePersistence: 0, NegativePersistence: 0, Expression: .2, Masking: .8, ConflictStyle: "adaptive", Triggers: map[string][]string{}, SoothingStrategies: []string{}}
	highDynamics := domain.EmotionalDynamics{Reactivity: 1, ResponseIntensity: 1, RecoverySpeed: 0, PositivePersistence: 1, NegativePersistence: 1, Expression: 1, Masking: 0, ConflictStyle: "direct", Triggers: map[string][]string{}, SoothingStrategies: []string{}}
	low := run(t, lowDynamics, func(value *domain.Temperament) { value.Optimism, value.Emotionality = 0, 0 })
	high := run(t, highDynamics, func(value *domain.Temperament) { value.Optimism, value.Emotionality = 1, 1 })
	if high.Intensity <= low.Intensity || high.HalfLifeSeconds <= low.HalfLifeSeconds {
		t.Fatalf("stored profiles did not diverge: low=%#v high=%#v", low, high)
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

func TestReflectOnTurnRespectsPerAgentReflectionMode(t *testing.T) {
	bridge := newPersonalityTestBridge(t)
	bridge.config.Persona.AutoEvolution = true
	seed, err := bridge.repositories.Personalization.Get(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	seed.Version++
	seed.RevisionID = ""
	seed.Operation = domain.PersonalizationOperationUpdate
	seed.EvolutionPolicy.ReflectionMode = domain.PersonalizationReflectionDisabled
	seed.Reason = "owner disabled this agent reflection"
	seed.UpdatedAt = seed.UpdatedAt.Add(time.Second)
	if _, err = bridge.repositories.Personalization.AppendVersion(context.Background(), seed, seed.Version-1); err != nil {
		t.Fatal(err)
	}
	provider := &reflectionBackendStub{events: []agent.ModelEvent{{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"no_change","reason":"none"}`}}}
	bridge.reflectOnTurn(context.Background(), provider, "test", memory.Turn{
		RunID: "run-disabled-agent", ConversationID: "conversation", Now: seed.UpdatedAt.Add(time.Minute),
		Messages: []memory.TranscriptMessage{{ID: "message", Role: string(agent.RoleUser), Content: "Привет", CreatedAt: seed.UpdatedAt.Add(time.Minute)}},
	})
	if provider.request.Model != "" {
		t.Fatal("per-agent disabled reflection still invoked the model")
	}
}

func TestReflectionBudgetForPolicyUsesOverridesAndLegacyFallback(t *testing.T) {
	fallback := reflection.ReflectionBudget{MaxDuration: 45 * time.Second, MaxTokens: 1_200, MaxInputBytes: 32, MaxOutputBytes: 16, MaxEvidence: 8}
	legacy := reflectionBudgetForPolicy(domain.PersonalizationEvolutionPolicy{}, fallback)
	if legacy != fallback {
		t.Fatalf("legacy reflection budget = %#v, want %#v", legacy, fallback)
	}
	custom := reflectionBudgetForPolicy(domain.PersonalizationEvolutionPolicy{
		ReflectionMaxTokens: 900, ReflectionMaxDurationSecs: 30, ReflectionMaxEvidence: 4,
	}, fallback)
	if custom.MaxTokens != 900 || custom.MaxDuration != 30*time.Second || custom.MaxEvidence != 4 || custom.MaxInputBytes != 32 || custom.MaxOutputBytes != 16 {
		t.Fatalf("custom reflection budget = %#v", custom)
	}
}

func TestReflectOnTurnRespectsVersionedLayerLocks(t *testing.T) {
	bridge := newPersonalityTestBridge(t)
	bridge.reflectionRuns = reflection.NewCoordinator()
	seed, err := bridge.repositories.Personalization.Get(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	seed.Version++
	seed.RevisionID = ""
	seed.Operation = domain.PersonalizationOperationUpdate
	seed.EvolutionPolicy.LockedFields = append(seed.EvolutionPolicy.LockedFields, "mutable_persona", "relationship", "affect")
	seed.Reason = "owner locked every mutable reflection layer"
	seed.UpdatedAt = seed.UpdatedAt.Add(time.Second)
	if _, err = bridge.repositories.Personalization.AppendVersion(context.Background(), seed, seed.Version-1); err != nil {
		t.Fatal(err)
	}
	provider := &reflectionBackendStub{events: []agent.ModelEvent{
		{Type: agent.ModelEventTextDelta, Delta: `{"outcome":"changed","reason":"candidate","evidence_ids":["message"],"persona":{"traits":{"warmth":0.05},"reason":"candidate","confidence":0.8},"relationship":{"dimensions":{"trust":0.05},"reason":"candidate","confidence":0.8},"affect":{"dimensions":{"joy":0.05},"reason":"candidate","confidence":0.8}}`},
		{Type: agent.ModelEventCompleted},
	}}
	bridge.reflectOnTurn(context.Background(), provider, "test", memory.Turn{
		RunID: "run-locked-layers", ConversationID: "conversation", Now: seed.UpdatedAt.Add(time.Minute),
		Messages: []memory.TranscriptMessage{{ID: "message", Role: string(agent.RoleUser), Content: "Спасибо", CreatedAt: seed.UpdatedAt.Add(time.Minute)}},
	})
	persona, _ := bridge.repositories.Persona.Get(context.Background(), "owner")
	relationship, _ := bridge.repositories.Relationship.Get(context.Background(), "owner")
	affect, _ := bridge.repositories.Affect.Get(context.Background(), "owner")
	if persona.Version != 1 || relationship.Version != 1 || affect.Version != 1 {
		t.Fatalf("locked reflection persisted state: persona=%d relationship=%d affect=%d", persona.Version, relationship.Version, affect.Version)
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
