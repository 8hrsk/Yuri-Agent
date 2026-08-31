package desktop

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/config"
	contextbuilder "github.com/OrdoAI/yuri-agent/internal/context"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/memory"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func newAgentTestBridge(t *testing.T) *Bridge {
	t.Helper()
	root := t.TempDir()
	paths := config.Paths{
		ConfigDirectory: filepath.Join(root, "config"), ConfigFile: filepath.Join(root, "config", "config.json"),
		DataDirectory: filepath.Join(root, "data"), DatabaseFile: filepath.Join(root, "data", "yuri.sqlite3"),
		PebbleDirectory: filepath.Join(root, "data", "pebble"), BlobDirectory: filepath.Join(root, "data", "blobs"),
		LogDirectory: filepath.Join(root, "data", "logs"), PluginDirectory: filepath.Join(root, "data", "plugins"),
	}
	database, err := storage.Open(context.Background(), paths.DatabaseFile)
	if err != nil {
		t.Fatal(err)
	}
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	bridge := &Bridge{database: database, repositories: repositories, paths: paths, config: config.Default(paths)}
	t.Cleanup(func() { _ = database.Close() })
	return bridge
}

func TestActiveAgentCannotReusePeerConversation(t *testing.T) {
	bridge := newAgentTestBridge(t)
	first, err := bridge.CreateAgent(CreateAgentInput{Name: "Юри", Gender: "female"})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := bridge.NewConversation("Приватный диалог Юри")
	if err != nil {
		t.Fatal(err)
	}
	second, err := bridge.CreateAgent(CreateAgentInput{Name: "Мира", Gender: "female"})
	if err != nil {
		t.Fatal(err)
	}
	visible, err := bridge.ListConversations()
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 0 {
		t.Fatalf("peer conversations leaked to %s: %#v", second.Name, visible)
	}
	err = bridge.ensureConversation(context.Background(), domain.ID(conversation.ID), "чужой диалог", time.Now().UTC())
	if err == nil || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-agent conversation error = %v, want explicit ownership rejection", err)
	}
	if _, err := bridge.SetActiveAgent(SelectAgentInput{ID: first.ID}); err != nil {
		t.Fatal(err)
	}
	visible, err = bridge.ListConversations()
	if err != nil || len(visible) != 1 || visible[0].ID != conversation.ID {
		t.Fatalf("owner conversations = %#v, err = %v", visible, err)
	}
}

