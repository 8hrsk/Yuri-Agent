package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigration015AddsBackstoryWithLegacyDefaultAndBound(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	var migrationCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 15`).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 1 {
		t.Fatalf("migration 015 rows = %d, want 1", migrationCount)
	}

	if _, err := database.ExecContext(ctx, `
		INSERT INTO agent_profiles(id, name, age, gender, preferences, created_at, updated_at)
		VALUES ('legacy-agent', 'Legacy', 0, 'female', '', '2026-08-30T00:00:00.000000000Z', '2026-08-30T00:00:00.000000000Z')`); err != nil {
		t.Fatal(err)
	}
	var backstory string
	if err := database.QueryRowContext(ctx, `SELECT backstory FROM agent_profiles WHERE id = 'legacy-agent'`).Scan(&backstory); err != nil {
		t.Fatal(err)
	}
	if backstory != "" {
		t.Fatalf("legacy backstory = %q, want empty default", backstory)
	}

	valid := strings.Repeat("я", 12000)
	if _, err := database.ExecContext(ctx, `
		INSERT INTO agent_profiles(id, name, age, gender, preferences, backstory, created_at, updated_at)
		VALUES ('bounded-agent', 'Bounded', 0, 'female', '', ?, '2026-08-30T00:00:00.000000000Z', '2026-08-30T00:00:00.000000000Z')`, valid); err != nil {
		t.Fatalf("12000-rune backstory rejected: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO agent_profiles(id, name, age, gender, preferences, backstory, created_at, updated_at)
		VALUES ('oversized-agent', 'Oversized', 0, 'female', '', ?, '2026-08-30T00:00:00.000000000Z', '2026-08-30T00:00:00.000000000Z')`, strings.Repeat("я", 12001)); err == nil {
		t.Fatal("12001-rune backstory unexpectedly accepted by migration constraint")
	}
}
