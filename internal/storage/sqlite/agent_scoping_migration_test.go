package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentScopingMigrationBackfillsPrePersonaInstallation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.sqlite3")
	database, err := sql.Open("sqlite", sqliteFileDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
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
		if migration.Version >= 8 {
			break
		}
		if _, err := database.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("apply legacy migration %d: %v", migration.Version, err)
		}
		if _, err := database.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, name, checksum) VALUES (?, ?, ?)",
			migration.Version, migration.Name, migration.Checksum); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := database.ExecContext(ctx, `
		INSERT INTO conversations(id, title, created_at, updated_at)
		VALUES ('legacy-conversation', 'Legacy', ?, ?);
		INSERT INTO agent_runs(
			id, kind, conversation_id, state, max_steps, max_tokens, max_tool_calls,
			max_tool_output_bytes, max_duration_seconds, version, created_at, updated_at
		) VALUES ('legacy-run', 'interactive', 'legacy-conversation', 'created', 1, 1, 1, 1, 1, 1, ?, ?);
		INSERT INTO memory_versions(
			memory_id, version, revision_id, kind, nature, content_text, created_at, updated_at,
			source_run_id, source_conversation_id
		) VALUES ('legacy-memory', 1, 'legacy-revision', 'semantic', 'fact', 'legacy fact', ?, ?, 'legacy-run', 'legacy-conversation')
	`, now, now, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	var profileID, conversationAgent, runAgent, memoryAgent, memoryScope string
	if err := database.QueryRowContext(ctx, "SELECT id FROM agent_profiles ORDER BY id LIMIT 1").Scan(&profileID); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, "SELECT agent_id FROM conversations WHERE id = 'legacy-conversation'").Scan(&conversationAgent); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, "SELECT agent_id FROM agent_runs WHERE id = 'legacy-run'").Scan(&runAgent); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, "SELECT agent_id, scope FROM memory_versions WHERE memory_id = 'legacy-memory'").Scan(&memoryAgent, &memoryScope); err != nil {
		t.Fatal(err)
	}
	if profileID != "owner" || conversationAgent != profileID || runAgent != profileID || memoryAgent != profileID || memoryScope != "agent_private" {
		t.Fatalf("backfill = profile:%q conversation:%q run:%q memory:%q scope:%q", profileID, conversationAgent, runAgent, memoryAgent, memoryScope)
	}
}
