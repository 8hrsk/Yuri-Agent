package desktop

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/config"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func newPersonalityTestBridge(t *testing.T) *Bridge {
	t.Helper()
	root := t.TempDir()
	paths := config.Paths{
		ConfigDirectory: filepath.Join(root, "config"), ConfigFile: filepath.Join(root, "config", "config.json"),
		DataDirectory: filepath.Join(root, "data"), DatabaseFile: filepath.Join(root, "data", "yuri.sqlite3"),
		BlobDirectory: filepath.Join(root, "data", "blobs"), LogDirectory: filepath.Join(root, "data", "logs"),
		PluginDirectory: filepath.Join(root, "data", "plugins"), PebbleDirectory: filepath.Join(root, "data", "pebble"),
	}
	value := config.Default(paths)
	database, err := storage.Open(context.Background(), paths.DatabaseFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &Bridge{database: database, repositories: repositories, paths: paths, config: value}
	if err := bridge.ensurePersonaState(context.Background()); err != nil {
		t.Fatal(err)
	}
	return bridge
}

func TestPersonalitySnapshotSeedsSingleOwnerState(t *testing.T) {
	bridge := newPersonalityTestBridge(t)
	snapshot, err := bridge.GetPersonalitySnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ID != "owner" || snapshot.CurrentVersion != 1 || len(snapshot.Traits) < 24 || snapshot.Relationship.Version != 1 || snapshot.Affect.Version != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	for _, trait := range []string{"empathy", "sociability", "shyness", "anxiety", "fearfulness", "emotional_stability", "sensitivity", "possessiveness", "impulsivity", "stubbornness", "optimism", "curiosity", "suspicion"} {
		found := false
		for _, item := range snapshot.Traits {
			if item.ID == trait && item.Min == 0 && item.Max == 1 {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expanded trait %q missing from snapshot", trait)
		}
	}
	if !snapshot.AutoEvolution || len(snapshot.Versions) != 1 || snapshot.CurrentVersionID == "" {
		t.Fatalf("persona controls/history = %#v", snapshot)
	}
}

func TestPersonalityControlsAppendHistoryAndPersistSettings(t *testing.T) {
	bridge := newPersonalityTestBridge(t)
	pinned, err := bridge.SetPersonaTraitPinned(PersonaTraitPinnedInput{TraitID: "warmth", Pinned: true})
	if err != nil {
		t.Fatal(err)
	}
	if pinned.CurrentVersion != 2 || len(pinned.PinnedTraits) != 1 || pinned.PinnedTraits[0] != "warmth" {
		t.Fatalf("pinned snapshot = %#v", pinned)
	}
	disabled, err := bridge.SetPersonaAutoEvolution(PersonaAutoEvolutionInput{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if disabled.AutoEvolution {
		t.Fatal("auto evolution remained enabled")
	}
	loaded, err := config.Load(bridge.paths)
	if err != nil || loaded.Persona.AutoEvolution {
		t.Fatalf("persisted persona config = %#v, %v", loaded.Persona, err)
	}
	rolledBack, err := bridge.RollbackPersona(PersonaVersionInput{VersionID: pinned.Versions[len(pinned.Versions)-1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.CurrentVersion != 3 {
		t.Fatalf("rollback version = %d", rolledBack.CurrentVersion)
	}
	reset, err := bridge.ResetPersona(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if reset.CurrentVersion != 4 || len(reset.Versions) != 4 {
		t.Fatalf("reset snapshot = %#v", reset)
	}
}

func TestAutoEvolutionRollsBackWhenMandatoryAuditFails(t *testing.T) {
	bridge := newPersonalityTestBridge(t)
	if _, err := bridge.database.Exec(`DROP TABLE audit_events`); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.SetPersonaAutoEvolution(PersonaAutoEvolutionInput{Enabled: false}); err == nil {
		t.Fatal("SetPersonaAutoEvolution succeeded without mandatory audit storage")
	}
	loaded, err := config.Load(bridge.paths)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Persona.AutoEvolution || !bridge.config.Persona.AutoEvolution {
		t.Fatalf("unaudited setting was not rolled back: persisted=%t memory=%t", loaded.Persona.AutoEvolution, bridge.config.Persona.AutoEvolution)
	}
}

func TestMutableContextKeepsOpinionsExplicitlySubjective(t *testing.T) {
	bridge := newPersonalityTestBridge(t)
	persona, err := bridge.repositories.Persona.Get(context.Background(), bridge.personaProfileID())
	if err != nil {
		t.Fatal(err)
	}
	relationship, err := bridge.repositories.Relationship.Get(context.Background(), bridge.personaProfileID())
	if err != nil {
		t.Fatal(err)
	}
	affect, err := bridge.repositories.Affect.Get(context.Background(), bridge.personaProfileID())
	if err != nil {
		t.Fatal(err)
	}
	if got := formatMutablePersonaContext(persona); got == "" {
		t.Fatal("mutable persona context is empty")
	}
	if got := formatRelationshipContext(relationship, affect); len(got) == 0 || got[:10] != "Subjective" {
		t.Fatalf("relationship context = %q", got)
	}
}
