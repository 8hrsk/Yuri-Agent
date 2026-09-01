package sqlite

import (
	"errors"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestAgentRepositoryPersistsAndUpdatesExplicitFallbackRoute(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	profile, err := domain.NewAgentProfile("agent-fallback-storage", "Эми", 21, "female", "", now)
	if err != nil {
		t.Fatal(err)
	}
	profile.ProviderID = "codex"
	profile.Model = "gpt-primary"
	profile.FallbackEnabled = true
	profile.FallbackProviderID = "openrouter"
	profile.FallbackModel = "vendor/free"
	if err := profile.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Agents.Create(ctx, profile); err != nil {
		t.Fatal(err)
	}
	stored, err := repositories.Agents.Get(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ProviderID != profile.ProviderID || stored.Model != profile.Model ||
		stored.FallbackEnabled != profile.FallbackEnabled || stored.FallbackProviderID != profile.FallbackProviderID || stored.FallbackModel != profile.FallbackModel {
		t.Fatalf("stored fallback profile = %#v", stored)
	}

	updated, err := repositories.Agents.UpdateFallbackRoute(ctx, profile.ID, false, "openrouter", "vendor/free", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if updated.FallbackEnabled || updated.FallbackProviderID != "openrouter" || updated.FallbackModel != "vendor/free" || updated.ProviderID != "codex" {
		t.Fatalf("disabled fallback update = %#v", updated)
	}
	updated, err = repositories.Agents.UpdateFallbackRoute(ctx, profile.ID, true, "openrouter", "vendor/free", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !updated.FallbackEnabled || updated.FallbackProviderID != "openrouter" || updated.FallbackModel != "vendor/free" {
		t.Fatalf("enabled fallback update = %#v", updated)
	}

	if _, err := repositories.Agents.UpdateFallbackRoute(ctx, profile.ID, true, "openrouter", "", now.Add(3*time.Minute)); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("partial fallback update error = %v, want ErrInvalidArgument", err)
	}
	if _, err := repositories.Agents.UpdateFallbackRoute(ctx, "missing-agent", true, "openrouter", "vendor/free", now.Add(3*time.Minute)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing fallback update error = %v, want ErrNotFound", err)
	}
}

func TestAgentRepositoryLegacyProfileGetsDisabledFallbackDefaults(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 13, 0, 0, 0, time.UTC)
	encoded := formatTime(now)
	if _, err := database.ExecContext(ctx, `
		INSERT INTO agent_profiles(id, name, age, gender, preferences, backstory, provider_id, model, created_at, updated_at)
		VALUES ('legacy-fallback-agent', 'Legacy', 21, 'female', '', '', 'codex', 'gpt-primary', ?, ?)`, encoded, encoded); err != nil {
		t.Fatal(err)
	}
	profile, err := repositories.Agents.Get(ctx, "legacy-fallback-agent")
	if err != nil {
		t.Fatal(err)
	}
	if profile.FallbackEnabled || profile.FallbackProviderID != "" || profile.FallbackModel != "" {
		t.Fatalf("legacy fallback defaults = %#v", profile)
	}
}
