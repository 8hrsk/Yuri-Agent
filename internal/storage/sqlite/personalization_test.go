package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestPersonalizationRepositoryCreateGetAndAppend(t *testing.T) {
	database, ctx := testDatabase(t)
	agents := NewAgentRepository(database)
	repository := NewPersonalizationRepository(database)
	now := time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC)
	profile, err := domain.NewAgentProfileWithBackstory("agent-v2", "Yuri", 21, "female", "Краткая.", "История.", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := agents.Create(ctx, profile); err != nil {
		t.Fatal(err)
	}
	seed, err := domain.NewPersonalizationSeed(profile, map[string]float64{"warmth": .81}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	seed.Identity.Pronouns = "она/её"
	seed.Identity.UserAddress = "хозяин"
	seed.Identity.Role = "хранительница знаний"
	seed.CommunicationStyle.Figurativeness = .73
	seed.EmotionalDynamics.ConflictStyle = "direct"
	seed.EmotionalDynamics.Triggers = map[string][]string{"joy": {"совместное открытие"}}
	seed.EmotionalDynamics.SoothingStrategies = []string{"спокойный прямой разговор"}
	seed.RelationshipSeed.Summary = "Недавно познакомились в библиотеке."
	seed.Backstory.Summary = "Библиотекарь с тёплыми воспоминаниями."
	seed.Backstory.Episodes = []domain.BackstoryEpisode{{
		ID: "library-day", Title: "Первый день", Content: "Я впервые открыла старый архив.",
		Kind: "formative", People: []string{"наставница"}, Place: "архив", EmotionalValence: .7, Sequence: 1,
	}}
	seed.EvolutionPolicy.LockedFields = append(seed.EvolutionPolicy.LockedFields, "temperament.warmth")
	if err := seed.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := repository.Create(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if err := repository.Create(ctx, seed); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate create error = %v", err)
	}
	stored, err := repository.Get(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, seed) {
		t.Fatalf("stored seed round-trip mismatch\ngot:  %#v\nwant: %#v", stored, seed)
	}
	next := stored
	next.Version++
	next.RevisionID = ""
	next.Operation = domain.PersonalizationOperationUpdate
	next.Reason = "owner changed style"
	next.CommunicationStyle.Humor = .9
	next.UpdatedAt = now.Add(time.Minute)
	next, err = repository.AppendVersion(ctx, next, stored.Version)
	if err != nil {
		t.Fatal(err)
	}
	if next.ParentID != stored.RevisionID || next.ParentVersion != stored.Version {
		t.Fatalf("appended parent = %#v", next)
	}
	head, err := repository.Get(ctx, profile.ID)
	if err != nil || head.Version != 2 || head.CommunicationStyle.Humor != .9 {
		t.Fatalf("head = %#v, %v", head, err)
	}
	first, err := repository.GetVersion(ctx, profile.ID, 1)
	if err != nil || first.CommunicationStyle.Humor == .9 {
		t.Fatalf("immutable first version = %#v, %v", first, err)
	}
}

func TestPersonalizationMigrationBackfillsLegacyOwnerSeed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.sqlite3")
	database, err := sql.Open("sqlite", sqliteFileDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.ExecContext(ctx, `
		PRAGMA foreign_keys = ON;
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version >= 20 {
			break
		}
		if _, err := database.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("apply legacy migration %d: %v", migration.Version, err)
		}
		if _, err := database.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, checksum) VALUES (?, ?, ?)`, migration.Version, migration.Name, migration.Checksum); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	profile, _ := domain.NewAgentProfileWithBackstory("legacy-agent", "Legacy", 0, "female", "Старое описание.", "Старая история.", now)
	if err := NewAgentRepository(database).Create(ctx, profile); err != nil {
		t.Fatal(err)
	}
	persona, _ := domain.NewMutablePersona(profile.ID, map[string]float64{"warmth": .93, "dry_humor": .41}, "legacy", now)
	if err := NewPersonaRepository(database).Create(ctx, persona); err != nil {
		t.Fatal(err)
	}
	relationship, _ := domain.NewRelationshipState(profile.ID, map[string]float64{"trust": .76}, "legacy", now)
	if err := NewRelationshipRepository(database).Create(ctx, relationship); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	seed, err := NewPersonalizationRepository(database).Get(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if seed.Operation != domain.PersonalizationOperationMigration || seed.Temperament.Warmth != .93 || seed.Temperament.Custom["dry_humor"] != .41 {
		t.Fatalf("migrated temperament = %#v", seed)
	}
	if seed.Identity.SelfDescription != profile.Preferences || seed.Backstory.Narrative != profile.Backstory || seed.RelationshipSeed.Dimensions["trust"] != .76 {
		t.Fatalf("migrated owner seed = %#v", seed)
	}
	if len(seed.EvolutionPolicy.TraitBounds) != 25 || seed.EvolutionPolicy.TraitBounds["fearfulness"] != (domain.NumericRange{Min: 0, Max: 1}) {
		t.Fatalf("migrated evolution bounds = %#v", seed.EvolutionPolicy.TraitBounds)
	}
}
