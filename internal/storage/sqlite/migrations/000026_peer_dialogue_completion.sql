-- Bounded semantic completion for named-agent peer dialogues.
--
-- The original peer_dialogues table encoded max_turns <= 4 directly in a
-- CHECK constraint. SQLite cannot alter a CHECK in place, so this migration
-- rebuilds the three tables that point at the aggregate. Existing rows retain
-- their old "run until max turns" behavior by receiving min_turns = max_turns;
-- new callers can choose a lower semantic minimum. Empty completion_reason is
-- preserved for historical terminal rows whose stopping cause was not stored.

DROP TRIGGER IF EXISTS peer_dialogues_enforce_cooldown_insert;
DROP TRIGGER IF EXISTS peer_dialogues_validate_trigger_insert;
DROP TRIGGER IF EXISTS peer_dialogues_validate_trigger_update;
DROP TRIGGER IF EXISTS peer_dialogues_trigger_provenance_immutable;
DROP TRIGGER IF EXISTS peer_dialogue_messages_validate_participants_insert;
DROP TRIGGER IF EXISTS peer_dialogue_messages_validate_source_insert;
DROP TRIGGER IF EXISTS peer_dialogue_messages_validate_participants_update;
DROP TRIGGER IF EXISTS peer_dialogue_messages_validate_source_update;

DROP INDEX IF EXISTS idx_peer_dialogues_initiator_created;
DROP INDEX IF EXISTS idx_peer_dialogues_peer_created;
DROP INDEX IF EXISTS idx_peer_dialogues_pair_created;
DROP INDEX IF EXISTS idx_peer_dialogues_trigger;
DROP INDEX IF EXISTS idx_peer_dialogues_status_updated;
DROP INDEX IF EXISTS idx_peer_dialogues_trigger_kind_created;
DROP INDEX IF EXISTS idx_peer_dialogues_active_pair;
DROP INDEX IF EXISTS idx_peer_dialogue_messages_dialogue_sequence;
DROP INDEX IF EXISTS idx_peer_dialogue_messages_sender_created;
DROP INDEX IF EXISTS idx_peer_social_reflections_observer;

-- Renaming the parent first updates the foreign keys in both child tables to
-- point at the temporary parent. They are rebuilt after the new parent exists.
ALTER TABLE peer_dialogues RENAME TO peer_dialogues_v25;

CREATE TABLE peer_dialogues (
    id TEXT PRIMARY KEY,
    initiator_agent_id TEXT NOT NULL,
    peer_agent_id TEXT NOT NULL,
    trigger_run_id TEXT NOT NULL,
    trigger_kind TEXT NOT NULL DEFAULT 'agent_tool'
        CHECK (trigger_kind IN ('agent_tool', 'autonomous')),
    trigger_reason TEXT NOT NULL DEFAULT 'Агент явно запросил консультацию peer через tool.'
        CHECK (length(trim(trigger_reason)) BETWEEN 1 AND 512),
    pair_key TEXT NOT NULL,
    purpose TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'cancelling', 'completed', 'failed', 'cancelled', 'expired')),
    min_turns INTEGER NOT NULL DEFAULT 1 CHECK (min_turns >= 1 AND min_turns <= max_turns),
    max_turns INTEGER NOT NULL CHECK (max_turns >= 1 AND max_turns <= 8),
    max_tokens INTEGER NOT NULL CHECK (max_tokens >= 1 AND max_tokens <= 16000),
    max_duration_seconds INTEGER NOT NULL CHECK (max_duration_seconds >= 5 AND max_duration_seconds <= 300),
    cooldown_seconds INTEGER NOT NULL DEFAULT 0 CHECK (cooldown_seconds >= 0 AND cooldown_seconds <= 86400),
    turn_count INTEGER NOT NULL DEFAULT 0 CHECK (turn_count >= 0 AND turn_count <= max_turns),
    tokens_used INTEGER NOT NULL DEFAULT 0 CHECK (tokens_used >= 0 AND tokens_used <= max_tokens),
    completion_reason TEXT NOT NULL DEFAULT ''
        CHECK (completion_reason IN ('', 'semantic', 'max_turns', 'max_tokens', 'implicit')),
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    failure TEXT NOT NULL DEFAULT '',
    failure_kind TEXT NOT NULL DEFAULT ''
        CHECK (failure_kind IN ('', 'unknown', 'authentication', 'rate_limit', 'quota_exhausted', 'context_limit', 'model_unavailable', 'timeout', 'transient', 'invalid_request', 'budget_exceeded')),
    failure_retryable INTEGER NOT NULL DEFAULT 0 CHECK (failure_retryable IN (0, 1)),
    failure_retry_after_seconds INTEGER NOT NULL DEFAULT 0 CHECK (failure_retry_after_seconds BETWEEN 0 AND 86400),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    expires_at TEXT NOT NULL,
    CHECK (initiator_agent_id <> peer_agent_id),
    CHECK (length(trim(pair_key)) > 0),
    CHECK (length(trim(purpose)) BETWEEN 1 AND 256),
    CHECK (length(trim(idempotency_key)) BETWEEN 1 AND 256),
    CHECK (length(trim(request_hash)) BETWEEN 1 AND 256),
    CHECK (length(failure) <= 1024),
    CHECK (status <> 'failed' OR length(trim(failure)) > 0),
    CHECK (status = 'failed' OR trim(failure) = ''),
    CHECK (status <> 'completed' OR turn_count > 0),
    CHECK (status = 'completed' OR completion_reason = ''),
    CHECK (completion_reason <> 'semantic' OR turn_count >= min_turns),
    FOREIGN KEY (initiator_agent_id) REFERENCES agent_profiles(id) ON DELETE RESTRICT,
    FOREIGN KEY (peer_agent_id) REFERENCES agent_profiles(id) ON DELETE RESTRICT,
    FOREIGN KEY (trigger_run_id) REFERENCES agent_runs(id) ON DELETE CASCADE,
    UNIQUE (initiator_agent_id, trigger_run_id, idempotency_key)
);

