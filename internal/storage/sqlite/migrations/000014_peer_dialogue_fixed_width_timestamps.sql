-- Extend the fixed-width timestamp encoding of migration 000013 to the two
-- peer-dialogue tables, which 000013 deliberately skipped.
--
-- Why they were skipped, and why that could not be the end state. 000013
-- rewrote 60 columns across 25 tables and switched every writer in the package
-- to formatTime(). peer_dialogues.go was left out of the writer pass, but its
-- inserts go through the shared timeValue()/nullableTimeValue() helpers, which
-- 000013 *did* convert. The result was not "old encoding, self-consistent": it
-- was a straddle. Inserted rows were already fixed-width while four writers in
-- peer_dialogues.go still formatted with time.RFC3339Nano, and the tables were
-- never normalized. Concretely, HasRecentPair probed
--     created_at >= '2026-08-29T20:00:00Z'
-- against a stored '2026-08-29T20:00:00.000000000Z'. TEXT compares byte by
-- byte and '.' (0x2E) sorts before 'Z' (0x5A), so the stored value compared as
-- *smaller* and a dialogue created at exactly the cooldown boundary — and every
-- dialogue created anywhere in that second — was invisible to the cooldown
-- check. The Go change that lands with this migration routes those four
-- writers through formatTime(); this migration normalizes the rows they and
-- their predecessors already wrote.
--
-- Columns. Enumerated from pragma_table_info on a migrated database rather than
-- by matching column names, because 000013 found three timestamp columns that
-- do not end in _at (affective_states.as_of, job_runs.scheduled_for,
-- job_runs.execution_key). The peer tables have no such column: every other
-- TEXT column here is an id, a status enum, a sha256 digest (pair_key,
-- request_hash), a caller-supplied idempotency key, free text (purpose,
-- failure, content), and none embeds a timestamp the way execution_key does.
-- That leaves exactly six:
--
--   peer_dialogues.created_at    NOT NULL
--   peer_dialogues.updated_at    NOT NULL
--   peer_dialogues.started_at    nullable
--   peer_dialogues.finished_at   nullable
--   peer_dialogues.expires_at    NOT NULL
--   peer_dialogue_messages.created_at  NOT NULL
--
-- Triggers. Seven triggers exist on these two tables and every one was read
-- before the rewrite below:
--
--   peer_dialogues_enforce_cooldown_insert                BEFORE INSERT
--   peer_dialogues_validate_trigger_insert                BEFORE INSERT
--   peer_dialogues_validate_trigger_update                BEFORE UPDATE OF
--       initiator_agent_id, peer_agent_id, trigger_run_id, pair_key
--   peer_dialogue_messages_validate_participants_insert   BEFORE INSERT
--   peer_dialogue_messages_validate_source_insert         BEFORE INSERT
--   peer_dialogue_messages_validate_participants_update   BEFORE UPDATE OF
--       dialogue_id, sequence, sender_agent_id, recipient_agent_id
--   peer_dialogue_messages_validate_source_update         BEFORE UPDATE OF
--       dialogue_id, sender_agent_id, source_run_id
--
-- None of them fires here. The four INSERT triggers are not reached by an
-- UPDATE, and each of the three UPDATE triggers is scoped with UPDATE OF to a
-- column list containing no timestamp, while the statements below set only
-- timestamp columns. There is no AFTER UPDATE trigger, no FTS5 table and no
-- other projection over either table, so neither rewrite is amplified the way
-- the messages rewrite in 000013 was by messages_fts (which turned a linear
-- pass into an O(k*n) one, 16m18s for 100k rows, before that migration learned
-- to drop and rebuild). Both statements here are one scan plus an in-place
-- rewrite of the non-canonical rows.
--
-- No trigger needs changing. peer_dialogues_enforce_cooldown_insert is the
-- only trigger in the schema that compares timestamps, and it does so through
-- julianday(), which is semantic rather than lexicographic. Measured on SQLite
-- 3.53.3, julianday() is exactly encoding-invariant across this change:
--
--   julianday('...T12:00:00Z')  = julianday('...T12:00:00.000000000Z')  -> 1
--   julianday('...T12:00:00.5Z') = julianday('...T12:00:00.500000000Z') -> 1
--
-- so no cooldown decision can change when a column is rewritten. Note that
-- 000013's header overstates this as julianday() parsing "a nine-digit
-- fraction without loss": it does not. julianday() truncates the fraction to
-- milliseconds (julianday('...00.123Z') = julianday('...00.123456789Z')) and
-- the julian-day double itself resolves to roughly 5 microseconds at these
-- dates. That is immaterial here — cooldown_seconds is a whole number of
-- seconds between 0 and 86400, five orders of magnitude above the error — and,
-- because the loss is identical for both encodings, it cannot make this
-- migration change an outcome. The claim is corrected rather than relied on.
--
-- Recognized input shapes are exactly 000013's, in the same order: RFC3339Nano
-- UTC, SQLite CURRENT_TIMESTAMP ('YYYY-MM-DD HH:MM:SS'), and a numeric
-- +HH:MM/-HH:MM offset converted to UTC. Anything else is left byte for byte
-- alone rather than guessed at, NULL stays NULL, and the WHERE guard makes the
-- whole migration idempotent: a row already in the canonical shape is not
-- rewritten, so re-applying changes nothing.
--
-- Cost, measured rather than estimated (see the report accompanying this
-- change): 200000 peer_dialogue_messages plus 20000 peer_dialogues, all in the
-- legacy encoding, on the same loaded machine 000013 was measured on.

