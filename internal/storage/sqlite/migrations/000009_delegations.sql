-- Stage 8.4 bounded anonymous delegation metadata. A delegation is not an
-- AgentProfile and has no persona, memory, or shared namespace of its own.
CREATE TABLE IF NOT EXISTS delegations (
    id TEXT PRIMARY KEY,
    child_run_id TEXT NOT NULL UNIQUE,
    principal_agent_id TEXT NOT NULL,
    parent_run_id TEXT NOT NULL,
    scope_json TEXT NOT NULL DEFAULT '{}',
    depth INTEGER NOT NULL DEFAULT 1 CHECK (depth = 1),
    status TEXT NOT NULL DEFAULT 'created' CHECK (status IN ('created', 'queued', 'running', 'cancelling', 'completed', 'failed', 'cancelled')),
    max_steps INTEGER NOT NULL DEFAULT 0 CHECK (max_steps >= 0),
    max_tokens INTEGER NOT NULL DEFAULT 0 CHECK (max_tokens >= 0),
    max_tool_calls INTEGER NOT NULL DEFAULT 0 CHECK (max_tool_calls >= 0),
    max_tool_output_bytes INTEGER NOT NULL DEFAULT 0 CHECK (max_tool_output_bytes >= 0),
    max_duration_seconds INTEGER NOT NULL DEFAULT 0 CHECK (max_duration_seconds >= 0),
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    result_text TEXT NOT NULL DEFAULT '',
    failure TEXT NOT NULL DEFAULT '',
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    FOREIGN KEY (parent_run_id) REFERENCES agent_runs(id) ON DELETE CASCADE,
    FOREIGN KEY (child_run_id) REFERENCES agent_runs(id) ON DELETE CASCADE,
    UNIQUE (principal_agent_id, parent_run_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_delegations_principal_created
    ON delegations(principal_agent_id, created_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_delegations_parent_created
    ON delegations(parent_run_id, created_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_delegations_child
    ON delegations(child_run_id);
CREATE INDEX IF NOT EXISTS idx_delegations_status_updated
    ON delegations(status, updated_at DESC, id);

-- The first version has exactly one anonymous level. Root runs are named
-- executions without a parent; subagent runs are conversation-less children.
CREATE TRIGGER IF NOT EXISTS agent_runs_validate_subagent_shape_insert
BEFORE INSERT ON agent_runs
WHEN (NEW.kind = 'subagent' AND (NEW.conversation_id IS NOT NULL OR NEW.parent_run_id IS NULL
    OR NOT EXISTS (SELECT 1 FROM agent_runs AS parent
                   WHERE parent.id = NEW.parent_run_id
                     AND parent.kind <> 'subagent'
                     AND parent.parent_run_id IS NULL)))
   OR (NEW.kind <> 'subagent' AND NEW.parent_run_id IS NOT NULL)
BEGIN
    SELECT RAISE(ABORT, 'invalid agent run hierarchy');
END;

CREATE TRIGGER IF NOT EXISTS agent_runs_validate_subagent_shape_update
BEFORE UPDATE OF kind, conversation_id, parent_run_id ON agent_runs
WHEN (NEW.kind = 'subagent' AND (NEW.conversation_id IS NOT NULL OR NEW.parent_run_id IS NULL
    OR NOT EXISTS (SELECT 1 FROM agent_runs AS parent
                   WHERE parent.id = NEW.parent_run_id
                     AND parent.kind <> 'subagent'
                     AND parent.parent_run_id IS NULL)))
   OR (NEW.kind <> 'subagent' AND NEW.parent_run_id IS NOT NULL)
BEGIN
    SELECT RAISE(ABORT, 'invalid agent run hierarchy');
END;

CREATE TRIGGER IF NOT EXISTS delegations_match_parent_insert
BEFORE INSERT ON delegations
WHEN NOT EXISTS (
    SELECT 1 FROM agent_runs AS parent
    WHERE parent.id = NEW.parent_run_id
      AND parent.agent_id = NEW.principal_agent_id
      AND parent.kind <> 'subagent'
      AND parent.parent_run_id IS NULL
)
BEGIN
    SELECT RAISE(ABORT, 'delegation principal does not own parent run');
END;

CREATE TRIGGER IF NOT EXISTS delegations_match_child_insert
BEFORE INSERT ON delegations
WHEN NOT EXISTS (
    SELECT 1 FROM agent_runs AS child
    WHERE child.id = NEW.child_run_id
      AND child.agent_id = NEW.principal_agent_id
      AND child.parent_run_id = NEW.parent_run_id
      AND child.conversation_id IS NULL
      AND child.kind = 'subagent'
)
BEGIN
    SELECT RAISE(ABORT, 'delegation child run is invalid');
END;

CREATE TRIGGER IF NOT EXISTS delegations_match_parent_update
BEFORE UPDATE OF principal_agent_id, parent_run_id ON delegations
WHEN NOT EXISTS (
    SELECT 1 FROM agent_runs AS parent
    WHERE parent.id = NEW.parent_run_id
      AND parent.agent_id = NEW.principal_agent_id
      AND parent.kind <> 'subagent'
      AND parent.parent_run_id IS NULL
)
BEGIN
    SELECT RAISE(ABORT, 'delegation principal does not own parent run');
END;

CREATE TRIGGER IF NOT EXISTS delegations_match_child_update
BEFORE UPDATE OF child_run_id, principal_agent_id, parent_run_id ON delegations
WHEN NOT EXISTS (
    SELECT 1 FROM agent_runs AS child
    WHERE child.id = NEW.child_run_id
      AND child.agent_id = NEW.principal_agent_id
      AND child.parent_run_id = NEW.parent_run_id
      AND child.conversation_id IS NULL
      AND child.kind = 'subagent'
)
BEGIN
    SELECT RAISE(ABORT, 'delegation child run is invalid');
END;

INSERT OR IGNORE INTO app_metadata(key, value)
VALUES ('delegation_schema_generation', 'anonymous-depth-1-v1');
