package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// legacyPeerPairKey is the real digest domain.AgentPairKey produces for this
// pair, not a stand-in. It has to be: the scoped reads below hand rows to
// scanPeerDialogue, which runs domain.PeerDialogue.Validate, so a placeholder
// pair key makes the read fail on validation instead of on the property under
// test — which would make the H-16 query-count control fail for the wrong
// reason and prove nothing about the count.
var legacyPeerPairKey = domain.AgentPairKey("legacy-a", "legacy-b")

// peerDialogueTimestampColumns is the enumerated, not pattern-matched, set of
// timestamp columns on the two peer-dialogue tables. It is asserted against
// pragma_table_info below so a column added later cannot silently escape the
// canonical encoding.
var peerDialogueTimestampColumns = map[string][]string{
	"peer_dialogues":         {"created_at", "updated_at", "started_at", "finished_at", "expires_at"},
	"peer_dialogue_messages": {"created_at"},
}

// peerDialogueTriggers is every trigger migration 000014 must leave standing.
// Asserted absolutely rather than by comparing against a fresh database: both
// databases run the same migrations, so a comparison alone cannot see a
// mistake that affects both paths.
var peerDialogueTriggers = []string{
	"peer_dialogues_enforce_cooldown_insert",
	"peer_dialogues_validate_trigger_insert",
	"peer_dialogues_validate_trigger_update",
	"peer_dialogue_messages_validate_participants_insert",
	"peer_dialogue_messages_validate_source_insert",
	"peer_dialogue_messages_validate_participants_update",
	"peer_dialogue_messages_validate_source_update",
}

// seedLegacyPeerRow writes one dialogue and its sequence-zero message directly
// in SQL, bypassing the repository so the row lands in whatever encoding the
// caller asks for. The shape is dictated by the schema's own triggers and
// checks, not chosen freely: peer_dialogue_messages_validate_participants_insert
// requires dialogue.turn_count = NEW.sequence, so a sequence-zero message needs
// turn_count 0, and the "status <> 'completed' OR turn_count > 0" check then
// rules out 'completed'. 'cancelled' is terminal, so several rows can share one
// pair_key without tripping idx_peer_dialogues_active_pair, and cooldown_seconds
// is 0 so peer_dialogues_enforce_cooldown_insert stays quiet between them.
func seedLegacyPeerRow(t *testing.T, ctx context.Context, database *sql.DB, id, createdAt, updatedAt, startedAt, finishedAt, expiresAt, messageCreatedAt string) {
	t.Helper()
	nullable := func(value string) any {
		if value == "" {
			return nil
		}
		return value
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO peer_dialogues(id, initiator_agent_id, peer_agent_id, trigger_run_id, pair_key, purpose,
			status, max_turns, max_tokens, max_duration_seconds, cooldown_seconds, turn_count, tokens_used,
			idempotency_key, request_hash, failure, version, created_at, updated_at, started_at, finished_at, expires_at)
		VALUES (?, 'legacy-a', 'legacy-b', 'legacy-run', ?, 'p', 'cancelled', 3, 4000, 60, 0, 0, 0,
			?, 'sha256:req', '', 2, ?, ?, ?, ?, ?)`,
		id, legacyPeerPairKey, "idem-"+id, createdAt, updatedAt, nullable(startedAt), nullable(finishedAt), expiresAt); err != nil {
		t.Fatalf("seed dialogue %s: %v", id, err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO peer_dialogue_messages(id, dialogue_id, sequence, sender_agent_id, recipient_agent_id,
			source_run_id, content, created_at)
		VALUES (?, ?, 0, 'legacy-a', 'legacy-b', 'legacy-run', 'body', ?)`,
		"msg-"+id, id, messageCreatedAt); err != nil {
		t.Fatalf("seed message %s: %v", id, err)
	}
}