UPDATE peer_dialogues
SET created_at = CASE
        WHEN created_at IS NULL THEN NULL
        WHEN created_at LIKE '____-__-__T__:__:__%' AND length(created_at) <= 30 AND substr(created_at, length(created_at), 1) = 'Z'
            THEN substr(created_at, 1, 19) || '.'
                 || substr(replace(replace(substr(created_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN created_at LIKE '____-__-__ __:__:__' AND length(created_at) = 19
            THEN substr(created_at, 1, 10) || 'T' || substr(created_at, 12, 8) || '.000000000Z'
        WHEN created_at LIKE '____-__-__T__:__:__%' AND length(created_at) <= 35
             AND substr(created_at, length(created_at) - 5, 1) IN ('+', '-') AND substr(created_at, length(created_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(created_at, 1, 19) || 'Z',
                     (CASE WHEN substr(created_at, length(created_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(created_at, length(created_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(created_at, length(created_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(created_at, length(created_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(created_at, 20, length(created_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE created_at
    END,
    updated_at = CASE
        WHEN updated_at IS NULL THEN NULL
        WHEN updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) <= 30 AND substr(updated_at, length(updated_at), 1) = 'Z'
            THEN substr(updated_at, 1, 19) || '.'
                 || substr(replace(replace(substr(updated_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN updated_at LIKE '____-__-__ __:__:__' AND length(updated_at) = 19
            THEN substr(updated_at, 1, 10) || 'T' || substr(updated_at, 12, 8) || '.000000000Z'
        WHEN updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) <= 35
             AND substr(updated_at, length(updated_at) - 5, 1) IN ('+', '-') AND substr(updated_at, length(updated_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(updated_at, 1, 19) || 'Z',
                     (CASE WHEN substr(updated_at, length(updated_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(updated_at, length(updated_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(updated_at, length(updated_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(updated_at, length(updated_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(updated_at, 20, length(updated_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE updated_at
    END,
    started_at = CASE
        WHEN started_at IS NULL THEN NULL
        WHEN started_at LIKE '____-__-__T__:__:__%' AND length(started_at) <= 30 AND substr(started_at, length(started_at), 1) = 'Z'
            THEN substr(started_at, 1, 19) || '.'
                 || substr(replace(replace(substr(started_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN started_at LIKE '____-__-__ __:__:__' AND length(started_at) = 19
            THEN substr(started_at, 1, 10) || 'T' || substr(started_at, 12, 8) || '.000000000Z'
        WHEN started_at LIKE '____-__-__T__:__:__%' AND length(started_at) <= 35
             AND substr(started_at, length(started_at) - 5, 1) IN ('+', '-') AND substr(started_at, length(started_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(started_at, 1, 19) || 'Z',
                     (CASE WHEN substr(started_at, length(started_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(started_at, length(started_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(started_at, length(started_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(started_at, length(started_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(started_at, 20, length(started_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE started_at
    END,
    finished_at = CASE
        WHEN finished_at IS NULL THEN NULL
        WHEN finished_at LIKE '____-__-__T__:__:__%' AND length(finished_at) <= 30 AND substr(finished_at, length(finished_at), 1) = 'Z'
            THEN substr(finished_at, 1, 19) || '.'
                 || substr(replace(replace(substr(finished_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN finished_at LIKE '____-__-__ __:__:__' AND length(finished_at) = 19
            THEN substr(finished_at, 1, 10) || 'T' || substr(finished_at, 12, 8) || '.000000000Z'
        WHEN finished_at LIKE '____-__-__T__:__:__%' AND length(finished_at) <= 35
             AND substr(finished_at, length(finished_at) - 5, 1) IN ('+', '-') AND substr(finished_at, length(finished_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(finished_at, 1, 19) || 'Z',
                     (CASE WHEN substr(finished_at, length(finished_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(finished_at, length(finished_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(finished_at, length(finished_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(finished_at, length(finished_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(finished_at, 20, length(finished_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE finished_at
    END,
    expires_at = CASE
        WHEN expires_at IS NULL THEN NULL
        WHEN expires_at LIKE '____-__-__T__:__:__%' AND length(expires_at) <= 30 AND substr(expires_at, length(expires_at), 1) = 'Z'
            THEN substr(expires_at, 1, 19) || '.'
                 || substr(replace(replace(substr(expires_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN expires_at LIKE '____-__-__ __:__:__' AND length(expires_at) = 19
            THEN substr(expires_at, 1, 10) || 'T' || substr(expires_at, 12, 8) || '.000000000Z'
        WHEN expires_at LIKE '____-__-__T__:__:__%' AND length(expires_at) <= 35
             AND substr(expires_at, length(expires_at) - 5, 1) IN ('+', '-') AND substr(expires_at, length(expires_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(expires_at, 1, 19) || 'Z',
                     (CASE WHEN substr(expires_at, length(expires_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(expires_at, length(expires_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(expires_at, length(expires_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(expires_at, length(expires_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(expires_at, 20, length(expires_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE expires_at
    END
WHERE (created_at IS NOT NULL AND NOT (created_at LIKE '____-__-__T__:__:__%' AND length(created_at) = 30 AND substr(created_at, 20, 1) = '.' AND substr(created_at, 30, 1) = 'Z'))
   OR (updated_at IS NOT NULL AND NOT (updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) = 30 AND substr(updated_at, 20, 1) = '.' AND substr(updated_at, 30, 1) = 'Z'))
   OR (started_at IS NOT NULL AND NOT (started_at LIKE '____-__-__T__:__:__%' AND length(started_at) = 30 AND substr(started_at, 20, 1) = '.' AND substr(started_at, 30, 1) = 'Z'))
   OR (finished_at IS NOT NULL AND NOT (finished_at LIKE '____-__-__T__:__:__%' AND length(finished_at) = 30 AND substr(finished_at, 20, 1) = '.' AND substr(finished_at, 30, 1) = 'Z'))
   OR (expires_at IS NOT NULL AND NOT (expires_at LIKE '____-__-__T__:__:__%' AND length(expires_at) = 30 AND substr(expires_at, 20, 1) = '.' AND substr(expires_at, 30, 1) = 'Z'));

UPDATE peer_dialogue_messages
SET created_at = CASE
        WHEN created_at IS NULL THEN NULL
        WHEN created_at LIKE '____-__-__T__:__:__%' AND length(created_at) <= 30 AND substr(created_at, length(created_at), 1) = 'Z'
            THEN substr(created_at, 1, 19) || '.'
                 || substr(replace(replace(substr(created_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN created_at LIKE '____-__-__ __:__:__' AND length(created_at) = 19
            THEN substr(created_at, 1, 10) || 'T' || substr(created_at, 12, 8) || '.000000000Z'
        WHEN created_at LIKE '____-__-__T__:__:__%' AND length(created_at) <= 35
             AND substr(created_at, length(created_at) - 5, 1) IN ('+', '-') AND substr(created_at, length(created_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(created_at, 1, 19) || 'Z',
                     (CASE WHEN substr(created_at, length(created_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(created_at, length(created_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(created_at, length(created_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(created_at, length(created_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(created_at, 20, length(created_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE created_at
    END
WHERE (created_at IS NOT NULL AND NOT (created_at LIKE '____-__-__T__:__:__%' AND length(created_at) = 30 AND substr(created_at, 20, 1) = '.' AND substr(created_at, 30, 1) = 'Z'));

-- Marker row, written in the canonical encoding for the same reason 000013's is.
INSERT OR IGNORE INTO app_metadata(key, value, updated_at)
VALUES ('peer_dialogue_timestamp_encoding', 'fixed-width-nanos-v1',
        strftime('%Y-%m-%dT%H:%M:%S', 'now') || '.000000000Z');
