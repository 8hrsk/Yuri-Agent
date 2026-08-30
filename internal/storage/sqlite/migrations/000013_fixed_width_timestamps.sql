-- Normalize every stored timestamp to the fixed-width, order-preserving
-- encoding "YYYY-MM-DDTHH:MM:SS.nnnnnnnnnZ" (see sqliteTimeLayout).
--
-- Timestamps are TEXT and SQLite compares TEXT byte by byte, so the encoding
-- has to be order-preserving. time.RFC3339Nano is not: it strips trailing
-- zeros from the fractional part, so a whole second renders as "…T12:00:00Z"
-- and half a second as "…T12:00:00.5Z". Because 'Z' (0x5A) sorts after '.'
-- (0x2E), the earlier instant compares as the larger string; likewise ".5Z"
-- compares after ".55Z". Two consequences were reproduced before this change:
--
--   1. A schedule whose next_run_at falls on a whole second stayed invisible
--      to "next_run_at <= ?" for the entire remainder of that second — the
--      predicate was false for every now in (t, t+1s) and only became true
--      again at exactly t+1s. The same held for the lease-recovery scan on
--      job_runs.lease_until and for job_runs.retry_at.
--   2. ORDER BY created_at reversed any run of "prefix" fractions: the
--      chronological sequence (no fraction, .5, .55, .555) came back in the
--      byte order (.555, .55, .5, none).
--
-- Encoding choice. Fixed-width TEXT rather than INTEGER unix-nanos:
--   * scanTime already parses it with time.RFC3339Nano, so no read path,
--     scanner, or DTO changes.
--   * Every existing index on a timestamp column stays usable AND becomes
--     correctly ordered; an INTEGER column would need all of them rebuilt.
--   * SQLite's own date functions still accept it. That matters: the
--     peer_dialogues cooldown trigger compares julianday(created_at), and
--     PruneAffectEvents compares julianday(created_at). Both parse a
--     nine-digit fraction without loss.
--   * It stays human-readable in ad-hoc queries and in backups.
--
-- Triggers. Every domain invariant expressed as a trigger in this schema was
-- audited for timestamp comparisons. Exactly one compares times —
-- peer_dialogues_enforce_cooldown_insert (000010) — and it does so through
-- julianday(), which is semantic rather than lexicographic and therefore
-- correct under both the old and the new encoding. No trigger needed changing.
-- The audit triggers of 000006 are AFTER INSERT only and the agent-scope
-- triggers of 000008/000009 are BEFORE UPDATE OF <non-timestamp column>, so
-- the rewrites below fire none of them.
--
-- Table categories. Journals (append-only: memory_versions, memory_sources,
-- persona_versions, relationship_versions, affective_states, affective_events,
-- audit_events, messages) are re-encoded, not rewritten: every row keeps the
-- exact same instant, to the nanosecond, and no row is added or removed.
-- Re-encoding a journal entry is not editing history. Projections
-- (memory_heads, persona_heads, relationship_heads, affective_heads,
-- memory_recalls) and mutable state (schedules, job_runs, agent_runs,
-- conversations, tool_calls, approvals, delegations, plugins, plugin_sources,
-- permission_grants, agent_profiles, app_metadata) are normalized the same way.
--
-- Recognized input shapes, in order:
--   * "…THH:MM:SS[.f…]Z"      — RFC3339Nano UTC, the format every writer used.
--   * "YYYY-MM-DD HH:MM:SS"   — SQLite CURRENT_TIMESTAMP, used by the
--                               app_metadata default and the 000008 owner seed.
--                               These already mis-sorted against RFC3339 rows
--                               (' ' < 'T'), so this fixes them too.
--   * "…THH:MM:SS[.f…]±HH:MM" — a numeric offset. Some scheduler writers
--                               formatted without .UTC() first, so a schedule
--                               created from an offset-bearing RFC3339 input
--                               could be stored shifted. Converted to UTC.
-- Anything else is left byte-for-byte alone rather than guessed at.
--
-- Cost, measured rather than estimated. Each statement is one full scan of one
-- table plus an in-place rewrite of the rows that are not already canonical.
-- The WHERE guard is a cheap shape test, and rows written from time.Now()
-- usually already carry nine significant fractional digits, so most journal
-- rows need no write at all; whole-second values (schedules, job_runs, and any
-- hand-seeded fixture) always do. Measured on a seeded database:
--
--   job_runs      20000 rows rewritten (7 columns each)   0.94s
--   job_runs      20000 execution_key rewrites            0.30s
--   schedules       200 rows rewritten                    0.004s
--   whole migration, 200000 messages (20000 rewritten)    9.25s
--
-- The 9.25s figure is dominated by the messages_fts rebuild, which is linear in
-- the number of messages rather than in the number rewritten; extrapolating,
-- expect tens of seconds at 1e6 messages. Before the FTS handling below, the
-- same workload at half that size took 16m18s, because the triggers made the
-- messages rewrite quadratic. All numbers were taken on a heavily loaded
-- machine and are inflated; the shape of the cost, not the absolute figure, is
-- the point.
--
-- The whole migration runs in one transaction and Open() holds
-- SetMaxOpenConns(1), so the application is blocked for its duration.
UPDATE affective_events
SET decays_at = CASE
        WHEN decays_at IS NULL THEN NULL
        WHEN decays_at LIKE '____-__-__T__:__:__%' AND length(decays_at) <= 30 AND substr(decays_at, length(decays_at), 1) = 'Z'
            THEN substr(decays_at, 1, 19) || '.'
                 || substr(replace(replace(substr(decays_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN decays_at LIKE '____-__-__ __:__:__' AND length(decays_at) = 19
            THEN substr(decays_at, 1, 10) || 'T' || substr(decays_at, 12, 8) || '.000000000Z'
        WHEN decays_at LIKE '____-__-__T__:__:__%' AND length(decays_at) <= 35
             AND substr(decays_at, length(decays_at) - 5, 1) IN ('+', '-') AND substr(decays_at, length(decays_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(decays_at, 1, 19) || 'Z',
                     (CASE WHEN substr(decays_at, length(decays_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(decays_at, length(decays_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(decays_at, length(decays_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(decays_at, length(decays_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(decays_at, 20, length(decays_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE decays_at
    END,
    created_at = CASE
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
WHERE (decays_at IS NOT NULL AND NOT (decays_at LIKE '____-__-__T__:__:__%' AND length(decays_at) = 30 AND substr(decays_at, 20, 1) = '.' AND substr(decays_at, 30, 1) = 'Z'))
   OR (created_at IS NOT NULL AND NOT (created_at LIKE '____-__-__T__:__:__%' AND length(created_at) = 30 AND substr(created_at, 20, 1) = '.' AND substr(created_at, 30, 1) = 'Z'));

UPDATE affective_heads
SET updated_at = CASE
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
    END
WHERE (updated_at IS NOT NULL AND NOT (updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) = 30 AND substr(updated_at, 20, 1) = '.' AND substr(updated_at, 30, 1) = 'Z'));

UPDATE affective_states
SET as_of = CASE
        WHEN as_of IS NULL THEN NULL
        WHEN as_of LIKE '____-__-__T__:__:__%' AND length(as_of) <= 30 AND substr(as_of, length(as_of), 1) = 'Z'
            THEN substr(as_of, 1, 19) || '.'
                 || substr(replace(replace(substr(as_of, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN as_of LIKE '____-__-__ __:__:__' AND length(as_of) = 19
            THEN substr(as_of, 1, 10) || 'T' || substr(as_of, 12, 8) || '.000000000Z'
        WHEN as_of LIKE '____-__-__T__:__:__%' AND length(as_of) <= 35
             AND substr(as_of, length(as_of) - 5, 1) IN ('+', '-') AND substr(as_of, length(as_of) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(as_of, 1, 19) || 'Z',
                     (CASE WHEN substr(as_of, length(as_of) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(as_of, length(as_of) - 4, 2) || ' hours',
                     (CASE WHEN substr(as_of, length(as_of) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(as_of, length(as_of) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(as_of, 20, length(as_of) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE as_of
    END,
    created_at = CASE
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
    END
WHERE (as_of IS NOT NULL AND NOT (as_of LIKE '____-__-__T__:__:__%' AND length(as_of) = 30 AND substr(as_of, 20, 1) = '.' AND substr(as_of, 30, 1) = 'Z'))
   OR (created_at IS NOT NULL AND NOT (created_at LIKE '____-__-__T__:__:__%' AND length(created_at) = 30 AND substr(created_at, 20, 1) = '.' AND substr(created_at, 30, 1) = 'Z'))
   OR (updated_at IS NOT NULL AND NOT (updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) = 30 AND substr(updated_at, 20, 1) = '.' AND substr(updated_at, 30, 1) = 'Z'));

UPDATE agent_profiles
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
    END
WHERE (created_at IS NOT NULL AND NOT (created_at LIKE '____-__-__T__:__:__%' AND length(created_at) = 30 AND substr(created_at, 20, 1) = '.' AND substr(created_at, 30, 1) = 'Z'))
   OR (updated_at IS NOT NULL AND NOT (updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) = 30 AND substr(updated_at, 20, 1) = '.' AND substr(updated_at, 30, 1) = 'Z'));

UPDATE agent_runs
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
    END
WHERE (created_at IS NOT NULL AND NOT (created_at LIKE '____-__-__T__:__:__%' AND length(created_at) = 30 AND substr(created_at, 20, 1) = '.' AND substr(created_at, 30, 1) = 'Z'))
   OR (updated_at IS NOT NULL AND NOT (updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) = 30 AND substr(updated_at, 20, 1) = '.' AND substr(updated_at, 30, 1) = 'Z'))
   OR (started_at IS NOT NULL AND NOT (started_at LIKE '____-__-__T__:__:__%' AND length(started_at) = 30 AND substr(started_at, 20, 1) = '.' AND substr(started_at, 30, 1) = 'Z'))
   OR (finished_at IS NOT NULL AND NOT (finished_at LIKE '____-__-__T__:__:__%' AND length(finished_at) = 30 AND substr(finished_at, 20, 1) = '.' AND substr(finished_at, 30, 1) = 'Z'));

UPDATE app_metadata
SET updated_at = CASE
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
    END
WHERE (updated_at IS NOT NULL AND NOT (updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) = 30 AND substr(updated_at, 20, 1) = '.' AND substr(updated_at, 30, 1) = 'Z'));

UPDATE approvals
SET requested_at = CASE
        WHEN requested_at IS NULL THEN NULL
        WHEN requested_at LIKE '____-__-__T__:__:__%' AND length(requested_at) <= 30 AND substr(requested_at, length(requested_at), 1) = 'Z'
            THEN substr(requested_at, 1, 19) || '.'
                 || substr(replace(replace(substr(requested_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN requested_at LIKE '____-__-__ __:__:__' AND length(requested_at) = 19
            THEN substr(requested_at, 1, 10) || 'T' || substr(requested_at, 12, 8) || '.000000000Z'
        WHEN requested_at LIKE '____-__-__T__:__:__%' AND length(requested_at) <= 35
             AND substr(requested_at, length(requested_at) - 5, 1) IN ('+', '-') AND substr(requested_at, length(requested_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(requested_at, 1, 19) || 'Z',
                     (CASE WHEN substr(requested_at, length(requested_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(requested_at, length(requested_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(requested_at, length(requested_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(requested_at, length(requested_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(requested_at, 20, length(requested_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE requested_at
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
    END,
    decided_at = CASE
        WHEN decided_at IS NULL THEN NULL
        WHEN decided_at LIKE '____-__-__T__:__:__%' AND length(decided_at) <= 30 AND substr(decided_at, length(decided_at), 1) = 'Z'
            THEN substr(decided_at, 1, 19) || '.'
                 || substr(replace(replace(substr(decided_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN decided_at LIKE '____-__-__ __:__:__' AND length(decided_at) = 19
            THEN substr(decided_at, 1, 10) || 'T' || substr(decided_at, 12, 8) || '.000000000Z'
        WHEN decided_at LIKE '____-__-__T__:__:__%' AND length(decided_at) <= 35
             AND substr(decided_at, length(decided_at) - 5, 1) IN ('+', '-') AND substr(decided_at, length(decided_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(decided_at, 1, 19) || 'Z',
                     (CASE WHEN substr(decided_at, length(decided_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(decided_at, length(decided_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(decided_at, length(decided_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(decided_at, length(decided_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(decided_at, 20, length(decided_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE decided_at
    END
WHERE (requested_at IS NOT NULL AND NOT (requested_at LIKE '____-__-__T__:__:__%' AND length(requested_at) = 30 AND substr(requested_at, 20, 1) = '.' AND substr(requested_at, 30, 1) = 'Z'))
   OR (expires_at IS NOT NULL AND NOT (expires_at LIKE '____-__-__T__:__:__%' AND length(expires_at) = 30 AND substr(expires_at, 20, 1) = '.' AND substr(expires_at, 30, 1) = 'Z'))
   OR (decided_at IS NOT NULL AND NOT (decided_at LIKE '____-__-__T__:__:__%' AND length(decided_at) = 30 AND substr(decided_at, 20, 1) = '.' AND substr(decided_at, 30, 1) = 'Z'));

UPDATE audit_events
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

UPDATE conversations
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
    archived_at = CASE
        WHEN archived_at IS NULL THEN NULL
        WHEN archived_at LIKE '____-__-__T__:__:__%' AND length(archived_at) <= 30 AND substr(archived_at, length(archived_at), 1) = 'Z'
            THEN substr(archived_at, 1, 19) || '.'
                 || substr(replace(replace(substr(archived_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN archived_at LIKE '____-__-__ __:__:__' AND length(archived_at) = 19
            THEN substr(archived_at, 1, 10) || 'T' || substr(archived_at, 12, 8) || '.000000000Z'
        WHEN archived_at LIKE '____-__-__T__:__:__%' AND length(archived_at) <= 35
             AND substr(archived_at, length(archived_at) - 5, 1) IN ('+', '-') AND substr(archived_at, length(archived_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(archived_at, 1, 19) || 'Z',
                     (CASE WHEN substr(archived_at, length(archived_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(archived_at, length(archived_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(archived_at, length(archived_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(archived_at, length(archived_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(archived_at, 20, length(archived_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE archived_at
    END
WHERE (created_at IS NOT NULL AND NOT (created_at LIKE '____-__-__T__:__:__%' AND length(created_at) = 30 AND substr(created_at, 20, 1) = '.' AND substr(created_at, 30, 1) = 'Z'))
   OR (updated_at IS NOT NULL AND NOT (updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) = 30 AND substr(updated_at, 20, 1) = '.' AND substr(updated_at, 30, 1) = 'Z'))
   OR (archived_at IS NOT NULL AND NOT (archived_at LIKE '____-__-__T__:__:__%' AND length(archived_at) = 30 AND substr(archived_at, 20, 1) = '.' AND substr(archived_at, 30, 1) = 'Z'));

UPDATE delegations
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
    END
WHERE (created_at IS NOT NULL AND NOT (created_at LIKE '____-__-__T__:__:__%' AND length(created_at) = 30 AND substr(created_at, 20, 1) = '.' AND substr(created_at, 30, 1) = 'Z'))
   OR (updated_at IS NOT NULL AND NOT (updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) = 30 AND substr(updated_at, 20, 1) = '.' AND substr(updated_at, 30, 1) = 'Z'))
   OR (started_at IS NOT NULL AND NOT (started_at LIKE '____-__-__T__:__:__%' AND length(started_at) = 30 AND substr(started_at, 20, 1) = '.' AND substr(started_at, 30, 1) = 'Z'))
   OR (finished_at IS NOT NULL AND NOT (finished_at LIKE '____-__-__T__:__:__%' AND length(finished_at) = 30 AND substr(finished_at, 20, 1) = '.' AND substr(finished_at, 30, 1) = 'Z'));

UPDATE job_runs
SET lease_until = CASE
        WHEN lease_until IS NULL THEN NULL
        WHEN lease_until LIKE '____-__-__T__:__:__%' AND length(lease_until) <= 30 AND substr(lease_until, length(lease_until), 1) = 'Z'
            THEN substr(lease_until, 1, 19) || '.'
                 || substr(replace(replace(substr(lease_until, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN lease_until LIKE '____-__-__ __:__:__' AND length(lease_until) = 19
            THEN substr(lease_until, 1, 10) || 'T' || substr(lease_until, 12, 8) || '.000000000Z'
        WHEN lease_until LIKE '____-__-__T__:__:__%' AND length(lease_until) <= 35
             AND substr(lease_until, length(lease_until) - 5, 1) IN ('+', '-') AND substr(lease_until, length(lease_until) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(lease_until, 1, 19) || 'Z',
                     (CASE WHEN substr(lease_until, length(lease_until) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(lease_until, length(lease_until) - 4, 2) || ' hours',
                     (CASE WHEN substr(lease_until, length(lease_until) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(lease_until, length(lease_until) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(lease_until, 20, length(lease_until) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE lease_until
    END,
    scheduled_for = CASE
        WHEN scheduled_for IS NULL THEN NULL
        WHEN scheduled_for LIKE '____-__-__T__:__:__%' AND length(scheduled_for) <= 30 AND substr(scheduled_for, length(scheduled_for), 1) = 'Z'
            THEN substr(scheduled_for, 1, 19) || '.'
                 || substr(replace(replace(substr(scheduled_for, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN scheduled_for LIKE '____-__-__ __:__:__' AND length(scheduled_for) = 19
            THEN substr(scheduled_for, 1, 10) || 'T' || substr(scheduled_for, 12, 8) || '.000000000Z'
        WHEN scheduled_for LIKE '____-__-__T__:__:__%' AND length(scheduled_for) <= 35
             AND substr(scheduled_for, length(scheduled_for) - 5, 1) IN ('+', '-') AND substr(scheduled_for, length(scheduled_for) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(scheduled_for, 1, 19) || 'Z',
                     (CASE WHEN substr(scheduled_for, length(scheduled_for) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(scheduled_for, length(scheduled_for) - 4, 2) || ' hours',
                     (CASE WHEN substr(scheduled_for, length(scheduled_for) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(scheduled_for, length(scheduled_for) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(scheduled_for, 20, length(scheduled_for) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE scheduled_for
    END,
    retry_at = CASE
        WHEN retry_at IS NULL THEN NULL
        WHEN retry_at LIKE '____-__-__T__:__:__%' AND length(retry_at) <= 30 AND substr(retry_at, length(retry_at), 1) = 'Z'
            THEN substr(retry_at, 1, 19) || '.'
                 || substr(replace(replace(substr(retry_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN retry_at LIKE '____-__-__ __:__:__' AND length(retry_at) = 19
            THEN substr(retry_at, 1, 10) || 'T' || substr(retry_at, 12, 8) || '.000000000Z'
        WHEN retry_at LIKE '____-__-__T__:__:__%' AND length(retry_at) <= 35
             AND substr(retry_at, length(retry_at) - 5, 1) IN ('+', '-') AND substr(retry_at, length(retry_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(retry_at, 1, 19) || 'Z',
                     (CASE WHEN substr(retry_at, length(retry_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(retry_at, length(retry_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(retry_at, length(retry_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(retry_at, length(retry_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(retry_at, 20, length(retry_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE retry_at
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
    created_at = CASE
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
    END
WHERE (lease_until IS NOT NULL AND NOT (lease_until LIKE '____-__-__T__:__:__%' AND length(lease_until) = 30 AND substr(lease_until, 20, 1) = '.' AND substr(lease_until, 30, 1) = 'Z'))
   OR (scheduled_for IS NOT NULL AND NOT (scheduled_for LIKE '____-__-__T__:__:__%' AND length(scheduled_for) = 30 AND substr(scheduled_for, 20, 1) = '.' AND substr(scheduled_for, 30, 1) = 'Z'))
   OR (retry_at IS NOT NULL AND NOT (retry_at LIKE '____-__-__T__:__:__%' AND length(retry_at) = 30 AND substr(retry_at, 20, 1) = '.' AND substr(retry_at, 30, 1) = 'Z'))
   OR (started_at IS NOT NULL AND NOT (started_at LIKE '____-__-__T__:__:__%' AND length(started_at) = 30 AND substr(started_at, 20, 1) = '.' AND substr(started_at, 30, 1) = 'Z'))
   OR (finished_at IS NOT NULL AND NOT (finished_at LIKE '____-__-__T__:__:__%' AND length(finished_at) = 30 AND substr(finished_at, 20, 1) = '.' AND substr(finished_at, 30, 1) = 'Z'))
   OR (created_at IS NOT NULL AND NOT (created_at LIKE '____-__-__T__:__:__%' AND length(created_at) = 30 AND substr(created_at, 20, 1) = '.' AND substr(created_at, 30, 1) = 'Z'))
   OR (updated_at IS NOT NULL AND NOT (updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) = 30 AND substr(updated_at, 20, 1) = '.' AND substr(updated_at, 30, 1) = 'Z'));

UPDATE memory_heads
SET updated_at = CASE
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
    END
WHERE (updated_at IS NOT NULL AND NOT (updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) = 30 AND substr(updated_at, 20, 1) = '.' AND substr(updated_at, 30, 1) = 'Z'));

UPDATE memory_recalls
SET last_accessed_at = CASE
        WHEN last_accessed_at IS NULL THEN NULL
        WHEN last_accessed_at LIKE '____-__-__T__:__:__%' AND length(last_accessed_at) <= 30 AND substr(last_accessed_at, length(last_accessed_at), 1) = 'Z'
            THEN substr(last_accessed_at, 1, 19) || '.'
                 || substr(replace(replace(substr(last_accessed_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN last_accessed_at LIKE '____-__-__ __:__:__' AND length(last_accessed_at) = 19
            THEN substr(last_accessed_at, 1, 10) || 'T' || substr(last_accessed_at, 12, 8) || '.000000000Z'
        WHEN last_accessed_at LIKE '____-__-__T__:__:__%' AND length(last_accessed_at) <= 35
             AND substr(last_accessed_at, length(last_accessed_at) - 5, 1) IN ('+', '-') AND substr(last_accessed_at, length(last_accessed_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(last_accessed_at, 1, 19) || 'Z',
                     (CASE WHEN substr(last_accessed_at, length(last_accessed_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(last_accessed_at, length(last_accessed_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(last_accessed_at, length(last_accessed_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(last_accessed_at, length(last_accessed_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(last_accessed_at, 20, length(last_accessed_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE last_accessed_at
    END,
    last_recalled_at = CASE
        WHEN last_recalled_at IS NULL THEN NULL
        WHEN last_recalled_at LIKE '____-__-__T__:__:__%' AND length(last_recalled_at) <= 30 AND substr(last_recalled_at, length(last_recalled_at), 1) = 'Z'
            THEN substr(last_recalled_at, 1, 19) || '.'
                 || substr(replace(replace(substr(last_recalled_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN last_recalled_at LIKE '____-__-__ __:__:__' AND length(last_recalled_at) = 19
            THEN substr(last_recalled_at, 1, 10) || 'T' || substr(last_recalled_at, 12, 8) || '.000000000Z'
        WHEN last_recalled_at LIKE '____-__-__T__:__:__%' AND length(last_recalled_at) <= 35
             AND substr(last_recalled_at, length(last_recalled_at) - 5, 1) IN ('+', '-') AND substr(last_recalled_at, length(last_recalled_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(last_recalled_at, 1, 19) || 'Z',
                     (CASE WHEN substr(last_recalled_at, length(last_recalled_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(last_recalled_at, length(last_recalled_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(last_recalled_at, length(last_recalled_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(last_recalled_at, length(last_recalled_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(last_recalled_at, 20, length(last_recalled_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE last_recalled_at
    END
WHERE (last_accessed_at IS NOT NULL AND NOT (last_accessed_at LIKE '____-__-__T__:__:__%' AND length(last_accessed_at) = 30 AND substr(last_accessed_at, 20, 1) = '.' AND substr(last_accessed_at, 30, 1) = 'Z'))
   OR (last_recalled_at IS NOT NULL AND NOT (last_recalled_at LIKE '____-__-__T__:__:__%' AND length(last_recalled_at) = 30 AND substr(last_recalled_at, 20, 1) = '.' AND substr(last_recalled_at, 30, 1) = 'Z'));

UPDATE memory_sources
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

UPDATE memory_versions
SET last_accessed_at = CASE
        WHEN last_accessed_at IS NULL THEN NULL
        WHEN last_accessed_at LIKE '____-__-__T__:__:__%' AND length(last_accessed_at) <= 30 AND substr(last_accessed_at, length(last_accessed_at), 1) = 'Z'
            THEN substr(last_accessed_at, 1, 19) || '.'
                 || substr(replace(replace(substr(last_accessed_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN last_accessed_at LIKE '____-__-__ __:__:__' AND length(last_accessed_at) = 19
            THEN substr(last_accessed_at, 1, 10) || 'T' || substr(last_accessed_at, 12, 8) || '.000000000Z'
        WHEN last_accessed_at LIKE '____-__-__T__:__:__%' AND length(last_accessed_at) <= 35
             AND substr(last_accessed_at, length(last_accessed_at) - 5, 1) IN ('+', '-') AND substr(last_accessed_at, length(last_accessed_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(last_accessed_at, 1, 19) || 'Z',
                     (CASE WHEN substr(last_accessed_at, length(last_accessed_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(last_accessed_at, length(last_accessed_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(last_accessed_at, length(last_accessed_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(last_accessed_at, length(last_accessed_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(last_accessed_at, 20, length(last_accessed_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE last_accessed_at
    END,
    last_recalled_at = CASE
        WHEN last_recalled_at IS NULL THEN NULL
        WHEN last_recalled_at LIKE '____-__-__T__:__:__%' AND length(last_recalled_at) <= 30 AND substr(last_recalled_at, length(last_recalled_at), 1) = 'Z'
            THEN substr(last_recalled_at, 1, 19) || '.'
                 || substr(replace(replace(substr(last_recalled_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN last_recalled_at LIKE '____-__-__ __:__:__' AND length(last_recalled_at) = 19
            THEN substr(last_recalled_at, 1, 10) || 'T' || substr(last_recalled_at, 12, 8) || '.000000000Z'
        WHEN last_recalled_at LIKE '____-__-__T__:__:__%' AND length(last_recalled_at) <= 35
             AND substr(last_recalled_at, length(last_recalled_at) - 5, 1) IN ('+', '-') AND substr(last_recalled_at, length(last_recalled_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(last_recalled_at, 1, 19) || 'Z',
                     (CASE WHEN substr(last_recalled_at, length(last_recalled_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(last_recalled_at, length(last_recalled_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(last_recalled_at, length(last_recalled_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(last_recalled_at, length(last_recalled_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(last_recalled_at, 20, length(last_recalled_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE last_recalled_at
    END,
    created_at = CASE
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
    dormant_at = CASE
        WHEN dormant_at IS NULL THEN NULL
        WHEN dormant_at LIKE '____-__-__T__:__:__%' AND length(dormant_at) <= 30 AND substr(dormant_at, length(dormant_at), 1) = 'Z'
            THEN substr(dormant_at, 1, 19) || '.'
                 || substr(replace(replace(substr(dormant_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN dormant_at LIKE '____-__-__ __:__:__' AND length(dormant_at) = 19
            THEN substr(dormant_at, 1, 10) || 'T' || substr(dormant_at, 12, 8) || '.000000000Z'
        WHEN dormant_at LIKE '____-__-__T__:__:__%' AND length(dormant_at) <= 35
             AND substr(dormant_at, length(dormant_at) - 5, 1) IN ('+', '-') AND substr(dormant_at, length(dormant_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(dormant_at, 1, 19) || 'Z',
                     (CASE WHEN substr(dormant_at, length(dormant_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(dormant_at, length(dormant_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(dormant_at, length(dormant_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(dormant_at, length(dormant_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(dormant_at, 20, length(dormant_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE dormant_at
    END,
    deleted_at = CASE
        WHEN deleted_at IS NULL THEN NULL
        WHEN deleted_at LIKE '____-__-__T__:__:__%' AND length(deleted_at) <= 30 AND substr(deleted_at, length(deleted_at), 1) = 'Z'
            THEN substr(deleted_at, 1, 19) || '.'
                 || substr(replace(replace(substr(deleted_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN deleted_at LIKE '____-__-__ __:__:__' AND length(deleted_at) = 19
            THEN substr(deleted_at, 1, 10) || 'T' || substr(deleted_at, 12, 8) || '.000000000Z'
        WHEN deleted_at LIKE '____-__-__T__:__:__%' AND length(deleted_at) <= 35
             AND substr(deleted_at, length(deleted_at) - 5, 1) IN ('+', '-') AND substr(deleted_at, length(deleted_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(deleted_at, 1, 19) || 'Z',
                     (CASE WHEN substr(deleted_at, length(deleted_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(deleted_at, length(deleted_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(deleted_at, length(deleted_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(deleted_at, length(deleted_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(deleted_at, 20, length(deleted_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE deleted_at
    END
WHERE (last_accessed_at IS NOT NULL AND NOT (last_accessed_at LIKE '____-__-__T__:__:__%' AND length(last_accessed_at) = 30 AND substr(last_accessed_at, 20, 1) = '.' AND substr(last_accessed_at, 30, 1) = 'Z'))
   OR (last_recalled_at IS NOT NULL AND NOT (last_recalled_at LIKE '____-__-__T__:__:__%' AND length(last_recalled_at) = 30 AND substr(last_recalled_at, 20, 1) = '.' AND substr(last_recalled_at, 30, 1) = 'Z'))
   OR (created_at IS NOT NULL AND NOT (created_at LIKE '____-__-__T__:__:__%' AND length(created_at) = 30 AND substr(created_at, 20, 1) = '.' AND substr(created_at, 30, 1) = 'Z'))
   OR (updated_at IS NOT NULL AND NOT (updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) = 30 AND substr(updated_at, 20, 1) = '.' AND substr(updated_at, 30, 1) = 'Z'))
   OR (dormant_at IS NOT NULL AND NOT (dormant_at LIKE '____-__-__T__:__:__%' AND length(dormant_at) = 30 AND substr(dormant_at, 20, 1) = '.' AND substr(dormant_at, 30, 1) = 'Z'))
   OR (deleted_at IS NOT NULL AND NOT (deleted_at LIKE '____-__-__T__:__:__%' AND length(deleted_at) = 30 AND substr(deleted_at, 20, 1) = '.' AND substr(deleted_at, 30, 1) = 'Z'));

-- messages carries AFTER UPDATE triggers that reproject each touched row into
-- messages_fts. FTS5 does not index an UNINDEXED column, so the trigger's
-- "DELETE FROM messages_fts WHERE message_id = old.id" scans the whole index
-- once per updated row: rewriting k of n messages costs O(k*n). Measured at
-- 50k messages, rewriting 5k of them through the triggers took 3m49s, against
-- 0.94s for a 20k-row rewrite of the trigger-free job_runs.
--
-- Drop the triggers, rewrite the column, rebuild the projection in one linear
-- pass, and restore the triggers byte-for-byte as 000003 defines them. This is
-- the same rebuild RebuildProjections performs, so messages_fts.created_at ends
-- up consistent with messages instead of merely being left alone.
DROP TRIGGER IF EXISTS messages_fts_ai;
DROP TRIGGER IF EXISTS messages_fts_ad;
DROP TRIGGER IF EXISTS messages_fts_au;

UPDATE messages
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

DELETE FROM messages_fts;
INSERT INTO messages_fts(message_id, conversation_id, role, content, created_at)
SELECT m.id, m.conversation_id, m.role, m.content, m.created_at
FROM messages AS m;

CREATE TRIGGER IF NOT EXISTS messages_fts_ai AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(message_id, conversation_id, role, content, created_at)
    VALUES (new.id, new.conversation_id, new.role, new.content, new.created_at);
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_ad AFTER DELETE ON messages BEGIN
    DELETE FROM messages_fts WHERE message_id = old.id;
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_au AFTER UPDATE ON messages BEGIN
    DELETE FROM messages_fts WHERE message_id = old.id;
    INSERT INTO messages_fts(message_id, conversation_id, role, content, created_at)
    VALUES (new.id, new.conversation_id, new.role, new.content, new.created_at);
END;

UPDATE permission_grants
SET granted_at = CASE
        WHEN granted_at IS NULL THEN NULL
        WHEN granted_at LIKE '____-__-__T__:__:__%' AND length(granted_at) <= 30 AND substr(granted_at, length(granted_at), 1) = 'Z'
            THEN substr(granted_at, 1, 19) || '.'
                 || substr(replace(replace(substr(granted_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN granted_at LIKE '____-__-__ __:__:__' AND length(granted_at) = 19
            THEN substr(granted_at, 1, 10) || 'T' || substr(granted_at, 12, 8) || '.000000000Z'
        WHEN granted_at LIKE '____-__-__T__:__:__%' AND length(granted_at) <= 35
             AND substr(granted_at, length(granted_at) - 5, 1) IN ('+', '-') AND substr(granted_at, length(granted_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(granted_at, 1, 19) || 'Z',
                     (CASE WHEN substr(granted_at, length(granted_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(granted_at, length(granted_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(granted_at, length(granted_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(granted_at, length(granted_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(granted_at, 20, length(granted_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE granted_at
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
WHERE (granted_at IS NOT NULL AND NOT (granted_at LIKE '____-__-__T__:__:__%' AND length(granted_at) = 30 AND substr(granted_at, 20, 1) = '.' AND substr(granted_at, 30, 1) = 'Z'))
   OR (expires_at IS NOT NULL AND NOT (expires_at LIKE '____-__-__T__:__:__%' AND length(expires_at) = 30 AND substr(expires_at, 20, 1) = '.' AND substr(expires_at, 30, 1) = 'Z'));

UPDATE persona_heads
SET updated_at = CASE
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
    END
WHERE (updated_at IS NOT NULL AND NOT (updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) = 30 AND substr(updated_at, 20, 1) = '.' AND substr(updated_at, 30, 1) = 'Z'));

UPDATE persona_versions
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
    END
WHERE (created_at IS NOT NULL AND NOT (created_at LIKE '____-__-__T__:__:__%' AND length(created_at) = 30 AND substr(created_at, 20, 1) = '.' AND substr(created_at, 30, 1) = 'Z'))
   OR (updated_at IS NOT NULL AND NOT (updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) = 30 AND substr(updated_at, 20, 1) = '.' AND substr(updated_at, 30, 1) = 'Z'));

UPDATE plugin_sources
SET checked_at = CASE
        WHEN checked_at IS NULL THEN NULL
        WHEN checked_at LIKE '____-__-__T__:__:__%' AND length(checked_at) <= 30 AND substr(checked_at, length(checked_at), 1) = 'Z'
            THEN substr(checked_at, 1, 19) || '.'
                 || substr(replace(replace(substr(checked_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN checked_at LIKE '____-__-__ __:__:__' AND length(checked_at) = 19
            THEN substr(checked_at, 1, 10) || 'T' || substr(checked_at, 12, 8) || '.000000000Z'
        WHEN checked_at LIKE '____-__-__T__:__:__%' AND length(checked_at) <= 35
             AND substr(checked_at, length(checked_at) - 5, 1) IN ('+', '-') AND substr(checked_at, length(checked_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(checked_at, 1, 19) || 'Z',
                     (CASE WHEN substr(checked_at, length(checked_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(checked_at, length(checked_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(checked_at, length(checked_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(checked_at, length(checked_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(checked_at, 20, length(checked_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE checked_at
    END
WHERE (checked_at IS NOT NULL AND NOT (checked_at LIKE '____-__-__T__:__:__%' AND length(checked_at) = 30 AND substr(checked_at, 20, 1) = '.' AND substr(checked_at, 30, 1) = 'Z'));

UPDATE plugins
SET installed_at = CASE
        WHEN installed_at IS NULL THEN NULL
        WHEN installed_at LIKE '____-__-__T__:__:__%' AND length(installed_at) <= 30 AND substr(installed_at, length(installed_at), 1) = 'Z'
            THEN substr(installed_at, 1, 19) || '.'
                 || substr(replace(replace(substr(installed_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN installed_at LIKE '____-__-__ __:__:__' AND length(installed_at) = 19
            THEN substr(installed_at, 1, 10) || 'T' || substr(installed_at, 12, 8) || '.000000000Z'
        WHEN installed_at LIKE '____-__-__T__:__:__%' AND length(installed_at) <= 35
             AND substr(installed_at, length(installed_at) - 5, 1) IN ('+', '-') AND substr(installed_at, length(installed_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(installed_at, 1, 19) || 'Z',
                     (CASE WHEN substr(installed_at, length(installed_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(installed_at, length(installed_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(installed_at, length(installed_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(installed_at, length(installed_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(installed_at, 20, length(installed_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE installed_at
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
    END
WHERE (installed_at IS NOT NULL AND NOT (installed_at LIKE '____-__-__T__:__:__%' AND length(installed_at) = 30 AND substr(installed_at, 20, 1) = '.' AND substr(installed_at, 30, 1) = 'Z'))
   OR (updated_at IS NOT NULL AND NOT (updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) = 30 AND substr(updated_at, 20, 1) = '.' AND substr(updated_at, 30, 1) = 'Z'));

UPDATE relationship_heads
SET updated_at = CASE
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
    END
WHERE (updated_at IS NOT NULL AND NOT (updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) = 30 AND substr(updated_at, 20, 1) = '.' AND substr(updated_at, 30, 1) = 'Z'));

UPDATE relationship_versions
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
    END
WHERE (created_at IS NOT NULL AND NOT (created_at LIKE '____-__-__T__:__:__%' AND length(created_at) = 30 AND substr(created_at, 20, 1) = '.' AND substr(created_at, 30, 1) = 'Z'))
   OR (updated_at IS NOT NULL AND NOT (updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) = 30 AND substr(updated_at, 20, 1) = '.' AND substr(updated_at, 30, 1) = 'Z'));

UPDATE schedules
SET start_at = CASE
        WHEN start_at IS NULL THEN NULL
        WHEN start_at LIKE '____-__-__T__:__:__%' AND length(start_at) <= 30 AND substr(start_at, length(start_at), 1) = 'Z'
            THEN substr(start_at, 1, 19) || '.'
                 || substr(replace(replace(substr(start_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN start_at LIKE '____-__-__ __:__:__' AND length(start_at) = 19
            THEN substr(start_at, 1, 10) || 'T' || substr(start_at, 12, 8) || '.000000000Z'
        WHEN start_at LIKE '____-__-__T__:__:__%' AND length(start_at) <= 35
             AND substr(start_at, length(start_at) - 5, 1) IN ('+', '-') AND substr(start_at, length(start_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(start_at, 1, 19) || 'Z',
                     (CASE WHEN substr(start_at, length(start_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(start_at, length(start_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(start_at, length(start_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(start_at, length(start_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(start_at, 20, length(start_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE start_at
    END,
    next_run_at = CASE
        WHEN next_run_at IS NULL THEN NULL
        WHEN next_run_at LIKE '____-__-__T__:__:__%' AND length(next_run_at) <= 30 AND substr(next_run_at, length(next_run_at), 1) = 'Z'
            THEN substr(next_run_at, 1, 19) || '.'
                 || substr(replace(replace(substr(next_run_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN next_run_at LIKE '____-__-__ __:__:__' AND length(next_run_at) = 19
            THEN substr(next_run_at, 1, 10) || 'T' || substr(next_run_at, 12, 8) || '.000000000Z'
        WHEN next_run_at LIKE '____-__-__T__:__:__%' AND length(next_run_at) <= 35
             AND substr(next_run_at, length(next_run_at) - 5, 1) IN ('+', '-') AND substr(next_run_at, length(next_run_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(next_run_at, 1, 19) || 'Z',
                     (CASE WHEN substr(next_run_at, length(next_run_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(next_run_at, length(next_run_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(next_run_at, length(next_run_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(next_run_at, length(next_run_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(next_run_at, 20, length(next_run_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE next_run_at
    END,
    last_run_at = CASE
        WHEN last_run_at IS NULL THEN NULL
        WHEN last_run_at LIKE '____-__-__T__:__:__%' AND length(last_run_at) <= 30 AND substr(last_run_at, length(last_run_at), 1) = 'Z'
            THEN substr(last_run_at, 1, 19) || '.'
                 || substr(replace(replace(substr(last_run_at, 20), 'Z', ''), '.', '') || '000000000', 1, 9) || 'Z'
        WHEN last_run_at LIKE '____-__-__ __:__:__' AND length(last_run_at) = 19
            THEN substr(last_run_at, 1, 10) || 'T' || substr(last_run_at, 12, 8) || '.000000000Z'
        WHEN last_run_at LIKE '____-__-__T__:__:__%' AND length(last_run_at) <= 35
             AND substr(last_run_at, length(last_run_at) - 5, 1) IN ('+', '-') AND substr(last_run_at, length(last_run_at) - 2, 1) = ':'
            THEN strftime('%Y-%m-%dT%H:%M:%S', substr(last_run_at, 1, 19) || 'Z',
                     (CASE WHEN substr(last_run_at, length(last_run_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(last_run_at, length(last_run_at) - 4, 2) || ' hours',
                     (CASE WHEN substr(last_run_at, length(last_run_at) - 5, 1) = '+' THEN '-' ELSE '+' END) || substr(last_run_at, length(last_run_at) - 1, 2) || ' minutes')
                 || '.'
                 || substr(replace(substr(last_run_at, 20, length(last_run_at) - 25), '.', '') || '000000000', 1, 9) || 'Z'
        ELSE last_run_at
    END,
    created_at = CASE
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
    END
WHERE (start_at IS NOT NULL AND NOT (start_at LIKE '____-__-__T__:__:__%' AND length(start_at) = 30 AND substr(start_at, 20, 1) = '.' AND substr(start_at, 30, 1) = 'Z'))
   OR (next_run_at IS NOT NULL AND NOT (next_run_at LIKE '____-__-__T__:__:__%' AND length(next_run_at) = 30 AND substr(next_run_at, 20, 1) = '.' AND substr(next_run_at, 30, 1) = 'Z'))
   OR (last_run_at IS NOT NULL AND NOT (last_run_at LIKE '____-__-__T__:__:__%' AND length(last_run_at) = 30 AND substr(last_run_at, 20, 1) = '.' AND substr(last_run_at, 30, 1) = 'Z'))
   OR (created_at IS NOT NULL AND NOT (created_at LIKE '____-__-__T__:__:__%' AND length(created_at) = 30 AND substr(created_at, 20, 1) = '.' AND substr(created_at, 30, 1) = 'Z'))
   OR (updated_at IS NOT NULL AND NOT (updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) = 30 AND substr(updated_at, 20, 1) = '.' AND substr(updated_at, 30, 1) = 'Z'));

UPDATE tool_calls
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
    END
WHERE (created_at IS NOT NULL AND NOT (created_at LIKE '____-__-__T__:__:__%' AND length(created_at) = 30 AND substr(created_at, 20, 1) = '.' AND substr(created_at, 30, 1) = 'Z'))
   OR (updated_at IS NOT NULL AND NOT (updated_at LIKE '____-__-__T__:__:__%' AND length(updated_at) = 30 AND substr(updated_at, 20, 1) = '.' AND substr(updated_at, 30, 1) = 'Z'));


-- job_runs.execution_key is the uniqueness key that makes a scheduled run
-- idempotent; it is built in Go as "<schedule_id>:<scheduled_for>" with the
-- same timestamp encoding. Recompute it from the row's own now-canonical
-- scheduled_for so an existing queued run still collides with the key a new
-- claim would generate. Only rows whose key still has the exact legacy shape
-- are touched, so a key from any other source is left alone.
UPDATE job_runs
SET execution_key = schedule_id || ':' || scheduled_for
WHERE execution_key <> schedule_id || ':' || scheduled_for
  AND execution_key LIKE schedule_id || ':____-__-__T__:__:__%';

-- messages_fts.created_at was rebuilt from messages above, so the projection
-- and its source agree. It is declared UNINDEXED in any case: carried metadata,
-- never tokenized, never MATCHed, never compared or ordered on (archive search
-- orders by bm25 then messages.created_at).
--
-- schema_migrations.applied_at is deliberately not touched. It defaults to
-- CURRENT_TIMESTAMP, is never read by the application, is ordered by the
-- integer version column, and this migration's own row is inserted after this
-- statement list commits.

-- app_metadata is a schema-generation marker table written only by migrations;
-- its updated_at default is CURRENT_TIMESTAMP, so set it explicitly here to
-- keep this row in the canonical encoding the statements above just enforced.
INSERT OR IGNORE INTO app_metadata(key, value, updated_at)
VALUES ('timestamp_encoding_generation', 'fixed-width-nanos-v1',
        strftime('%Y-%m-%dT%H:%M:%S', 'now') || '.000000000Z');
