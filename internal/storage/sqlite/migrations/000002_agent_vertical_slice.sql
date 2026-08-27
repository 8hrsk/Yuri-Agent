-- Durable records used by the conversational agent vertical slice.
-- SQLite is the authoritative store.  JSON columns are intentionally kept as
-- opaque payloads at this layer so schema evolution does not require the
-- storage adapter to know provider-specific fields.

CREATE TABLE IF NOT EXISTS conversations (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    archived_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_conversations_updated_at
    ON conversations(updated_at DESC);

CREATE TABLE IF NOT EXISTS messages (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    status TEXT NOT NULL,
    provider_meta_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_messages_conversation_created
    ON messages(conversation_id, created_at, id);

CREATE TABLE IF NOT EXISTS agent_runs (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    conversation_id TEXT,
    parent_run_id TEXT,
    state TEXT NOT NULL,
    max_steps INTEGER NOT NULL DEFAULT 0,
    max_tokens INTEGER NOT NULL DEFAULT 0,
    max_tool_output_bytes INTEGER NOT NULL DEFAULT 0,
    max_duration_seconds INTEGER NOT NULL DEFAULT 0,
    failure TEXT,
    version INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE SET NULL,
    FOREIGN KEY (parent_run_id) REFERENCES agent_runs(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_agent_runs_conversation_created
    ON agent_runs(conversation_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_agent_runs_state_updated
    ON agent_runs(state, updated_at);

CREATE TABLE IF NOT EXISTS approvals (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    action_hash TEXT NOT NULL,
    action TEXT NOT NULL,
    tool_id TEXT NOT NULL DEFAULT '',
    risk TEXT NOT NULL,
    scope_json TEXT NOT NULL,
    decision TEXT NOT NULL,
    requested_at TEXT NOT NULL,
    expires_at TEXT,
    decided_at TEXT,
    decided_by TEXT,
    reason TEXT NOT NULL DEFAULT '',
    version INTEGER NOT NULL,
    FOREIGN KEY (run_id) REFERENCES agent_runs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_approvals_run_requested
    ON approvals(run_id, requested_at, id);
CREATE INDEX IF NOT EXISTS idx_approvals_pending_expiry
    ON approvals(decision, expires_at);

CREATE TABLE IF NOT EXISTS tool_calls (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    tool_id TEXT NOT NULL,
    args_redacted TEXT NOT NULL DEFAULT '{}',
    risk TEXT NOT NULL,
    approval_id TEXT,
    status TEXT NOT NULL,
    result_ref TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL DEFAULT '',
    version INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES agent_runs(id) ON DELETE CASCADE,
    FOREIGN KEY (approval_id) REFERENCES approvals(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_tool_calls_run_created
    ON tool_calls(run_id, created_at, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tool_calls_idempotency
    ON tool_calls(run_id, idempotency_key)
    WHERE idempotency_key <> '';

CREATE TABLE IF NOT EXISTS audit_events (
    id TEXT PRIMARY KEY,
    run_id TEXT,
    tool_call_id TEXT,
    approval_id TEXT,
    actor TEXT NOT NULL,
    action TEXT NOT NULL,
    target TEXT NOT NULL DEFAULT '',
    decision TEXT NOT NULL DEFAULT '',
    payload_redacted TEXT NOT NULL DEFAULT '{}',
    duration_ms INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES agent_runs(id) ON DELETE SET NULL,
    FOREIGN KEY (tool_call_id) REFERENCES tool_calls(id) ON DELETE SET NULL,
    FOREIGN KEY (approval_id) REFERENCES approvals(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_audit_events_created
    ON audit_events(created_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_events_run_created
    ON audit_events(run_id, created_at, id);