func seedLegacyPeerOwners(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`INSERT INTO agent_profiles(id, name, age, gender, preferences, created_at, updated_at)
		 VALUES ('legacy-a', 'A', 20, 'female', '', '2026-08-28T10:00:00.000000000Z', '2026-08-28T10:00:00.000000000Z')`,
		`INSERT INTO agent_profiles(id, name, age, gender, preferences, created_at, updated_at)
		 VALUES ('legacy-b', 'B', 21, 'female', '', '2026-08-28T10:00:00.000000000Z', '2026-08-28T10:00:00.000000000Z')`,
		`INSERT INTO conversations(id, title, created_at, updated_at, archived_at, agent_id)
		 VALUES ('legacy-conv', 'c', '2026-08-28T10:00:00.000000000Z', '2026-08-28T10:00:00.000000000Z', NULL, 'legacy-a')`,
	} {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed owner: %v\n%s", err, statement)
		}
	}
	// Columns read from pragma_table_info rather than assumed: agent_runs has
	// `state` (not `status`) and a columnar budget (not a budget_json blob).
	// parent_run_id must stay NULL and kind must not be 'subagent', because
	// peer_dialogues_validate_trigger_insert requires an initiator-owned root
	// run before it will admit a dialogue.
	if _, err := database.ExecContext(ctx, `
		INSERT INTO agent_runs(id, kind, conversation_id, parent_run_id, state,
			max_steps, max_tokens, max_tool_output_bytes, max_duration_seconds, max_tool_calls,
			failure, version, created_at, updated_at, started_at, finished_at, agent_id)
		VALUES ('legacy-run', 'background', 'legacy-conv', NULL, 'succeeded',
			4, 2000, 4096, 60, 4,
			'', 1, '2026-08-28T10:00:00.000000000Z', '2026-08-28T10:00:00.000000000Z',
			NULL, NULL, 'legacy-a')`); err != nil {
		t.Fatalf("seed run: %v", err)
	}
}