func TestCreateAgentPersistsOwnerIdentityAndInitialPersona(t *testing.T) {
	bridge := newAgentTestBridge(t)
	created, err := bridge.CreateAgent(CreateAgentInput{
		Name: "Аки", Age: 23, Gender: "female", Preferences: "Любит точность и сухой юмор.",
		Backstory: "В детстве Аки пряталась в старой библиотеке и мечтала стать хранительницей знаний.",
		Traits:    map[string]float64{"warmth": .31, "directness": .91},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created.Active || created.Name != "Аки" || created.Backstory == "" || created.Traits["warmth"] != .31 {
		t.Fatalf("created agent = %#v", created)
	}
	state := bridge.GetOnboardingState()
	if !state.AgentConfigured || state.Completed || state.ProviderTested || state.ActiveAgentID != created.ID {
		t.Fatalf("onboarding state = %#v", state)
	}
	persona, err := bridge.repositories.Persona.Get(context.Background(), bridge.personaProfileID())
	if err != nil {
		t.Fatal(err)
	}
	if persona.Traits["directness"] != .91 || !strings.Contains(persona.Prompt(), "Аки") || !strings.Contains(persona.Prompt(), "сухой юмор") || strings.Contains(persona.Prompt(), "В детстве Аки") {
		t.Fatalf("persona seed = %#v", persona)
	}
	profile, err := bridge.repositories.Agents.Get(context.Background(), domain.ID(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	if profile.Backstory == "" {
		t.Fatal("agent backstory was not persisted")
	}
	if _, err := bridge.repositories.Relationship.Get(context.Background(), domain.ID(created.ID)); err != nil {
		t.Fatalf("relationship defaults missing: %v", err)
	}
	if _, err := bridge.repositories.Affect.Get(context.Background(), domain.ID(created.ID)); err != nil {
		t.Fatalf("affect defaults missing: %v", err)
	}
	personalization, err := bridge.repositories.Personalization.Get(context.Background(), domain.ID(created.ID))
	if err != nil {
		t.Fatalf("personalization v2 seed missing: %v", err)
	}
	if personalization.SchemaVersion != domain.PersonalizationSchemaVersion || personalization.Temperament.Warmth != .31 || personalization.Temperament.Directness != .91 || personalization.Backstory.Narrative != profile.Backstory {
		t.Fatalf("personalization v2 seed = %#v", personalization)
	}
	view, err := bridge.GetActiveAgentPersonalization()
	if err != nil || view.AgentID != created.ID || view.SchemaVersion != domain.PersonalizationSchemaVersion || view.Temperament.Warmth != .31 {
		t.Fatalf("personalization bridge view = %#v, %v", view, err)
	}
	loaded, err := config.Load(bridge.paths)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Persona.ProfileID != created.ID || !loaded.Onboarding.AgentConfigured {
		t.Fatalf("persisted config = %#v", loaded)
	}
}

func TestUpdateActiveAgentPersonalizationAppendsOwnerBaselineWithoutResettingRuntime(t *testing.T) {
	bridge := newAgentTestBridge(t)
	created, err := bridge.CreateAgent(CreateAgentInput{Name: "Эми", Gender: "female"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	agentID := domain.ID(created.ID)
	seed, err := bridge.repositories.Personalization.Get(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	personaBefore, _ := bridge.repositories.Persona.Get(ctx, agentID)
	relationshipBefore, _ := bridge.repositories.Relationship.Get(ctx, agentID)
	affectBefore, _ := bridge.repositories.Affect.Get(ctx, agentID)
	auditBefore, _ := bridge.repositories.Audit.List(ctx, 20, 0)
	input := updatePersonalizationInputFromSeed(seed)
	input.ExpectedVersion = seed.Version
	input.Reason = "Владелец сделал характер теплее"
	input.Traits["warmth"] = .91
	input.Personalization.CommunicationStyle.Humor = .82
	input.Personalization.Identity.Role = "картограф"
	input.Personalization.StructuredBackstory.Summary = "Картограф, которая учится доверять людям."

	updated, err := bridge.UpdateActiveAgentPersonalization(input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != seed.Version+1 || updated.Temperament.Warmth != .91 || updated.CommunicationStyle.Humor != .82 || updated.Reason != input.Reason {
		t.Fatalf("updated owner seed = %#v", updated)
	}
	original, err := bridge.repositories.Personalization.GetVersion(ctx, agentID, seed.Version)
	if err != nil || original.Temperament.Warmth == .91 || original.Identity.Role == "картограф" {
		t.Fatalf("previous owner seed changed = %#v, %v", original, err)
	}
	personaAfter, _ := bridge.repositories.Persona.Get(ctx, agentID)
	relationshipAfter, _ := bridge.repositories.Relationship.Get(ctx, agentID)
	affectAfter, _ := bridge.repositories.Affect.Get(ctx, agentID)
	if personaAfter.Version != personaBefore.Version || personaAfter.Traits["warmth"] != personaBefore.Traits["warmth"] || relationshipAfter.Version != relationshipBefore.Version || affectAfter.Version != affectBefore.Version {
		t.Fatalf("owner baseline update reset runtime: persona=%#v relationship=%#v affect=%#v", personaAfter, relationshipAfter, affectAfter)
	}
	audit, err := bridge.repositories.Audit.List(ctx, 10, 0)
	if err != nil || len(audit) != len(auditBefore)+1 || audit[0].Action != "personalization.owner_seed.update" || audit[0].Actor != domain.ActorUser {
		t.Fatalf("owner seed audit = %#v, %v", audit, err)
	}
	if strings.Contains(audit[0].PayloadRedacted, input.Reason) {
		t.Fatalf("owner reason leaked into redacted audit payload: %s", audit[0].PayloadRedacted)
	}
	if _, err := bridge.UpdateActiveAgentPersonalization(input); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale owner revision error = %v", err)
	}
	auditAfterConflict, _ := bridge.repositories.Audit.List(ctx, 10, 0)
	if len(auditAfterConflict) != len(audit) {
		t.Fatalf("stale update wrote audit: %#v", auditAfterConflict)
	}
}

func updatePersonalizationInputFromSeed(seed domain.PersonalizationSeed) UpdateAgentPersonalizationInput {
	bounds := make(map[string]CreateAgentNumericRangeInput, len(seed.EvolutionPolicy.TraitBounds))
	for name, value := range seed.EvolutionPolicy.TraitBounds {
		bounds[name] = CreateAgentNumericRangeInput{Min: value.Min, Max: value.Max}
	}
	episodes := make([]CreateAgentBackstoryEpisodeInput, 0, len(seed.Backstory.Episodes))
	for _, episode := range seed.Backstory.Episodes {
		episodes = append(episodes, CreateAgentBackstoryEpisodeInput{
			ID: episode.ID, Title: episode.Title, Content: episode.Content, Kind: episode.Kind,
			People: append([]string(nil), episode.People...), Place: episode.Place,
			EmotionalValence: episode.EmotionalValence, Sequence: episode.Sequence,
		})
	}
	return UpdateAgentPersonalizationInput{
		ExpectedVersion: seed.Version, Traits: seed.Temperament.Traits(),
		Personalization: CreateAgentPersonalizationInput{
			Identity: CreateAgentIdentityInput{
				PreferredLanguage: seed.Identity.PreferredLanguage, Pronouns: seed.Identity.Pronouns,
				UserAddress: seed.Identity.UserAddress, SelfDescription: seed.Identity.SelfDescription, Role: seed.Identity.Role,
			},
			CommunicationStyle: CreateAgentCommunicationStyleInput{
				Verbosity: seed.CommunicationStyle.Verbosity, Softness: seed.CommunicationStyle.Softness,
				Humor: seed.CommunicationStyle.Humor, Figurativeness: seed.CommunicationStyle.Figurativeness,
				Expressiveness: seed.CommunicationStyle.Expressiveness, Supportiveness: seed.CommunicationStyle.Supportiveness,
				Formality: seed.CommunicationStyle.Formality, Teasing: seed.CommunicationStyle.Teasing,
				EmojiFrequency: seed.CommunicationStyle.EmojiFrequency, Flirtation: seed.CommunicationStyle.Flirtation,
				ConversationalInitiative: seed.CommunicationStyle.ConversationalInitiative,
			},
			EmotionalDynamics: CreateAgentEmotionalDynamicsInput{
				Reactivity: seed.EmotionalDynamics.Reactivity, ResponseIntensity: seed.EmotionalDynamics.ResponseIntensity,
				RecoverySpeed: seed.EmotionalDynamics.RecoverySpeed, PositivePersistence: seed.EmotionalDynamics.PositivePersistence,
				NegativePersistence: seed.EmotionalDynamics.NegativePersistence, Expression: seed.EmotionalDynamics.Expression,
				Masking: seed.EmotionalDynamics.Masking, ConflictStyle: seed.EmotionalDynamics.ConflictStyle,
				Triggers: seed.EmotionalDynamics.Triggers, SoothingStrategies: seed.EmotionalDynamics.SoothingStrategies,
			},
			RelationshipSeed: CreateAgentRelationshipSeedInput{
				Preset: string(seed.RelationshipSeed.Preset), Dimensions: seed.RelationshipSeed.Dimensions, Summary: seed.RelationshipSeed.Summary,
			},
			StructuredBackstory: CreateAgentStructuredBackstoryInput{
				Narrative: seed.Backstory.Narrative, Summary: seed.Backstory.Summary, Episodes: episodes,
			},
			EvolutionPolicy: CreateAgentEvolutionPolicyInput{
				LockedFields: seed.EvolutionPolicy.LockedFields, TraitBounds: bounds,
				ReflectionMode: string(seed.EvolutionPolicy.ReflectionMode), ReflectionCooldownMinutes: seed.EvolutionPolicy.ReflectionCooldownMinutes,
			},
		},
	}
}

func TestCreateAgentRoundTripsPersonalizationProfileV2(t *testing.T) {
	bridge := newAgentTestBridge(t)
	now := time.Date(2026, 8, 31, 20, 0, 0, 0, time.UTC)
	templateProfile, err := domain.NewAgentProfileWithBackstory("template", "Эми", 22, "female", "Спокойная исследовательница.", "Выросла рядом с обсерваторией.", now)
	if err != nil {
		t.Fatal(err)
	}
	template, err := domain.NewPersonalizationSeed(templateProfile, map[string]float64{"warmth": .81, "fearfulness": .67}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	template.Identity.Pronouns = "она/её"
	template.Identity.UserAddress = "капитан"
	template.Identity.Role = "звёздный картограф"
	template.CommunicationStyle.Verbosity = .83
	template.CommunicationStyle.Figurativeness = .77
	template.EmotionalDynamics.ConflictStyle = "cold"
	template.EmotionalDynamics.Triggers = map[string][]string{"fear": {"потеря связи"}}
	template.EmotionalDynamics.SoothingStrategies = []string{"свериться с картой"}
	template.RelationshipSeed = domain.RelationshipSeed{Preset: domain.RelationshipSeedFriends, Dimensions: map[string]float64{"trust": .72, "respect": .78, "closeness": .66}, Summary: "Давно исследуют вместе."}
	template.Backstory = domain.StructuredBackstory{
		Narrative: templateProfile.Backstory, Summary: "Картограф, выросшая у обсерватории.",
		Episodes: []domain.BackstoryEpisode{{ID: "first-comet", Title: "Первая комета", Content: "Впервые увидела комету вместе с наставницей.", Kind: "formative", People: []string{"наставница"}, Place: "обсерватория", EmotionalValence: .8, Sequence: 1}},
	}
	created, err := bridge.CreateAgent(CreateAgentInput{
		Name: templateProfile.Name, Age: templateProfile.Age, Gender: templateProfile.Gender,
		Preferences: templateProfile.Preferences, Backstory: templateProfile.Backstory,
		Traits: map[string]float64{"warmth": .81, "fearfulness": .67},
		Personalization: &CreateAgentPersonalizationInput{
			Identity: CreateAgentIdentityInput{
				PreferredLanguage: template.Identity.PreferredLanguage, Pronouns: template.Identity.Pronouns,
				UserAddress: template.Identity.UserAddress, SelfDescription: template.Identity.SelfDescription, Role: template.Identity.Role,
			},
			CommunicationStyle: CreateAgentCommunicationStyleInput{
				Verbosity: template.CommunicationStyle.Verbosity, Softness: template.CommunicationStyle.Softness,
				Humor: template.CommunicationStyle.Humor, Figurativeness: template.CommunicationStyle.Figurativeness,
				Expressiveness: template.CommunicationStyle.Expressiveness, Supportiveness: template.CommunicationStyle.Supportiveness,
				Formality: template.CommunicationStyle.Formality, Teasing: template.CommunicationStyle.Teasing,
				EmojiFrequency: template.CommunicationStyle.EmojiFrequency, Flirtation: template.CommunicationStyle.Flirtation,
				ConversationalInitiative: template.CommunicationStyle.ConversationalInitiative,
			},
			EmotionalDynamics: CreateAgentEmotionalDynamicsInput{
				Reactivity: template.EmotionalDynamics.Reactivity, ResponseIntensity: template.EmotionalDynamics.ResponseIntensity,
				RecoverySpeed: template.EmotionalDynamics.RecoverySpeed, PositivePersistence: template.EmotionalDynamics.PositivePersistence,
				NegativePersistence: template.EmotionalDynamics.NegativePersistence, Expression: template.EmotionalDynamics.Expression,
				Masking: template.EmotionalDynamics.Masking, ConflictStyle: template.EmotionalDynamics.ConflictStyle,
				Triggers: template.EmotionalDynamics.Triggers, SoothingStrategies: template.EmotionalDynamics.SoothingStrategies,
			},
			RelationshipSeed: CreateAgentRelationshipSeedInput{
				Preset: string(template.RelationshipSeed.Preset), Dimensions: template.RelationshipSeed.Dimensions, Summary: template.RelationshipSeed.Summary,
			},
			StructuredBackstory: CreateAgentStructuredBackstoryInput{
				Narrative: template.Backstory.Narrative, Summary: template.Backstory.Summary,
				Episodes: []CreateAgentBackstoryEpisodeInput{{ID: "first-comet", Title: "Первая комета", Content: "Впервые увидела комету вместе с наставницей.", Kind: "formative", People: []string{"наставница"}, Place: "обсерватория", EmotionalValence: .8, Sequence: 1}},
			},
			EvolutionPolicy: CreateAgentEvolutionPolicyInput{
				LockedFields: template.EvolutionPolicy.LockedFields, TraitBounds: map[string]CreateAgentNumericRangeInput{"warmth": {Min: 0, Max: 1}, "fearfulness": {Min: 0, Max: 1}},
				ReflectionMode: "disabled", ReflectionCooldownMinutes: 90,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := bridge.repositories.Personalization.Get(context.Background(), domain.ID(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Identity.Pronouns != "она/её" || stored.Identity.UserAddress != "капитан" || stored.Identity.Role != "звёздный картограф" ||
		stored.CommunicationStyle.Verbosity != .83 || stored.CommunicationStyle.Figurativeness != .77 ||
		stored.Temperament.Warmth != .81 || stored.Temperament.Fearfulness != .67 ||
		stored.EmotionalDynamics.ConflictStyle != "cold" || stored.EmotionalDynamics.Triggers["fear"][0] != "потеря связи" ||
		stored.RelationshipSeed.Preset != domain.RelationshipSeedFriends || stored.RelationshipSeed.Dimensions["closeness"] != .66 ||
		stored.Backstory.Summary != "Картограф, выросшая у обсерватории." || len(stored.Backstory.Episodes) != 1 || stored.Backstory.Episodes[0].ID != "first-comet" ||
		stored.EvolutionPolicy.TraitBounds["warmth"] != (domain.NumericRange{Min: 0, Max: 1}) || stored.EvolutionPolicy.ReflectionMode != domain.PersonalizationReflectionDisabled || stored.EvolutionPolicy.ReflectionCooldownMinutes != 90 {
		t.Fatalf("personalization v2 did not round-trip: %#v", stored)
	}
	ownerRelationship, err := bridge.repositories.Relationship.Get(context.Background(), domain.ID(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	if ownerRelationship.Version != 1 || ownerRelationship.Dimensions["trust"] != .72 || ownerRelationship.Dimensions["closeness"] != .66 || ownerRelationship.Summary != template.RelationshipSeed.Summary ||
		!strings.Contains(ownerRelationship.Reason, string(domain.RelationshipSeedFriends)) || len(ownerRelationship.Evidence) != 1 || ownerRelationship.Evidence[0].Provenance != "owner_relationship_seed" {
		t.Fatalf("owner relationship did not initialize from personalization seed: %#v", ownerRelationship)
	}
	view, err := bridge.GetActiveAgentPersonalization()
	if err != nil || view.AgentID != created.ID || view.Identity.UserAddress != "капитан" || view.Backstory.Episodes[0].Place != "обсерватория" {
		t.Fatalf("personalization view = %#v, %v", view, err)
	}
}

func TestStructuredBackstoryHydrationRoundTripsThroughSQLite(t *testing.T) {
	bridge := newAgentTestBridge(t)
	created, err := bridge.CreateAgent(CreateAgentInput{Name: "Эми", Gender: "female"})
	if err != nil {
		t.Fatal(err)
	}
	agentID := domain.ID(created.ID)
	seed, err := bridge.repositories.Personalization.Get(context.Background(), agentID)
	if err != nil {
		t.Fatal(err)
	}
	seed.ParentID = seed.RevisionID
	seed.ParentVersion = seed.Version
	seed.Version++
	seed.RevisionID = domain.ID(fmt.Sprintf("%s:personalization:v%d", agentID, seed.Version))
	seed.Operation = domain.PersonalizationOperationUpdate
	seed.Reason = "owner added structured backstory"
	seed.UpdatedAt = seed.UpdatedAt.Add(time.Minute)
	seed.Backstory.Narrative = "RAW-NARRATIVE-ONLY: подробная история, которая не должна постоянно попадать в prompt."
	seed.Backstory.Summary = "Картограф, выросшая рядом с обсерваторией."
	seed.Backstory.Episodes = []domain.BackstoryEpisode{{
		ID: "first-comet", Title: "Первая комета",
		Content: "Впервые увидела комету вместе с наставницей.",
		Kind:    "formative", People: []string{"наставница"}, Place: "обсерватория",
		EmotionalValence: .8, Sequence: 1,
	}}
	seed, err = bridge.repositories.Personalization.AppendVersion(context.Background(), seed, seed.ParentVersion)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := bridge.newMemoryEngine(nil, "", agentID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := engine.HydrateBackstory(context.Background(), seed)
	if err != nil || len(first) != 1 || !first[0].Created {
		t.Fatalf("hydrate = %#v, %v", first, err)
	}
	stored, err := bridge.repositories.Memories.GetForAgent(context.Background(), agentID, first[0].Memory.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := memory.ParseBackstoryMemoryPayload(stored.ContentJSON)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Nature != domain.MemoryNatureFiction || payload.EpisodeID != "first-comet" || payload.PersonalizationRevisionID != seed.RevisionID {
		t.Fatalf("stored fictional memory = %#v, payload = %#v", stored, payload)
	}
	sources, err := bridge.repositories.Memories.ListSources(context.Background(), stored.ID)
	if err != nil || len(sources) != 1 || sources[0].SourceType != memory.BackstorySourceIdentitySeed {
		t.Fatalf("sources = %#v, %v", sources, err)
	}
	repeated, err := engine.HydrateBackstory(context.Background(), seed)
	if err != nil || repeated[0].Changed {
		t.Fatalf("repeat = %#v, %v", repeated, err)
	}
	recalled, err := engine.Recall(context.Background(), "комета", memory.RecallOptions{
		AgentID: agentID, Mode: memory.RecallAutomatic, Limit: 3, Now: seed.UpdatedAt,
	})
	if err != nil || len(recalled) != 1 || recalled[0].Memory.ID != stored.ID || recalled[0].Memory.Nature != domain.MemoryNatureFiction {
		t.Fatalf("fictional recall = %#v, %v", recalled, err)
	}
	conversationID := domain.ID("conversation-selective-backstory")
	if err := bridge.repositories.Conversations.Create(context.Background(), storage.Conversation{
		ID: conversationID, AgentID: agentID, Title: "Проверка памяти", CreatedAt: seed.UpdatedAt, UpdatedAt: seed.UpdatedAt,
	}); err != nil {
		t.Fatal(err)
	}
	assembler, err := contextbuilder.New(desktopContextSource{engine: engine, repositories: bridge.repositories, agentID: agentID}, contextbuilder.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	assemble := func(query string) string {
		t.Helper()
		snapshot, assembleErr := assembler.Assemble(context.Background(), contextbuilder.Input{
			AgentID: agentID, ConversationID: conversationID, Query: query,
			ImmutablePolicy: "POLICY", IdentitySeed: "IDENTITY",
			BackstorySummary: domain.BackstoryIdentitySummary(seed.Backstory),
			Transcript:       []agent.Message{{Role: agent.RoleUser, Content: query}},
		})
		if assembleErr != nil {
			t.Fatal(assembleErr)
		}
		var joined strings.Builder
		for _, message := range snapshot.Messages {
			joined.WriteString(message.Content)
			joined.WriteByte('\n')
		}
		return joined.String()
	}
	relevantContext := assemble("комета")
	if !strings.Contains(relevantContext, seed.Backstory.Summary) || !strings.Contains(relevantContext, "комет") ||
		!strings.Contains(relevantContext, "nature=fiction") || !strings.Contains(relevantContext, "source=identity_seed:") ||
		strings.Contains(relevantContext, "RAW-NARRATIVE-ONLY") {
		t.Fatalf("relevant context = %s", relevantContext)
	}
	unrelatedContext := assemble("налоги")
	if !strings.Contains(unrelatedContext, seed.Backstory.Summary) || strings.Contains(unrelatedContext, "Впервые увидела комету") ||
		strings.Contains(unrelatedContext, "RAW-NARRATIVE-ONLY") {
		t.Fatalf("unrelated context = %s", unrelatedContext)
	}
	versions, err := bridge.repositories.Memories.ListVersionsForAgent(context.Background(), agentID, stored.ID, 0)
	if err != nil || len(versions) != 1 {
		t.Fatalf("versions = %#v, %v", versions, err)
	}
}

func TestLegacyBackstoryIsMigratedAndHydratedBeforeContextUse(t *testing.T) {
	bridge := newAgentTestBridge(t)
	original := "Эми выросла рядом с маяком. По вечерам она записывала истории моряков."
	created, err := bridge.CreateAgent(CreateAgentInput{
		Name: "Эми", Gender: "female", Backstory: original,
	})
	if err != nil {
		t.Fatal(err)
	}
	agentID := domain.ID(created.ID)
	engine, err := bridge.newMemoryEngine(nil, "", agentID)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := bridge.hydrateAgentBackstory(context.Background(), engine, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if seed.Version != 2 || seed.Operation != domain.PersonalizationOperationMigration || seed.Backstory.Narrative != original ||
		len(seed.Backstory.Episodes) != 1 || seed.Backstory.Summary == "" {
		t.Fatalf("migrated seed = %#v", seed)
	}
	recalled, err := engine.Recall(context.Background(), "маяк", memory.RecallOptions{
		AgentID: agentID, Mode: memory.RecallAutomatic, Limit: 3, Now: time.Now().UTC(),
	})
	if err != nil || len(recalled) != 1 || recalled[0].Memory.Nature != domain.MemoryNatureFiction {
		t.Fatalf("legacy recall = %#v, %v", recalled, err)
	}
	firstMemory := recalled[0].Memory
	secondSeed, err := bridge.hydrateAgentBackstory(context.Background(), engine, agentID)
	if err != nil || secondSeed.Version != seed.Version {
		t.Fatalf("repeat seed = %#v, %v", secondSeed, err)
	}
	versions, err := bridge.repositories.Memories.ListVersionsForAgent(context.Background(), agentID, firstMemory.ID, 0)
	if err != nil || len(versions) != 1 {
		t.Fatalf("repeat memory versions = %#v, %v", versions, err)
	}
	originalSeed, err := bridge.repositories.Personalization.GetVersion(context.Background(), agentID, 1)
	if err != nil || originalSeed.Backstory.Narrative != original || len(originalSeed.Backstory.Episodes) != 0 {
		t.Fatalf("original seed was not preserved: %#v, %v", originalSeed, err)
	}
}

func TestAgentIdentitySeedDeclaresBackstoryBoundaryWithoutEmbeddingRawText(t *testing.T) {
	now := time.Now().UTC()
	profile, err := domain.NewAgentProfileWithBackstory("agent_yuri", "Юри", 21, "female", "", "Секретная вымышленная история", now)
	if err != nil {
		t.Fatal(err)
	}
	seed := agentIdentitySeed(profile, []domain.AgentProfile{profile})
	for _, required := range []string{"вымышленная личная история", "subjective identity summary", "fictional episodes", "не воспринимай их как факт", "разрешение"} {
		if !strings.Contains(strings.ToLower(seed), strings.ToLower(required)) {
			t.Fatalf("identity seed missing boundary %q: %s", required, seed)
		}
	}
	if strings.Contains(seed, profile.Backstory) {
		t.Fatalf("raw backstory leaked into privileged identity seed: %q", seed)
	}
}

func TestAgentRosterExposesPeersWithoutPrivatePreferences(t *testing.T) {
	bridge := newAgentTestBridge(t)
	first, err := bridge.CreateAgent(CreateAgentInput{Name: "Юри", Age: 21, Gender: "female", Preferences: "private-one"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := bridge.CreateAgent(CreateAgentInput{Name: "Мира", Age: 24, Gender: "nonbinary", Preferences: "private-two"})
	if err != nil {
		t.Fatal(err)
	}
	roster, err := bridge.ListAgents()
	if err != nil || len(roster) != 2 {
		t.Fatalf("agent roster = %#v, err = %v", roster, err)
	}
	activeCount := 0
	for _, view := range roster {
		if view.Active {
			activeCount++
			if view.ID != second.ID {
				t.Fatalf("unexpected active roster entry = %#v", view)
			}
		}
	}
	if activeCount != 1 {
		t.Fatalf("active roster count = %d, roster = %#v", activeCount, roster)
	}
	profiles, err := bridge.repositories.Agents.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seed := agentIdentitySeed(profiles[1], profiles)
	if !strings.Contains(seed, "Юри") || strings.Contains(seed, "private-one") || strings.Contains(seed, "private-two") {
		t.Fatalf("peer registry seed = %q", seed)
	}
	selected, err := bridge.SetActiveAgent(SelectAgentInput{ID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !selected.Active || selected.ID != first.ID || bridge.personaProfileID().String() != first.ID {
		t.Fatalf("selected = %#v, profile = %q, second = %#v", selected, bridge.personaProfileID(), second)
	}
}

func TestAgentIdentitySeedBoundsPeerRoster(t *testing.T) {
	now := time.Now().UTC()
	active, err := domain.NewAgentProfile("agent_active", "Active", 21, "female", "active-private", now)
	if err != nil {
		t.Fatal(err)
	}
	roster := []domain.AgentProfile{active}
	for index := 0; index < maxAgentRosterContextEntries+5; index++ {
		peer, err := domain.NewAgentProfile(
			domain.ID(fmt.Sprintf("agent_peer_%02d", index)),
			fmt.Sprintf("Peer %02d", index),
			20+index%10,
			"unspecified",
			fmt.Sprintf("private-%02d", index),
			now.Add(time.Duration(index+1)*time.Second),
		)
		if err != nil {
			t.Fatal(err)
		}
		roster = append(roster, peer)
	}
	seed := agentIdentitySeed(active, roster)
	if count := strings.Count(seed, "\n- "); count != maxAgentRosterContextEntries {
		t.Fatalf("peer context count = %d, want %d", count, maxAgentRosterContextEntries)
	}
	if !strings.Contains(seed, "Ещё peers вне текущего bounded roster: 5.") || strings.Contains(seed, "private-") {
		t.Fatalf("bounded peer seed = %q", seed)
	}
}
