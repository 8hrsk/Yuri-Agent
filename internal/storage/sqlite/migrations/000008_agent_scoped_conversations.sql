-- Stage 8.2 conversation ownership. The column is backfilled to the earliest
-- named agent, which is the only reconstructable owner for pre-scope data.
-- Application repositories require a non-empty agent_id for every new row.

ALTER TABLE conversations ADD COLUMN agent_id TEXT NOT NULL DEFAULT '';

-- Very old installations may contain transcripts but no Stage 6 persona
-- head, so migration 7 had nothing to turn into a profile. Materialize the
-- legacy owner before assigning scopes instead of leaving orphaned data.
INSERT OR IGNORE INTO agent_profiles(id, name, age, gender, preferences, created_at, updated_at)
SELECT 'owner', 'Yuri', 0, 'female', '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
WHERE NOT EXISTS (SELECT 1 FROM agent_profiles)
  AND (
      EXISTS (SELECT 1 FROM conversations)
      OR EXISTS (SELECT 1 FROM agent_runs)
      OR EXISTS (SELECT 1 FROM memory_versions)
  );

UPDATE conversations
SET agent_id = COALESCE(
    (SELECT id FROM agent_profiles ORDER BY created_at ASC, id ASC LIMIT 1),
    'owner'
)
WHERE agent_id = '';

CREATE INDEX IF NOT EXISTS idx_conversations_agent_updated
    ON conversations(agent_id, updated_at DESC, id DESC);

CREATE TRIGGER IF NOT EXISTS conversations_require_agent_insert
BEFORE INSERT ON conversations
WHEN trim(NEW.agent_id) = ''
BEGIN
    SELECT RAISE(ABORT, 'conversation agent_id is required');
END;

CREATE TRIGGER IF NOT EXISTS conversations_require_agent_update
BEFORE UPDATE OF agent_id ON conversations
WHEN trim(NEW.agent_id) = ''
BEGIN
    SELECT RAISE(ABORT, 'conversation agent_id is required');
END;

INSERT OR IGNORE INTO app_metadata(key, value)
VALUES ('conversation_scope_generation', 'agent-id-v1');

-- Scope the append-only memory journal to the named agent that owns the
-- originating conversation. Legacy installations have one reconstructable
-- owner: the earliest named profile (or the explicit legacy owner marker).
ALTER TABLE memory_versions ADD COLUMN agent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE memory_versions ADD COLUMN scope TEXT NOT NULL DEFAULT 'agent_private';

UPDATE memory_versions
SET agent_id = COALESCE(
    (SELECT c.agent_id FROM conversations AS c
      WHERE c.id = memory_versions.source_conversation_id
        AND trim(c.agent_id) <> ''),
    (SELECT id FROM agent_profiles ORDER BY created_at ASC, id ASC LIMIT 1),
    'owner'
)
WHERE trim(agent_id) = '';

CREATE INDEX IF NOT EXISTS idx_memory_versions_agent_state
    ON memory_versions(agent_id, lifecycle_state, salience DESC, updated_at DESC, memory_id);

CREATE INDEX IF NOT EXISTS idx_memory_versions_agent_canonical
    ON memory_versions(agent_id, canonical_key, lifecycle_state, updated_at DESC)
    WHERE canonical_key <> '';

CREATE TRIGGER IF NOT EXISTS memory_versions_require_agent_insert
BEFORE INSERT ON memory_versions
WHEN trim(NEW.agent_id) = ''
BEGIN
    SELECT RAISE(ABORT, 'memory agent_id is required');
END;

CREATE TRIGGER IF NOT EXISTS memory_versions_require_agent_update
BEFORE UPDATE OF agent_id ON memory_versions
WHEN trim(NEW.agent_id) = ''
BEGIN
    SELECT RAISE(ABORT, 'memory agent_id is required');
END;

CREATE TRIGGER IF NOT EXISTS memory_versions_require_private_scope_insert
BEFORE INSERT ON memory_versions
WHEN NEW.scope <> 'agent_private'
BEGIN
    SELECT RAISE(ABORT, 'unsupported memory scope');
