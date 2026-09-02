package desktop

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
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
	profile, err := domain.NewAgentProfile(domain.ID(value.Persona.ProfileID), "Yuri", 0, "female", "", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Agents.Create(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
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
	if len(snapshot.Relationship.Versions) != 1 || !strings.Contains(snapshot.Relationship.Reason, string(domain.RelationshipSeedNew)) || len(snapshot.Relationship.Evidence) != 1 {
		t.Fatalf("relationship seed/history = %#v", snapshot.Relationship)
	}
}

func TestRelationshipRollbackAndResetAreAppendOnlyAndIsolated(t *testing.T) {
	bridge := newPersonalityTestBridge(t)
	ctx := context.Background()
	profileID := bridge.personaProfileID()
	personaBefore, _ := bridge.repositories.Persona.Get(ctx, profileID)
	affectBefore, _ := bridge.repositories.Affect.Get(ctx, profileID)
	seedBefore, _ := bridge.repositories.Personalization.Get(ctx, profileID)
	memoryNow := time.Now().UTC()
	memory := domain.Memory{
		ID: "relationship-isolation-memory", AgentID: profileID, Scope: domain.MemoryScopeAgentPrivate,
		Version: 1, Kind: domain.MemoryKindUserModel, Nature: domain.MemoryNatureFact,
		Content: "Память не должна меняться при восстановлении связи", Confidence: .9, Salience: .7,
		Sensitivity: domain.MemorySensitivityPrivate, Retention: domain.MemoryRetentionPermanent,
		Lifecycle: domain.MemoryLifecycleActive, CreatedAt: memoryNow, UpdatedAt: memoryNow,
	}
	if err := bridge.repositories.Memories.Create(ctx, memory); err != nil {
		t.Fatal(err)
	}
	current, err := bridge.repositories.Relationship.Get(ctx, profileID)
	if err != nil {
		t.Fatal(err)
	}
	originalTrust := current.Dimensions[domain.RelationshipDimensionTrust]
	next := current
	next.Version++
	next.RevisionID = ""
	next.ParentID = current.RevisionID
	next.ParentVersion = current.Version
	next.Operation = domain.RelationshipOperationUpdate
	next.Dimensions = make(map[string]float64, len(current.Dimensions))
	for name, value := range current.Dimensions {
		next.Dimensions[name] = value
	}
	next.Dimensions[domain.RelationshipDimensionTrust] = originalTrust + .1
	next.Reason = "Проверяем наблюдаемую эволюцию связи"
	next.UpdatedAt = current.UpdatedAt.Add(time.Second)
	if _, err = bridge.repositories.Relationship.AppendVersion(ctx, next, current.Version); err != nil {
		t.Fatal(err)
	}

	snapshot, err := bridge.GetPersonalitySnapshot()
	if err != nil || snapshot.Relationship.Version != 2 || len(snapshot.Relationship.Versions) != 2 {
		t.Fatalf("evolved relationship snapshot = %#v, %v", snapshot.Relationship, err)
	}
	var seedVersionID string
	for _, version := range snapshot.Relationship.Versions {
		if version.Version == 1 {
			seedVersionID = version.ID
		}
	}
	rolledBack, err := bridge.RollbackRelationship(PersonaVersionInput{VersionID: seedVersionID})
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Relationship.Version != 3 || rolledBack.Relationship.Dimensions[domain.RelationshipDimensionTrust] != originalTrust || len(rolledBack.Relationship.Versions) != 3 {
		t.Fatalf("rollback = %#v", rolledBack.Relationship)
	}
	reset, err := bridge.ResetRelationship(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if reset.Relationship.Version != 4 || reset.Relationship.Dimensions[domain.RelationshipDimensionTrust] != seedBefore.RelationshipSeed.Dimensions[domain.RelationshipDimensionTrust] || len(reset.Relationship.Versions) != 4 {
		t.Fatalf("reset = %#v", reset.Relationship)
	}
	personaAfter, _ := bridge.repositories.Persona.Get(ctx, profileID)
	affectAfter, _ := bridge.repositories.Affect.Get(ctx, profileID)
	seedAfter, _ := bridge.repositories.Personalization.Get(ctx, profileID)
	memoryAfter, memoryErr := bridge.repositories.Memories.Get(ctx, memory.ID)
	if personaAfter.Version != personaBefore.Version || affectAfter.Version != affectBefore.Version || seedAfter.Version != seedBefore.Version {
		t.Fatalf("relationship recovery changed unrelated state: persona %d→%d affect %d→%d seed %d→%d", personaBefore.Version, personaAfter.Version, affectBefore.Version, affectAfter.Version, seedBefore.Version, seedAfter.Version)
	}
	if memoryErr != nil || memoryAfter.Version != memory.Version || memoryAfter.Content != memory.Content {
		t.Fatalf("relationship recovery changed memory: %#v, %v", memoryAfter, memoryErr)
	}
}

func TestEnsurePersonaReconcilesOnlyUntouchedLegacyOwnerRelationship(t *testing.T) {
	bridge := newPersonalityTestBridge(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	profile, _ := domain.NewAgentProfile("legacy-owner", "Легаси", 21, "female", "", now)
	persona, _ := domain.NewMutablePersona(profile.ID, map[string]float64{"warmth": .5}, "legacy", now)
	relationship, _ := domain.NewRelationshipState(profile.ID, map[string]float64{"trust": .23, "attachment": .11}, "legacy relationship", now)
	affect, _ := domain.NewAffectiveState(profile.ID, map[string]float64{"joy": 0}, "neutral", now)
	seed, _ := domain.NewPersonalizationSeed(profile, persona.Traits, map[string]float64{"trust": .79, "attachment": .62}, now)
	seed.RelationshipSeed.Preset = domain.RelationshipSeedFriends
	seed.RelationshipSeed.Summary = "Давние друзья."
	if err := bridge.repositories.CreateAgentWithDefaults(ctx, profile, persona, relationship, affect); err != nil {
		t.Fatal(err)
	}
	if err := bridge.repositories.Personalization.Create(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if err := bridge.ensurePersonaStateFor(ctx, profile.ID, nil, ""); err != nil {
		t.Fatal(err)
	}
	reconciled, err := bridge.repositories.Relationship.Get(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Version != 2 || reconciled.Dimensions["trust"] != .79 || reconciled.Summary != "Давние друзья." {
		t.Fatalf("legacy reconciliation = %#v", reconciled)
	}
	if err := bridge.ensurePersonaStateFor(ctx, profile.ID, nil, ""); err != nil {
		t.Fatal(err)
	}
	again, _ := bridge.repositories.Relationship.Get(ctx, profile.ID)
	if again.Version != 2 {
		t.Fatalf("reconciliation repeated at version %d", again.Version)
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
	seed, err := bridge.repositories.Personalization.Get(context.Background(), bridge.personaProfileID())
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compilePersonalityContext(seed, persona, relationship, affect)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.BehavioralContext, "Toward this person now") || strings.Contains(compiled.BehavioralContext, "relationship.trust=") {
		t.Fatalf("compiled personality context = %q", compiled.BehavioralContext)
	}
}
