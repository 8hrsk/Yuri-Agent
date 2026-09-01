package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigration026RebuildsLegacyPeerTables exercises the migration against a
// populated v25 schema. Most repository tests start on the newest schema and
// therefore cannot catch a foreign-key target accidentally left pointing at a
// temporary table during the rebuild.
func TestMigration026RebuildsLegacyPeerTables(t *testing.T) {
	ctx := context.Background()
	database, err := sql.Open("sqlite", sqliteFileDSN(filepath.Join(t.TempDir(), "legacy.sqlite3"), false))
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.ExecContext(ctx, `
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
	if len(migrations) < 26 {
		t.Fatalf("migration count = %d, want at least 26", len(migrations))
	}
	for _, migration := range migrations[:25] {
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, migration.SQL); err != nil {
			_ = transaction.Rollback()
			t.Fatalf("apply legacy migration %d: %v", migration.Version, err)
		}
		if _, err := transaction.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, name, checksum) VALUES (?, ?, ?)`,
			migration.Version, migration.Name, migration.Checksum); err != nil {
			_ = transaction.Rollback()
			t.Fatalf("record legacy migration %d: %v", migration.Version, err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatalf("commit legacy migration %d: %v", migration.Version, err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO agent_profiles(id, name, age, gender, preferences, created_at, updated_at)
		 VALUES ('legacy-a', 'A', 20, 'female', '', '2026-08-29T10:00:00.000000000Z', '2026-08-29T10:00:00.000000000Z')`,
		`INSERT INTO agent_profiles(id, name, age, gender, preferences, created_at, updated_at)
		 VALUES ('legacy-b', 'B', 21, 'female', '', '2026-08-29T10:00:00.000000000Z', '2026-08-29T10:00:00.000000000Z')`,
		`INSERT INTO conversations(id, title, created_at, updated_at, archived_at, agent_id)
		 VALUES ('legacy-conversation', 'legacy', '2026-08-29T10:00:00.000000000Z', '2026-08-29T10:00:00.000000000Z', NULL, 'legacy-a')`,
		`INSERT INTO agent_runs(id, kind, conversation_id, parent_run_id, state,
		 max_steps, max_tokens, max_tool_output_bytes, max_duration_seconds, max_tool_calls,
		 failure, version, created_at, updated_at, started_at, finished_at, agent_id)
		 VALUES ('legacy-run-a', 'background', 'legacy-conversation', NULL, 'queued',
		 4, 2000, 4096, 60, 4, '', 1,
		 '2026-08-29T10:00:00.000000000Z', '2026-08-29T10:00:00.000000000Z', NULL, NULL, 'legacy-a')`,
	} {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed legacy data: %v\n%s", err, statement)
		}
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO peer_dialogues(
		 id, initiator_agent_id, peer_agent_id, trigger_run_id, trigger_kind, trigger_reason,
		 pair_key, purpose, status, max_turns, max_tokens, max_duration_seconds, cooldown_seconds,
		 turn_count, tokens_used, idempotency_key, request_hash, failure, failure_kind,
		 failure_retryable, failure_retry_after_seconds, version, created_at, updated_at,
		 started_at, finished_at, expires_at)
		 VALUES ('legacy-dialogue', 'legacy-a', 'legacy-b', 'legacy-run-a', 'agent_tool',
		 'legacy trigger', 'legacy-pair-key', 'legacy exchange', 'cancelled', 4, 4000, 60, 0, 0, 0,
		 'legacy-key', 'sha256:legacy', '', '', 0, 0, 2,
		 '2026-08-29T10:00:01.000000000Z', '2026-08-29T10:00:01.000000000Z', NULL, NULL,
		 '2026-08-29T10:01:01.000000000Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO peer_dialogue_messages(id, dialogue_id, sequence, sender_agent_id,
		 recipient_agent_id, source_run_id, content, created_at)
		VALUES ('legacy-message', 'legacy-dialogue', 0, 'legacy-a', 'legacy-b', 'legacy-run-a',
		 'legacy body', '2026-08-29T10:00:01.000000000Z')`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("apply migration 026: %v", err)
	}
	var minTurns, maxTurns int
	if err := database.QueryRowContext(ctx, `SELECT min_turns, max_turns FROM peer_dialogues WHERE id = 'legacy-dialogue'`).Scan(&minTurns, &maxTurns); err != nil {
		t.Fatal(err)
	}
	if minTurns != maxTurns || maxTurns != 4 {
		t.Fatalf("legacy budget = %d/%d, want 4/4", minTurns, maxTurns)
	}
	var reason string
	if err := database.QueryRowContext(ctx, `SELECT completion_reason FROM peer_dialogues WHERE id = 'legacy-dialogue'`).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Fatalf("legacy completion reason = %q, want empty", reason)
	}
	var content string
	if err := database.QueryRowContext(ctx, `SELECT content FROM peer_dialogue_messages WHERE id = 'legacy-message'`).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if content != "legacy body" {
		t.Fatalf("migrated peer message = %q", content)
	}
	var violations string
	if err := database.QueryRowContext(ctx, `PRAGMA foreign_key_check`).Scan(&violations); err != sql.ErrNoRows {
		t.Fatalf("foreign key check = %q, err=%v", violations, err)
	}
}
