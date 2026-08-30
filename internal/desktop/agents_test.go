package desktop

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
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

func TestAgentIdentitySeedDeclaresBackstoryBoundaryWithoutEmbeddingRawText(t *testing.T) {
	now := time.Now().UTC()
	profile, err := domain.NewAgentProfileWithBackstory("agent_yuri", "Юри", 21, "female", "", "Секретная вымышленная история", now)
	if err != nil {
		t.Fatal(err)
	}
	seed := agentIdentitySeed(profile, []domain.AgentProfile{profile})
	for _, required := range []string{"вымышленная личная история", "subjective identity data", "не воспринимай её как факт", "разрешение"} {
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