END;

CREATE TRIGGER IF NOT EXISTS memory_versions_require_private_scope_update
BEFORE UPDATE OF scope ON memory_versions
WHEN NEW.scope <> 'agent_private'
BEGIN
    SELECT RAISE(ABORT, 'unsupported memory scope');
END;

INSERT OR IGNORE INTO app_metadata(key, value)
VALUES ('memory_scope_generation', 'agent-private-v1');

-- Scope durable executions to the named agent that owns their conversation.
-- Legacy runs without a reconstructable conversation owner use the stable
-- single-agent marker so the NOT NULL invariant can be introduced without
-- dropping historical activity.
ALTER TABLE agent_runs ADD COLUMN agent_id TEXT NOT NULL DEFAULT '';

UPDATE agent_runs
SET agent_id = COALESCE(
    (SELECT NULLIF(trim(c.agent_id), '')
       FROM conversations AS c
      WHERE c.id = agent_runs.conversation_id),
    (SELECT NULLIF(trim(id), '')
       FROM agent_profiles
      ORDER BY created_at ASC, id ASC LIMIT 1),
    'owner'
)
WHERE trim(agent_id) = '';

CREATE INDEX IF NOT EXISTS idx_agent_runs_agent_created
    ON agent_runs(agent_id, created_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_agent_runs_agent_state_updated
    ON agent_runs(agent_id, state, updated_at DESC, id);

CREATE TRIGGER IF NOT EXISTS agent_runs_require_agent_insert
BEFORE INSERT ON agent_runs
WHEN trim(NEW.agent_id) = ''
BEGIN
    SELECT RAISE(ABORT, 'run agent_id is required');
END;

CREATE TRIGGER IF NOT EXISTS agent_runs_require_agent_update
BEFORE UPDATE OF agent_id ON agent_runs
WHEN trim(NEW.agent_id) = ''
BEGIN
    SELECT RAISE(ABORT, 'run agent_id is required');
END;

CREATE TRIGGER IF NOT EXISTS agent_runs_match_conversation_insert
BEFORE INSERT ON agent_runs
WHEN NEW.conversation_id IS NOT NULL
 AND NOT EXISTS (
     SELECT 1 FROM conversations AS c
      WHERE c.id = NEW.conversation_id
        AND c.agent_id = NEW.agent_id
 )
BEGIN
    SELECT RAISE(ABORT, 'run agent_id does not own conversation');
END;

CREATE TRIGGER IF NOT EXISTS agent_runs_match_conversation_update
BEFORE UPDATE OF agent_id, conversation_id ON agent_runs
WHEN NEW.conversation_id IS NOT NULL
 AND NOT EXISTS (
     SELECT 1 FROM conversations AS c
      WHERE c.id = NEW.conversation_id
        AND c.agent_id = NEW.agent_id
 )
BEGIN
    SELECT RAISE(ABORT, 'run agent_id does not own conversation');
END;

CREATE TRIGGER IF NOT EXISTS agent_runs_match_parent_insert
BEFORE INSERT ON agent_runs
WHEN NEW.parent_run_id IS NOT NULL
 AND EXISTS (
     SELECT 1 FROM agent_runs AS parent
      WHERE parent.id = NEW.parent_run_id
        AND parent.agent_id <> NEW.agent_id
 )
BEGIN
    SELECT RAISE(ABORT, 'run agent_id does not match parent run');
END;

CREATE TRIGGER IF NOT EXISTS agent_runs_match_parent_update
BEFORE UPDATE OF agent_id, parent_run_id ON agent_runs
WHEN NEW.parent_run_id IS NOT NULL
 AND EXISTS (
     SELECT 1 FROM agent_runs AS parent
      WHERE parent.id = NEW.parent_run_id
        AND parent.agent_id <> NEW.agent_id
 )
BEGIN
    SELECT RAISE(ABORT, 'run agent_id does not match parent run');
END;

INSERT OR IGNORE INTO app_metadata(key, value)
VALUES ('run_scope_generation', 'agent-id-v1');
