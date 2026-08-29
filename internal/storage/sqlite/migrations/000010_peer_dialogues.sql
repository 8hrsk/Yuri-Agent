-- Stage 8.5 bounded background conversations between two named agents.
-- Dialogue rows are owned by two durable agent profiles. They are not a new
-- identity namespace: all execution remains attributable to the initiating
-- agent and all message content is kept in this dedicated, bounded table.

CREATE TABLE IF NOT EXISTS peer_dialogues (
    id TEXT PRIMARY KEY,
    initiator_agent_id TEXT NOT NULL,
    peer_agent_id TEXT NOT NULL,
    trigger_run_id TEXT NOT NULL,
    pair_key TEXT NOT NULL,
    purpose TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'cancelling', 'completed', 'failed', 'cancelled', 'expired')),
    max_turns INTEGER NOT NULL CHECK (max_turns >= 1 AND max_turns <= 4),
    max_tokens INTEGER NOT NULL CHECK (max_tokens >= 1 AND max_tokens <= 16000),
    max_duration_seconds INTEGER NOT NULL CHECK (max_duration_seconds >= 5 AND max_duration_seconds <= 300),
    cooldown_seconds INTEGER NOT NULL DEFAULT 0 CHECK (cooldown_seconds >= 0 AND cooldown_seconds <= 86400),
    turn_count INTEGER NOT NULL DEFAULT 0 CHECK (turn_count >= 0 AND turn_count <= max_turns),
    tokens_used INTEGER NOT NULL DEFAULT 0 CHECK (tokens_used >= 0 AND tokens_used <= max_tokens),
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    failure TEXT NOT NULL DEFAULT '',
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
    FOREIGN KEY (initiator_agent_id) REFERENCES agent_profiles(id) ON DELETE RESTRICT,
    FOREIGN KEY (peer_agent_id) REFERENCES agent_profiles(id) ON DELETE RESTRICT,
    FOREIGN KEY (trigger_run_id) REFERENCES agent_runs(id) ON DELETE CASCADE,
    UNIQUE (initiator_agent_id, trigger_run_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_peer_dialogues_initiator_created
    ON peer_dialogues(initiator_agent_id, created_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_peer_dialogues_peer_created
    ON peer_dialogues(peer_agent_id, created_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_peer_dialogues_pair_created
    ON peer_dialogues(pair_key, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_peer_dialogues_trigger
    ON peer_dialogues(trigger_run_id);
CREATE INDEX IF NOT EXISTS idx_peer_dialogues_status_updated
    ON peer_dialogues(status, updated_at DESC, id);

-- A pair can have only one live exchange at a time, regardless of which
-- named agent initiated it. Terminal rows remain available for history and
-- cooldown checks.
CREATE UNIQUE INDEX IF NOT EXISTS idx_peer_dialogues_active_pair
    ON peer_dialogues(pair_key)
    WHERE status IN ('queued', 'running', 'cancelling');

-- Cooldown is enforced at insertion time as well as exposed through the
-- repository query. This closes the race where an earlier dialogue becomes
-- terminal between a caller's cooldown check and its INSERT. The larger of
-- the previous and requested cooldown windows wins.
CREATE TRIGGER IF NOT EXISTS peer_dialogues_enforce_cooldown_insert
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

-- The trigger run is a named root execution. A subagent cannot create a peer
-- dialogue and a child run cannot be used to smuggle ownership across agents.
CREATE TRIGGER IF NOT EXISTS peer_dialogues_validate_trigger_insert
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

CREATE TRIGGER IF NOT EXISTS peer_dialogues_validate_trigger_update
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

CREATE TABLE IF NOT EXISTS peer_dialogue_messages (
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

CREATE INDEX IF NOT EXISTS idx_peer_dialogue_messages_dialogue_sequence
    ON peer_dialogue_messages(dialogue_id, sequence ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_peer_dialogue_messages_sender_created
    ON peer_dialogue_messages(sender_agent_id, created_at DESC, id DESC);

-- Sequence zero is always the initiator's opening message; every following
-- message alternates between the two profile IDs. The current dialogue turn
-- count is checked as well, so a message cannot be appended without the
-- aggregate update performed by AppendPeerDialogueTurn.
CREATE TRIGGER IF NOT EXISTS peer_dialogue_messages_validate_participants_insert
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

CREATE TRIGGER IF NOT EXISTS peer_dialogue_messages_validate_source_insert
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

CREATE TRIGGER IF NOT EXISTS peer_dialogue_messages_validate_participants_update
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

CREATE TRIGGER IF NOT EXISTS peer_dialogue_messages_validate_source_update
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

INSERT OR IGNORE INTO app_metadata(key, value)
VALUES ('peer_dialogue_schema_generation', 'named-agents-v1');