// Migration 000014 must re-encode the peer-dialogue timestamps without changing
// the instant they denote, without adding or dropping a row, without touching a
// value it does not recognize, and without disturbing any trigger.
func TestMigration014NormalizesPeerDialogueTimestamps(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	rewindMigration(t, database, 14)
	seedLegacyPeerOwners(t, ctx, database)

	// IDs are assigned in REVERSE chronological order so an ORDER BY that
	// happened to fall back on the id tiebreak could not produce the expected
	// sequence by accident.
	//
	// Chronological order:  d-4 (.0) < d-3 (.123456789) < d-2 (.5) < d-1 (.55)
	// Byte order pre-fix:   d-1 (.55Z) < d-2 (.5Z) < d-3 (.123456789Z) < d-4 (Z)
	seedLegacyPeerRow(t, ctx, database, "d-1", "2026-08-28T12:00:00.55Z", "2026-08-28T12:00:00.55Z",
		"", "", "2026-08-28T13:00:00.55Z", "2026-08-28T12:00:00.55Z")
	seedLegacyPeerRow(t, ctx, database, "d-2", "2026-08-28T12:00:00.5Z", "2026-08-28T12:00:00.5Z",
		"2026-08-28T12:00:00.5Z", "2026-08-28T12:00:30Z", "2026-08-28T13:00:00.5Z", "2026-08-28T12:00:00.5Z")
	seedLegacyPeerRow(t, ctx, database, "d-3", "2026-08-28T12:00:00.123456789Z", "2026-08-28 12:00:30",
		"", "", "2026-08-28T13:00:00.123456789Z", "2026-08-28T12:00:00.123456789Z")
	// A numeric offset: the same instant, expressed shifted. Must become UTC.
	seedLegacyPeerRow(t, ctx, database, "d-4", "2026-08-28T12:00:00Z", "2026-08-28T15:00:00+03:00",
		"", "", "2026-08-28T13:00:00Z", "2026-08-28T12:00:00Z")
	// A value the migration must not recognize, and therefore must not touch.
	seedLegacyPeerRow(t, ctx, database, "d-5", "2026-08-28T14:00:00Z", "2026-08-28T14:00:00Z",
		"", "not-a-timestamp", "2026-08-28T15:00:00Z", "2026-08-28T14:00:00Z")

	before := map[string]int{}
	for _, table := range []string{"peer_dialogues", "peer_dialogue_messages"} {
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

	checks := []struct{ query, want string }{
		{`SELECT created_at FROM peer_dialogues WHERE id = 'd-1'`, "2026-08-28T12:00:00.550000000Z"},
		{`SELECT created_at FROM peer_dialogues WHERE id = 'd-2'`, "2026-08-28T12:00:00.500000000Z"},
		{`SELECT started_at FROM peer_dialogues WHERE id = 'd-2'`, "2026-08-28T12:00:00.500000000Z"},
		{`SELECT finished_at FROM peer_dialogues WHERE id = 'd-2'`, "2026-08-28T12:00:30.000000000Z"},
		{`SELECT created_at FROM peer_dialogues WHERE id = 'd-3'`, "2026-08-28T12:00:00.123456789Z"},
		// SQLite CURRENT_TIMESTAMP shape.
		{`SELECT updated_at FROM peer_dialogues WHERE id = 'd-3'`, "2026-08-28T12:00:30.000000000Z"},
		{`SELECT created_at FROM peer_dialogues WHERE id = 'd-4'`, "2026-08-28T12:00:00.000000000Z"},
		// +03:00 becomes the same instant expressed in UTC.
		{`SELECT updated_at FROM peer_dialogues WHERE id = 'd-4'`, "2026-08-28T12:00:00.000000000Z"},
		{`SELECT expires_at FROM peer_dialogues WHERE id = 'd-4'`, "2026-08-28T13:00:00.000000000Z"},
		{`SELECT created_at FROM peer_dialogue_messages WHERE id = 'msg-d-3'`, "2026-08-28T12:00:00.123456789Z"},
		{`SELECT created_at FROM peer_dialogue_messages WHERE id = 'msg-d-4'`, "2026-08-28T12:00:00.000000000Z"},
		// Unrecognized: left byte for byte alone.
		{`SELECT finished_at FROM peer_dialogues WHERE id = 'd-5'`, "not-a-timestamp"},
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

	// NULL stays NULL rather than becoming an empty or defaulted string.
	var nullable sql.NullString
	if err := database.QueryRowContext(ctx, `SELECT started_at FROM peer_dialogues WHERE id = 'd-1'`).Scan(&nullable); err != nil {
		t.Fatal(err)
	}
	if nullable.Valid {
		t.Errorf("started_at = %q, want NULL preserved", nullable.String)
	}

	// The whole point: ORDER BY created_at now agrees with chronological order.
	// d-5 is the late outlier; the interesting group is d-4 < d-3 < d-2 < d-1,
	// which is the exact reverse of both the id order and the pre-migration
	// byte order.
	assertPeerDialogueOrder(t, ctx, database,
		`SELECT id FROM peer_dialogues ORDER BY created_at ASC, id ASC`,
		[]string{"d-4", "d-3", "d-2", "d-1", "d-5"})
	assertPeerDialogueOrder(t, ctx, database,
		`SELECT id FROM peer_dialogue_messages ORDER BY created_at ASC, id ASC`,
		[]string{"msg-d-4", "msg-d-3", "msg-d-2", "msg-d-1", "msg-d-5"})

	// Every timestamp column is now canonical, and the enumerated set is the
	// complete set: pragma_table_info must not reveal a TEXT column holding a
	// timestamp-shaped value that this migration left behind.
	assertPeerTimestampsCanonical(t, ctx, database)

	// Absolute trigger presence, not a comparison against a fresh database.
	for _, name := range peerDialogueTriggers {
		var present int
		if err := database.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, name).Scan(&present); err != nil {
			t.Fatal(err)
		}
		if present != 1 {
			t.Errorf("trigger %s is missing after the migration", name)
		}
	}
	// The cost of this migration is linear only because nothing amplifies the
	// rewrite. Pin that: no AFTER UPDATE trigger and no FTS projection over
	// either table. The messages rewrite in 000013 was O(k*n) — 16m18s for
	// 100k rows — precisely because messages carries AFTER UPDATE FTS triggers
	// and FTS5 does not index an UNINDEXED column, so each updated row cost a
	// full scan of the FTS table. If either check below starts failing, this
	// migration's cost model no longer holds.
	var amplifiers int
	if err := database.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'trigger' AND tbl_name LIKE 'peer_dialogue%'
		  AND upper(COALESCE(sql, '')) LIKE '%AFTER%'
		  AND upper(COALESCE(sql, '')) LIKE '%UPDATE%'`).Scan(&amplifiers); err != nil {
		t.Fatal(err)
	}
	if amplifiers != 0 {
		t.Errorf("peer-dialogue tables carry %d AFTER UPDATE triggers; the migration cost is no longer linear", amplifiers)
	}
	var projections int
	if err := database.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master
		WHERE (type = 'table' AND name LIKE '%peer%' AND upper(COALESCE(sql, '')) LIKE '%USING FTS%')
		   OR (type = 'trigger' AND tbl_name LIKE 'peer_dialogue%' AND upper(COALESCE(sql, '')) LIKE '%FTS%')`).Scan(&projections); err != nil {
		t.Fatal(err)
	}
	if projections != 0 {
		t.Errorf("peer-dialogue tables have %d FTS projections; the migration must rebuild them explicitly", projections)
	}

	// And, separately, the migrated schema still matches a fresh install's.
	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	if got, want := schemaObjects(t, database), schemaObjects(t, fresh); got != want {
		t.Errorf("migrated schema differs from a fresh install:\n got:\n%s\nwant:\n%s", got, want)
	}

	// The cooldown trigger still fires, so the rewrite did not disarm it.
	_, err = database.ExecContext(ctx, `
		INSERT INTO peer_dialogues(id, initiator_agent_id, peer_agent_id, trigger_run_id, pair_key, purpose,
			status, max_turns, max_tokens, max_duration_seconds, cooldown_seconds, turn_count, tokens_used,
			idempotency_key, request_hash, failure, version, created_at, updated_at, started_at, finished_at, expires_at)
		VALUES ('d-6', 'legacy-a', 'legacy-b', 'legacy-run', '`+legacyPeerPairKey+`', 'p', 'queued', 3, 4000, 60, 3600, 0, 0,
			'idem-d-6', 'sha256:req', '', 1, '2026-08-28T14:00:01.000000000Z', '2026-08-28T14:00:01.000000000Z',
			NULL, NULL, '2026-08-28T15:00:00.000000000Z')`)
	if err == nil {
		t.Error("cooldown trigger did not fire on a canonical-encoding insert inside the window")
	}

	// Idempotent: a second application changes nothing.
	digest := map[string]string{}
	digestQueries := []string{
		`SELECT group_concat(id || created_at || updated_at || COALESCE(started_at, '~') || COALESCE(finished_at, '~') || expires_at, '|') FROM (SELECT * FROM peer_dialogues ORDER BY id)`,
		`SELECT group_concat(id || created_at, '|') FROM (SELECT * FROM peer_dialogue_messages ORDER BY id)`,
	}
	for _, query := range digestQueries {
		var value sql.NullString
		if err := database.QueryRowContext(ctx, query).Scan(&value); err != nil {
			t.Fatal(err)
		}
		digest[query] = value.String
	}
	rewindMigration(t, database, 14)
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

func assertPeerDialogueOrder(t *testing.T, ctx context.Context, database *sql.DB, query string, want []string) {
	t.Helper()
	rows, err := database.QueryContext(ctx, query)
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
	if len(order) != len(want) {
		t.Fatalf("%s returned %v, want %v", query, order, want)
	}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("%s = %v, want %v", query, order, want)
		}
	}
}

