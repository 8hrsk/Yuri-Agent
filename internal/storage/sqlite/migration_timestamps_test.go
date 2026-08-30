package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rewindMigration removes one applied migration's bookkeeping row so the next
// Migrate call re-applies it against rows seeded in the pre-migration format.
func rewindMigration(t *testing.T, database *sql.DB, version int) {
	t.Helper()
	if _, err := database.Exec("DELETE FROM schema_migrations WHERE version = ?", version); err != nil {
		t.Fatal(err)
	}
}

func countRows(t *testing.T, database *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

// Migration 000013 must re-encode every stored timestamp without changing the
// instant it denotes, without adding or dropping a row, and without touching a
// value it does not recognize.
func TestMigration013NormalizesLegacyTimestamps(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	rewindMigration(t, database, 13)

	seed := []struct{ statement string }{
		{`INSERT INTO agent_profiles(id, name, age, gender, preferences, created_at, updated_at)
		  VALUES ('legacy-owner', 'Legacy', 0, 'female', '', '2026-08-28 11:00:00', '2026-08-28 11:00:00')`},
		{`INSERT INTO conversations(id, title, created_at, updated_at, archived_at, agent_id)
		  VALUES ('conv-1', 'c', '2026-08-28T12:00:00Z', '2026-08-28T12:00:00.5Z', NULL, 'legacy-owner')`},
		// Prefix fractions and a whole second, in chronological order.
		{`INSERT INTO messages(id, conversation_id, role, content, status, provider_meta_json, created_at)
		  VALUES ('m-1', 'conv-1', 'user', 'first', 'complete', '{}', '2026-08-28T12:00:00Z')`},
		{`INSERT INTO messages(id, conversation_id, role, content, status, provider_meta_json, created_at)
		  VALUES ('m-2', 'conv-1', 'user', 'second', 'complete', '{}', '2026-08-28T12:00:00.5Z')`},
		{`INSERT INTO messages(id, conversation_id, role, content, status, provider_meta_json, created_at)
		  VALUES ('m-3', 'conv-1', 'user', 'third', 'complete', '{}', '2026-08-28T12:00:00.55Z')`},
		{`INSERT INTO messages(id, conversation_id, role, content, status, provider_meta_json, created_at)
		  VALUES ('m-4', 'conv-1', 'user', 'fourth', 'complete', '{}', '2026-08-28T12:00:00.123456789Z')`},
		{`INSERT INTO schedules(id, name, kind, expression, timezone, start_at, interval_seconds,
		      payload_json, status, enabled, misfire_policy, next_run_at, last_run_at, version,
		      created_at, updated_at)
		  VALUES ('sched-1', 's', 'interval', '', 'UTC', '2026-08-28T11:00:00+03:00', 3600, '{}',
		      'active', 1, 'run_once', '2026-08-28T12:00:00Z', NULL, 1,
		      '2026-08-28T10:00:00Z', '2026-08-28T10:00:00.000000001Z')`},
		{`INSERT INTO job_runs(id, schedule_id, state, trigger, attempt, execution_key, lease_owner,
		      lease_token, lease_until, scheduled_for, retry_at, started_at, finished_at, error,
		      result_ref, version, created_at, updated_at)
		  VALUES ('job-1', 'sched-1', 'running', 'scheduled', 1, 'sched-1:2026-08-28T12:00:00Z', 'w',
		      'tok', '2026-08-28T12:01:00Z', '2026-08-28T12:00:00Z', NULL, '2026-08-28T12:00:00Z',
		      NULL, '', '', 1, '2026-08-28T12:00:00Z', '2026-08-28T12:00:00Z')`},
		{`INSERT INTO memory_versions(memory_id, version, revision_id, operation, kind, nature,
		      content_text, content_json, summary, confidence, salience, valence, sensitivity,
		      retention_policy, lifecycle_state, pinned, hidden_from_core, canonical_key,
		      embedding_version, access_count, created_at, updated_at, reason, agent_id, scope)
		  VALUES ('mem-1', 1, 'rev-1', 'create', 'fact', 'semantic', 'text', '{}', 's', 1, 1, 0,
		      'normal', 'keep', 'active', 0, 0, '', '', 0,
		      '2026-08-28T09:00:00.987654321Z', '2026-08-28T09:00:00.987654321Z', '', 'legacy-owner',
		      'agent_private')`},
	}
	for _, item := range seed {
		if _, err := database.ExecContext(ctx, item.statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, item.statement)
		}
	}
	// A value the migration must not recognize, and therefore must not touch.
	if _, err := database.ExecContext(ctx,
		`INSERT INTO app_metadata(key, value, updated_at) VALUES ('opaque', 'x', 'not-a-timestamp')`); err != nil {
		t.Fatal(err)
	}

	before := map[string]int{}
	for _, table := range []string{"agent_profiles", "conversations", "messages", "messages_fts", "schedules", "job_runs", "memory_versions", "app_metadata"} {
		before[table] = countRows(t, database, table)
	}

	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	for table, want := range before {
		if got := countRows(t, database, table); got != want {
			t.Errorf("%s row count = %d after migration, want %d", table, got, want)
		}
	}

	type check struct{ query, want string }
	checks := []check{
		{`SELECT created_at FROM agent_profiles WHERE id = 'legacy-owner'`, "2026-08-28T11:00:00.000000000Z"},
		{`SELECT created_at FROM conversations WHERE id = 'conv-1'`, "2026-08-28T12:00:00.000000000Z"},
		{`SELECT updated_at FROM conversations WHERE id = 'conv-1'`, "2026-08-28T12:00:00.500000000Z"},
		{`SELECT created_at FROM messages WHERE id = 'm-1'`, "2026-08-28T12:00:00.000000000Z"},
		{`SELECT created_at FROM messages WHERE id = 'm-2'`, "2026-08-28T12:00:00.500000000Z"},
		{`SELECT created_at FROM messages WHERE id = 'm-3'`, "2026-08-28T12:00:00.550000000Z"},
		{`SELECT created_at FROM messages WHERE id = 'm-4'`, "2026-08-28T12:00:00.123456789Z"},
		// +03:00 becomes the same instant expressed in UTC.
		{`SELECT start_at FROM schedules WHERE id = 'sched-1'`, "2026-08-28T08:00:00.000000000Z"},
		{`SELECT next_run_at FROM schedules WHERE id = 'sched-1'`, "2026-08-28T12:00:00.000000000Z"},
		{`SELECT updated_at FROM schedules WHERE id = 'sched-1'`, "2026-08-28T10:00:00.000000001Z"},
		{`SELECT scheduled_for FROM job_runs WHERE id = 'job-1'`, "2026-08-28T12:00:00.000000000Z"},
		{`SELECT lease_until FROM job_runs WHERE id = 'job-1'`, "2026-08-28T12:01:00.000000000Z"},
		{`SELECT execution_key FROM job_runs WHERE id = 'job-1'`, "sched-1:2026-08-28T12:00:00.000000000Z"},
		{`SELECT created_at FROM memory_versions WHERE memory_id = 'mem-1'`, "2026-08-28T09:00:00.987654321Z"},
		{`SELECT updated_at FROM app_metadata WHERE key = 'opaque'`, "not-a-timestamp"},
	}
	for _, item := range checks {
		var got string
		if err := database.QueryRowContext(ctx, item.query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", item.query, err)
		}
		if got != item.want {
			t.Errorf("%s = %q, want %q", item.query, got, item.want)
		}
	}

	var nullable sql.NullString
	if err := database.QueryRowContext(ctx, `SELECT archived_at FROM conversations WHERE id = 'conv-1'`).Scan(&nullable); err != nil {
		t.Fatal(err)
	}
	if nullable.Valid {
		t.Errorf("archived_at = %q, want NULL preserved", nullable.String)
	}

	// Ordering and range predicates now agree with chronological order.
	rows, err := database.QueryContext(ctx, `SELECT id FROM messages ORDER BY created_at ASC, id ASC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	order := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		order = append(order, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"m-1", "m-4", "m-2", "m-3"}
	for index := range want {
		if index >= len(order) || order[index] != want[index] {
			t.Fatalf("ORDER BY created_at = %v, want %v", order, want)
		}
	}

	// The messages FTS triggers are dropped and recreated around the rewrite, so
	// the migrated schema must end up identical to a fresh install's — same
	// objects, same normalized DDL, nothing dropped and not restored.
	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	if got, want := schemaObjects(t, database), schemaObjects(t, fresh); got != want {
		t.Errorf("migrated schema differs from a fresh install:\n got:\n%s\nwant:\n%s", got, want)
	}
	// That comparison is relative: it cannot see a mistake this migration makes
	// on both paths at once. Pin the three triggers absolutely as well.
	for _, name := range []string{"messages_fts_ai", "messages_fts_ad", "messages_fts_au"} {
		var present int
		if err := database.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, name).Scan(&present); err != nil {
			t.Fatal(err)
		}
		if present != 1 {
			t.Errorf("trigger %s is missing after the migration", name)
		}
	}

	var mismatched int
	if err := database.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM messages AS m
		LEFT JOIN messages_fts AS f ON f.message_id = m.id
		WHERE f.message_id IS NULL OR f.created_at <> m.created_at OR f.content <> m.content`).Scan(&mismatched); err != nil {
		t.Fatal(err)
	}
	if mismatched != 0 {
		t.Errorf("messages_fts disagrees with messages on %d rows", mismatched)
	}
	// The restored triggers still maintain the projection.
	if _, err := database.ExecContext(ctx,
		`INSERT INTO messages(id, conversation_id, role, content, status, provider_meta_json, created_at)
		 VALUES ('m-5', 'conv-1', 'user', 'fifth', 'complete', '{}', '2026-08-28T12:00:01.000000000Z')`); err != nil {
		t.Fatal(err)
	}
	var projected int
	if err := database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages_fts WHERE message_id = 'm-5'`).Scan(&projected); err != nil {
		t.Fatal(err)
	}
	if projected != 1 {
		t.Errorf("messages_fts_ai did not project a new message: %d rows", projected)
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM messages WHERE id = 'm-5'`); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages_fts WHERE message_id = 'm-5'`).Scan(&projected); err != nil {
		t.Fatal(err)
	}
	if projected != 0 {
		t.Errorf("messages_fts_ad did not remove a deleted message: %d rows", projected)
	}

	repository := NewSchedulerRepository(database)
	due := time.Date(2026, 8, 28, 12, 0, 0, 500000000, time.UTC)
	found, err := repository.ListDueSchedules(ctx, due, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("due schedules at %s = %d, want 1", due.Format(time.RFC3339Nano), len(found))
	}

	// The migration is idempotent: a second application changes nothing.
	digest := map[string]string{}
	for _, query := range []string{
		`SELECT group_concat(created_at, '|') FROM messages ORDER BY id`,
		`SELECT group_concat(next_run_at || start_at || created_at || updated_at, '|') FROM schedules`,
		`SELECT group_concat(execution_key, '|') FROM job_runs`,
	} {
		var value sql.NullString
		if err := database.QueryRowContext(ctx, query).Scan(&value); err != nil {
			t.Fatal(err)
		}
		digest[query] = value.String
	}
	rewindMigration(t, database, 13)
	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	for query, want := range digest {
		var value sql.NullString
		if err := database.QueryRowContext(ctx, query).Scan(&value); err != nil {
			t.Fatal(err)
		}
		if value.String != want {
			t.Errorf("re-applying the migration changed %s:\n got %q\nwant %q", query, value.String, want)
		}
	}
}

// schemaObjects renders every object in sqlite_master as SQLite normalized and
// stored it, so two databases can be compared for exact schema equality.
func schemaObjects(t *testing.T, database *sql.DB) string {
	t.Helper()
	rows, err := database.Query(
		`SELECT type, name, COALESCE(sql, '') FROM sqlite_master ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	parts := []string{}
	for rows.Next() {
		var kind, name, statement string
		if err := rows.Scan(&kind, &name, &statement); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, kind+" "+name+"\n"+statement)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(parts, "\n---\n")
}
