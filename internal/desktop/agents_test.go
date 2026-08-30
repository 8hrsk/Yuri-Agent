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
	loaded, err := config.Load(bridge.paths)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Persona.ProfileID != created.ID || !loaded.Onboarding.AgentConfigured {
		t.Fatalf("persisted config = %#v", loaded)
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