INSERT INTO peer_dialogues(
    id, initiator_agent_id, peer_agent_id, trigger_run_id, trigger_kind, trigger_reason, pair_key, purpose, status,
    min_turns, max_turns, max_tokens, max_duration_seconds, cooldown_seconds, turn_count, tokens_used,
    completion_reason, idempotency_key, request_hash, failure, failure_kind, failure_retryable, failure_retry_after_seconds,
    version, created_at, updated_at, started_at, finished_at, expires_at
)
SELECT
    id, initiator_agent_id, peer_agent_id, trigger_run_id, trigger_kind, trigger_reason, pair_key, purpose, status,
    max_turns, max_turns, max_tokens, max_duration_seconds, cooldown_seconds, turn_count, tokens_used,
    '',
    idempotency_key, request_hash, failure, failure_kind, failure_retryable, failure_retry_after_seconds,
    version, created_at, updated_at, started_at, finished_at, expires_at
FROM peer_dialogues_v25;

-- Rebuild message storage so its foreign key points at the new aggregate.
ALTER TABLE peer_dialogue_messages RENAME TO peer_dialogue_messages_v25;

CREATE TABLE peer_dialogue_messages (
    id TEXT PRIMARY KEY,
    dialogue_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence >= 0),
    sender_agent_id TEXT NOT NULL,
    recipient_agent_id TEXT NOT NULL,
    source_run_id TEXT NOT NULL,
    content TEXT NOT NULL CHECK (length(trim(content)) > 0 AND length(content) <= 16384),
    created_at TEXT NOT NULL,
    UNIQUE (dialogue_id, sequence),
    FOREIGN KEY (dialogue_id) REFERENCES peer_dialogues(id) ON DELETE CASCADE,
    FOREIGN KEY (sender_agent_id) REFERENCES agent_profiles(id) ON DELETE RESTRICT,
    FOREIGN KEY (recipient_agent_id) REFERENCES agent_profiles(id) ON DELETE RESTRICT,
    FOREIGN KEY (source_run_id) REFERENCES agent_runs(id) ON DELETE CASCADE,
    CHECK (sender_agent_id <> recipient_agent_id)
);

INSERT INTO peer_dialogue_messages(
    id, dialogue_id, sequence, sender_agent_id, recipient_agent_id, source_run_id, content, created_at
)
SELECT id, dialogue_id, sequence, sender_agent_id, recipient_agent_id, source_run_id, content, created_at
FROM peer_dialogue_messages_v25;

