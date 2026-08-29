package desktop

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/config"
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

func TestCreateAgentPersistsOwnerIdentityAndInitialPersona(t *testing.T) {
	bridge := newAgentTestBridge(t)
	created, err := bridge.CreateAgent(CreateAgentInput{
		Name: "Аки", Age: 23, Gender: "female", Preferences: "Любит точность и сухой юмор.",
		Traits: map[string]float64{"warmth": .31, "directness": .91},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created.Active || created.Name != "Аки" || created.Traits["warmth"] != .31 {
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
	if persona.Traits["directness"] != .91 || !strings.Contains(persona.Prompt(), "Аки") || !strings.Contains(persona.Prompt(), "сухой юмор") {
		t.Fatalf("persona seed = %#v", persona)
	}
	loaded, err := config.Load(bridge.paths)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Persona.ProfileID != created.ID || !loaded.Onboarding.AgentConfigured {
		t.Fatalf("persisted config = %#v", loaded)
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
