package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestAgentRepositoryCreateGetAndList(t *testing.T) {
	database, err := Open(context.Background(), filepath.Join(t.TempDir(), "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewAgentRepository(database)
	now := time.Now().UTC().Truncate(time.Microsecond)
	first, err := domain.NewAgentProfileWithBackstory("agent_yuri", "Юри", 21, "female", "Любит лаконичность.", "Когда-то я жила среди старых книг.", now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := domain.NewAgentProfileWithBackstory("agent_mira", "Мира", 24, "female", "Предпочитает анализ.", "", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Create(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := repository.Create(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := repository.Create(context.Background(), first); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate error = %v", err)
	}
	got, err := repository.Get(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != first.Name || got.Preferences != first.Preferences || got.Backstory != first.Backstory || !got.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("Get() = %#v, want %#v", got, first)
	}
	profiles, err := repository.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || profiles[0].ID != first.ID || profiles[1].ID != second.ID {
		t.Fatalf("List() = %#v", profiles)
	}
	if profiles[0].Backstory != first.Backstory || profiles[1].Backstory != "" {
		t.Fatalf("List() backstories = %#v", profiles)
	}
}

func TestCreateAgentWithDefaultsRollsBackPartialProfile(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	profile, err := domain.NewAgentProfile("agent_atomic", "Атоми", 22, "female", "Проверяет транзакции.", now)
	if err != nil {
		t.Fatal(err)
	}
	persona, err := domain.NewMutablePersona(profile.ID, map[string]float64{"warmth": .5}, "initial", now)
	if err != nil {
		t.Fatal(err)
	}
	relationship, err := domain.NewRelationshipState(profile.ID, map[string]float64{"trust": .2}, "initial", now)
	if err != nil {
		t.Fatal(err)
	}
	affect, err := domain.NewAffectiveState(profile.ID, map[string]float64{"joy": .1}, "initial", now)
	if err != nil {
		t.Fatal(err)
	}

	// Force the persona journal insert inside the aggregate transaction to
	// conflict after the profile row has already been attempted.
	if err := repositories.Persona.Create(ctx, persona); err != nil {
		t.Fatal(err)
	}
	if err := repositories.CreateAgentWithDefaults(ctx, profile, persona, relationship, affect); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("CreateAgentWithDefaults() error = %v, want ErrConflict", err)
	}
	if _, err := repositories.Agents.Get(ctx, profile.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("partial profile survived rollback: %v", err)
	}
	if _, err := repositories.Relationship.Get(ctx, profile.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("partial relationship survived rollback: %v", err)
	}
	if _, err := repositories.Affect.Get(ctx, profile.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("partial affect survived rollback: %v", err)
	}
}
