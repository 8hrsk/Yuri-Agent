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
	first.ProviderID = "openrouter"
	first.Model = "openrouter/free"
	if err := first.Validate(); err != nil {
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
	if got.Name != first.Name || got.Preferences != first.Preferences || got.Backstory != first.Backstory || got.ProviderID != first.ProviderID || got.Model != first.Model || !got.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("Get() = %#v, want %#v", got, first)
	}
	updated, err := repository.UpdateModelRoute(context.Background(), first.ID, "codex", "gpt-5.6", now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if updated.ProviderID != "codex" || updated.Model != "gpt-5.6" || !updated.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("UpdateModelRoute() = %#v", updated)
	}
	updated, err = repository.UpdateExecutionBudget(context.Background(), first.ID, domain.ExecutionBudgetExtended, now.Add(3*time.Second))
	if err != nil || updated.ExecutionBudget != domain.ExecutionBudgetExtended {
		t.Fatalf("UpdateExecutionBudget() = %#v err=%v", updated, err)
	}
	profiles, err := repository.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || profiles[0].ID != first.ID || profiles[1].ID != second.ID {
		t.Fatalf("List() = %#v", profiles)
	}
	if profiles[0].Backstory != first.Backstory || profiles[0].ProviderID != "codex" || profiles[0].Model != "gpt-5.6" || profiles[0].ExecutionBudget != domain.ExecutionBudgetExtended || profiles[1].ExecutionBudget != domain.ExecutionBudgetBalanced || profiles[1].Backstory != "" {
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

func TestCreateAgentWithPersonalizationDefaultsIsAtomic(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	profile, _ := domain.NewAgentProfileWithBackstory("agent-personalized", "Эми", 22, "female", "Мягкая.", "История Эми.", now)
	persona, _ := domain.NewMutablePersona(profile.ID, map[string]float64{"warmth": .7}, "initial", now)
	relationshipDimensions := map[string]float64{"trust": .2}
	affect, _ := domain.NewAffectiveState(profile.ID, map[string]float64{"joy": .1}, "initial", now)
	seed, _ := domain.NewPersonalizationSeed(profile, persona.Traits, relationshipDimensions, now)
	relationship, _ := domain.NewOwnerRelationshipState(seed, now)
	if err := repositories.CreateAgentWithPersonalizationDefaults(ctx, profile, persona, relationship, affect, seed); err != nil {
		t.Fatal(err)
	}
	stored, err := repositories.Personalization.Get(ctx, profile.ID)
	if err != nil || stored.Backstory.Narrative != profile.Backstory {
		t.Fatalf("personalization = %#v, %v", stored, err)
	}

	if _, err := database.ExecContext(ctx, `
		CREATE TRIGGER reject_personalization_for_atomic_test
		BEFORE INSERT ON personalization_seed_versions
		WHEN NEW.agent_id = 'agent-rollback-personalization'
		BEGIN SELECT RAISE(ABORT, 'forced personalization failure'); END`); err != nil {
		t.Fatal(err)
	}
	profile.ID = "agent-rollback-personalization"
	persona.ID, relationship.ID, affect.ID = profile.ID, profile.ID, profile.ID
	failedSeed, _ := domain.NewPersonalizationSeed(profile, persona.Traits, relationship.Dimensions, now)
	if err := repositories.CreateAgentWithPersonalizationDefaults(ctx, profile, persona, relationship, affect, failedSeed); err == nil {
		t.Fatal("forced personalization insert unexpectedly succeeded")
	}
	if _, err := repositories.Agents.Get(ctx, profile.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("partial profile survived personalization failure: %v", err)
	}
	if _, err := repositories.Persona.Get(ctx, profile.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("partial persona survived personalization failure: %v", err)
	}
	if _, err := repositories.Relationship.Get(ctx, profile.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("partial relationship survived personalization failure: %v", err)
	}
	if _, err := repositories.Affect.Get(ctx, profile.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("partial affect survived personalization failure: %v", err)
	}
}