-- Rebuild social-reflection markers for the same reason as the message log.
ALTER TABLE peer_social_reflections RENAME TO peer_social_reflections_v25;

CREATE TABLE peer_social_reflections (
    dialogue_id TEXT NOT NULL,
    observer_agent_id TEXT NOT NULL,
    subject_agent_id TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('no_change', 'changed')),
    reason TEXT NOT NULL DEFAULT '',
    relationship_version INTEGER NOT NULL DEFAULT 0 CHECK (relationship_version >= 0),
    affect_version INTEGER NOT NULL DEFAULT 0 CHECK (affect_version >= 0),
    created_at TEXT NOT NULL,
    PRIMARY KEY (dialogue_id, observer_agent_id),
    CHECK (observer_agent_id <> subject_agent_id),
    FOREIGN KEY (dialogue_id) REFERENCES peer_dialogues(id) ON DELETE CASCADE,
    FOREIGN KEY (observer_agent_id, subject_agent_id)
        REFERENCES agent_peer_relationships(observer_agent_id, subject_agent_id) ON DELETE CASCADE
);

INSERT INTO peer_social_reflections(
    dialogue_id, observer_agent_id, subject_agent_id, outcome, reason,
    relationship_version, affect_version, created_at
)
SELECT dialogue_id, observer_agent_id, subject_agent_id, outcome, reason,
       relationship_version, affect_version, created_at
FROM peer_social_reflections_v25;

DROP TABLE peer_dialogue_messages_v25;
DROP TABLE peer_social_reflections_v25;
DROP TABLE peer_dialogues_v25;

CREATE INDEX idx_peer_dialogues_initiator_created
    ON peer_dialogues(initiator_agent_id, created_at ASC, id ASC);
CREATE INDEX idx_peer_dialogues_peer_created
    ON peer_dialogues(peer_agent_id, created_at ASC, id ASC);
CREATE INDEX idx_peer_dialogues_pair_created
    ON peer_dialogues(pair_key, created_at DESC, id DESC);
CREATE INDEX idx_peer_dialogues_trigger
    ON peer_dialogues(trigger_run_id);
CREATE INDEX idx_peer_dialogues_status_updated
    ON peer_dialogues(status, updated_at DESC, id);