// assertPeerTimestampsCanonical proves two things at once: that the enumerated
// column set still matches what pragma_table_info reports, and that every value
// in those columns is in the fixed-width encoding.
func assertPeerTimestampsCanonical(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	for table, columns := range peerDialogueTimestampColumns {
		declared := map[string]bool{}
		rows, err := database.QueryContext(ctx,
			`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			declared[name] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
		for _, column := range columns {
			if !declared[column] {
				t.Fatalf("%s.%s is in the enumerated timestamp set but not in the table", table, column)
			}
			var offenders int
			query := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s IS NOT NULL
				AND %s <> 'not-a-timestamp'
				AND NOT (%s LIKE '____-__-__T__:__:__%%' AND length(%s) = 30
				         AND substr(%s, 20, 1) = '.' AND substr(%s, 30, 1) = 'Z')`,
				table, column, column, column, column, column, column)
			if err := database.QueryRowContext(ctx, query).Scan(&offenders); err != nil {
				t.Fatal(err)
			}
			if offenders != 0 {
				t.Errorf("%s.%s has %d values outside the canonical encoding", table, column, offenders)
			}
		}
	}
}

// TestPeerDialogueWritersUseCanonicalEncoding is the writer-side half of M-3:
// every timestamp peer_dialogues.go writes must land in the fixed-width
// encoding, and HasRecentPair must probe in the same one. The straddle this
// replaces was not hypothetical — created_at was already fixed-width (written
// through timeValue) while HasRecentPair probed with RFC3339Nano, so
// "created_at >= since" was false for a dialogue created at exactly since.
func TestPeerDialogueWritersUseCanonicalEncoding(t *testing.T) {
	fixture := newPeerDialogueFixture(t, "agent-a", "agent-b")
	// A whole second: the value RFC3339Nano renders without any fraction, and
	// therefore the one the two encodings disagree about most.
	at := time.Date(2026, 8, 29, 20, 0, 0, 0, time.UTC)
	dialogue, initial := fixture.newDialogue(t, "dlg-1", "agent-a", "agent-b", "trigger-agent-a", "key-1", at)
	if err := fixture.repos.CreatePeerDialogueWithMessage(fixture.ctx, dialogue, initial); err != nil {
		t.Fatal(err)
	}

	// The control this test needs: the two encodings of the same instant must
	// actually differ as strings, otherwise a pass proves nothing.
	if formatTime(at) == at.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("canonical and RFC3339Nano encodings of %s are identical (%q); this test cannot fail", at, formatTime(at))
	}

	// HasRecentPair probes at exactly the stored instant. Under the straddle
	// this was false, because '.' (0x2E) sorts before 'Z' (0x5A) and the stored
	// "…20:00:00.000000000Z" compared as smaller than the probe "…20:00:00Z".
	recent, err := fixture.repos.PeerDialogues.HasRecentPair(fixture.ctx, dialogue.PairKey, at)
	if err != nil {
		t.Fatal(err)
	}
	if !recent {
		t.Error("HasRecentPair missed a dialogue created at exactly the probe instant")
	}
	// And it stays true at every offset inside the second the probe falls in,
	// which is the window the straddle made invisible.
	for _, nanos := range []int{1, 1000, 1000000, 500000000, 999999999} {
		probe := at.Add(-time.Duration(nanos))
		recent, err := fixture.repos.PeerDialogues.HasRecentPair(fixture.ctx, dialogue.PairKey, probe)
		if err != nil {
			t.Fatal(err)
		}
		if !recent {
			t.Errorf("HasRecentPair missed the dialogue when probing %dns before it", nanos)
		}
	}

	// Every column the repository writes, including the two the Save path
	// formats directly, must be canonical on disk.
	running := dialogue
	if err := running.Transition(domain.PeerDialogueRunning, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repos.PeerDialogues.Save(fixture.ctx, running); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repos.PeerDialogues.RecoverInterrupted(fixture.ctx, at.Add(2*time.Second), "shutdown"); err != nil {
		t.Fatal(err)
	}
	assertPeerTimestampsCanonical(t, fixture.ctx, fixture.database)
}