CREATE INDEX idx_peer_dialogues_trigger_kind_created
    ON peer_dialogues(trigger_kind, initiator_agent_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX idx_peer_dialogues_active_pair
    ON peer_dialogues(pair_key)
    WHERE status IN ('queued', 'running', 'cancelling');
CREATE INDEX idx_peer_dialogue_messages_dialogue_sequence
    ON peer_dialogue_messages(dialogue_id, sequence ASC, id ASC);
CREATE INDEX idx_peer_dialogue_messages_sender_created
    ON peer_dialogue_messages(sender_agent_id, created_at DESC, id DESC);
CREATE INDEX idx_peer_social_reflections_observer
    ON peer_social_reflections(observer_agent_id, created_at DESC);

CREATE TRIGGER peer_dialogues_enforce_cooldown_insert
BEFORE INSERT ON peer_dialogues
WHEN EXISTS (
    SELECT 1
    FROM peer_dialogues AS previous
    WHERE previous.pair_key = NEW.pair_key
      AND previous.status NOT IN ('queued', 'running', 'cancelling')
      AND MAX(previous.cooldown_seconds, NEW.cooldown_seconds) > 0
      AND julianday(previous.created_at) >=
          julianday(NEW.created_at) - MAX(previous.cooldown_seconds, NEW.cooldown_seconds) / 86400.0
)
BEGIN
    SELECT RAISE(ABORT, 'peer dialogue pair is in cooldown');
END;

CREATE TRIGGER peer_dialogues_validate_trigger_insert
BEFORE INSERT ON peer_dialogues
WHEN NOT EXISTS (
    SELECT 1
    FROM agent_runs AS run
    WHERE run.id = NEW.trigger_run_id
      AND run.agent_id = NEW.initiator_agent_id
      AND run.kind <> 'subagent'
      AND run.parent_run_id IS NULL
)
BEGIN
    SELECT RAISE(ABORT, 'peer dialogue trigger run must be an initiator-owned root run');
END;

CREATE TRIGGER peer_dialogues_validate_trigger_update
BEFORE UPDATE OF initiator_agent_id, peer_agent_id, trigger_run_id, pair_key ON peer_dialogues
WHEN NOT EXISTS (
    SELECT 1
    FROM agent_runs AS run
    WHERE run.id = NEW.trigger_run_id
      AND run.agent_id = NEW.initiator_agent_id
      AND run.kind <> 'subagent'
      AND run.parent_run_id IS NULL
)
BEGIN
    SELECT RAISE(ABORT, 'peer dialogue trigger run must be an initiator-owned root run');
END;

CREATE TRIGGER peer_dialogues_trigger_provenance_immutable
BEFORE UPDATE OF trigger_kind, trigger_reason ON peer_dialogues
WHEN NEW.trigger_kind <> OLD.trigger_kind OR NEW.trigger_reason <> OLD.trigger_reason
BEGIN
    SELECT RAISE(ABORT, 'peer dialogue trigger provenance is immutable');
END;

CREATE TRIGGER peer_dialogue_completion_reason_immutable
BEFORE UPDATE OF completion_reason ON peer_dialogues
WHEN OLD.completion_reason <> '' AND NEW.completion_reason <> OLD.completion_reason
BEGIN
    SELECT RAISE(ABORT, 'peer dialogue completion reason is immutable');
END;

CREATE TRIGGER peer_dialogue_messages_validate_participants_insert
BEFORE INSERT ON peer_dialogue_messages
WHEN NOT EXISTS (
    SELECT 1
    FROM peer_dialogues AS dialogue
    WHERE dialogue.id = NEW.dialogue_id
      AND dialogue.turn_count = NEW.sequence
      AND (
          (NEW.sequence % 2 = 0
              AND NEW.sender_agent_id = dialogue.initiator_agent_id
              AND NEW.recipient_agent_id = dialogue.peer_agent_id)
          OR
          (NEW.sequence % 2 = 1
              AND NEW.sender_agent_id = dialogue.peer_agent_id
              AND NEW.recipient_agent_id = dialogue.initiator_agent_id)
      )
)
BEGIN
    SELECT RAISE(ABORT, 'peer dialogue message participants or sequence are invalid');
END;

CREATE TRIGGER peer_dialogue_messages_validate_source_insert
BEFORE INSERT ON peer_dialogue_messages
WHEN NOT EXISTS (
    SELECT 1
    FROM agent_runs AS run
    WHERE run.id = NEW.source_run_id
      AND run.agent_id = NEW.sender_agent_id
)
BEGIN
    SELECT RAISE(ABORT, 'peer dialogue message source run is not owned by sender');
END;

CREATE TRIGGER peer_dialogue_messages_validate_participants_update
BEFORE UPDATE OF dialogue_id, sequence, sender_agent_id, recipient_agent_id ON peer_dialogue_messages
WHEN NOT EXISTS (
    SELECT 1
    FROM peer_dialogues AS dialogue
    WHERE dialogue.id = NEW.dialogue_id
      AND dialogue.turn_count = NEW.sequence
      AND (
          (NEW.sequence % 2 = 0
              AND NEW.sender_agent_id = dialogue.initiator_agent_id
              AND NEW.recipient_agent_id = dialogue.peer_agent_id)
          OR
          (NEW.sequence % 2 = 1
              AND NEW.sender_agent_id = dialogue.peer_agent_id
              AND NEW.recipient_agent_id = dialogue.initiator_agent_id)
      )
)
BEGIN
    SELECT RAISE(ABORT, 'peer dialogue message participants or sequence are invalid');
END;

CREATE TRIGGER peer_dialogue_messages_validate_source_update
BEFORE UPDATE OF dialogue_id, sender_agent_id, source_run_id ON peer_dialogue_messages
WHEN NOT EXISTS (
    SELECT 1
    FROM agent_runs AS run
    WHERE run.id = NEW.source_run_id
      AND run.agent_id = NEW.sender_agent_id
)
BEGIN
    SELECT RAISE(ABORT, 'peer dialogue message source run is not owned by sender');
END;

INSERT OR REPLACE INTO app_metadata(key, value)
VALUES ('peer_dialogue_schema_generation', 'named-agents-v2-semantic-completion');