// TestPeerDialogueMessageOrderingSurvivesSubSecondWrites is the ordering half.
// IDs are assigned in reverse so a pass cannot come from the id tiebreak.
func TestPeerDialogueMessageOrderingSurvivesSubSecondWrites(t *testing.T) {
	fixture := newPeerDialogueFixture(t, "agent-a", "agent-b")
	base := time.Date(2026, 8, 29, 20, 0, 0, 0, time.UTC)
	// Chronological: .0 < .5 < .55 < .555 < +1s. IDs descend as time ascends.
	offsets := []time.Duration{0, 500 * time.Millisecond, 550 * time.Millisecond, 555 * time.Millisecond, time.Second}
	want := make([]string, 0, len(offsets))
	for index, offset := range offsets {
		id := fmt.Sprintf("dlg-%d", len(offsets)-index)
		want = append(want, id)
		dialogue, initial := fixture.newDialogue(t, id, "agent-a", "agent-b", "trigger-agent-a",
			"key-"+id, base.Add(offset))
		// A distinct pair per row would be simpler, but the cooldown trigger
		// guards the pair; zero the cooldown instead so the rows coexist.
		dialogue.Budget.CooldownSeconds = 0
		if err := fixture.repos.CreatePeerDialogueWithMessage(fixture.ctx, dialogue, initial); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		if err := closePeerDialogue(fixture, dialogue, base.Add(offset)); err != nil {
			t.Fatalf("close %s: %v", id, err)
		}
	}
	assertPeerDialogueOrder(t, fixture.ctx, fixture.database,
		`SELECT id FROM peer_dialogues ORDER BY created_at ASC, id ASC`, want)

	// The repository's own listing orders newest first, so it must be the
	// exact reverse — again not the id order.
	listed, err := fixture.repos.PeerDialogues.ListByParticipant(fixture.ctx, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != len(want) {
		t.Fatalf("ListByParticipant returned %d dialogues, want %d", len(listed), len(want))
	}
	for index, item := range listed {
		if got, expected := string(item.ID), want[len(want)-1-index]; got != expected {
			t.Fatalf("ListByParticipant[%d] = %q, want %q (full order %v)", index, got, expected, want)
		}
	}
}

// closePeerDialogue moves a dialogue to a terminal state so the active-pair
// unique index permits the next one for the same pair.
func closePeerDialogue(fixture *peerDialogueFixture, dialogue domain.PeerDialogue, at time.Time) error {
	current, err := fixture.repos.PeerDialogues.Get(fixture.ctx, dialogue.ID)
	if err != nil {
		return err
	}
	if err := current.Transition(domain.PeerDialogueCancelled, at); err != nil {
		return err
	}
	return fixture.repos.PeerDialogues.Save(fixture.ctx, current)
}

// TestPeerDialogueScopedReadsAreOneQueryAndStillScope is the H-16 safety
// property: the collapsed reads must issue exactly one statement and must
// still refuse a caller who is not one of the dialogue's two participants.
func TestPeerDialogueScopedReadsAreOneQueryAndStillScope(t *testing.T) {
	database, ctx := countingDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	seedLegacyPeerOwners(t, ctx, database)
	if _, err := database.ExecContext(ctx, `
		INSERT INTO agent_profiles(id, name, age, gender, preferences, created_at, updated_at)
		VALUES ('legacy-c', 'C', 22, 'female', '', '2026-08-28T10:00:00.000000000Z', '2026-08-28T10:00:00.000000000Z')`); err != nil {
		t.Fatal(err)
	}
	seedLegacyPeerRow(t, ctx, database, "d-1",
		"2026-08-28T12:00:00.000000000Z", "2026-08-28T12:00:00.000000000Z", "", "",
		"2026-08-28T13:00:00.000000000Z", "2026-08-28T12:00:00.000000000Z")

	var messages []domain.PeerDialogueMessage
	queries := countQueries(func() {
		messages, err = repositories.PeerDialogueMessages.ListByDialogue(ctx, "d-1", "legacy-a")
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 {
		t.Errorf("ListByDialogue issued %d queries, want 1", queries)
	}
	if len(messages) != 1 || messages[0].ID != "msg-d-1" {
		t.Fatalf("ListByDialogue returned %#v, want the one seeded message", messages)
	}
	// The other participant sees the same content.
	if peerMessages, err := repositories.PeerDialogueMessages.ListByDialogue(ctx, "d-1", "legacy-b"); err != nil || len(peerMessages) != 1 {
		t.Fatalf("peer read = %v, %v; want the same one message", peerMessages, err)
	}

	// Authorization: a non-participant gets nothing, and cannot tell an
	// unauthorized dialogue from a missing one.
	queries = countQueries(func() {
		messages, err = repositories.PeerDialogueMessages.ListByDialogue(ctx, "d-1", "legacy-c")
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("non-participant ListByDialogue = %v, want ErrNotFound", err)
	}
	if len(messages) != 0 {
		t.Fatalf("non-participant ListByDialogue leaked %d messages", len(messages))
	}
	if queries != 1 {
		t.Errorf("non-participant ListByDialogue issued %d queries, want 1", queries)
	}
	if _, missingErr := repositories.PeerDialogueMessages.ListByDialogue(ctx, "no-such-dialogue", "legacy-a"); !errors.Is(missingErr, domain.ErrNotFound) {
		t.Fatalf("missing dialogue = %v, want the same ErrNotFound a non-participant gets", missingErr)
	}

	var single domain.PeerDialogueMessage
	queries = countQueries(func() {
		single, err = repositories.PeerDialogueMessages.GetForParticipant(ctx, "d-1", "legacy-a", "msg-d-1")
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 {
		t.Errorf("GetForParticipant issued %d queries, want 1", queries)
	}
	if single.ID != "msg-d-1" {
		t.Fatalf("GetForParticipant returned %q", single.ID)
	}
	if _, err := repositories.PeerDialogueMessages.GetForParticipant(ctx, "d-1", "legacy-c", "msg-d-1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("non-participant GetForParticipant = %v, want ErrNotFound", err)
	}
	if _, err := repositories.PeerDialogueMessages.GetForParticipant(ctx, "no-such-dialogue", "legacy-a", "msg-d-1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("wrong-dialogue GetForParticipant = %v, want ErrNotFound", err)
	}

	// A participant whose dialogue has lost its messages must still get an
	// empty slice rather than ErrNotFound: the bridge reports that as damaged
	// durable state, and collapsing it into "not found" would hide it.
	if _, err := database.ExecContext(ctx, `DELETE FROM peer_dialogue_messages WHERE dialogue_id = 'd-1'`); err != nil {
		t.Fatal(err)
	}
	empty, err := repositories.PeerDialogueMessages.ListByDialogue(ctx, "d-1", "legacy-a")
	if err != nil {
		t.Fatalf("participant read of a message-less dialogue = %v, want an empty slice", err)
	}
	if len(empty) != 0 {
		t.Fatalf("participant read of a message-less dialogue returned %d messages", len(empty))
	}
}
