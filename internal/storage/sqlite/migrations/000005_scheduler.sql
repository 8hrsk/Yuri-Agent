-- Stage 4 durable schedules and worker leases. The scheduler stores task
-- payloads as opaque JSON; only the worker/application layer interprets them.
-- A schedule is soft-deleted so its run history remains auditable.

ALTER TABLE agent_runs ADD COLUMN max_tool_calls INTEGER NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS schedules (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    expression TEXT NOT NULL DEFAULT '',
    timezone TEXT NOT NULL,
    start_at TEXT NOT NULL,
    interval_seconds INTEGER NOT NULL DEFAULT 0,
    payload_json TEXT NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'active',
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    misfire_policy TEXT NOT NULL DEFAULT 'run_once',
    next_run_at TEXT,
    last_run_at TEXT,
    retry_max_attempts INTEGER NOT NULL DEFAULT 3,
    retry_initial_backoff_seconds INTEGER NOT NULL DEFAULT 5,
    retry_max_backoff_seconds INTEGER NOT NULL DEFAULT 300,
    max_duration_seconds INTEGER NOT NULL DEFAULT 0,
    max_tokens INTEGER NOT NULL DEFAULT 0,
    max_tool_calls INTEGER NOT NULL DEFAULT 0,
    allow_overlap INTEGER NOT NULL DEFAULT 0 CHECK (allow_overlap IN (0, 1)),
    history_limit INTEGER NOT NULL DEFAULT 100,
    version INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_schedules_due
    ON schedules(enabled, status, next_run_at, id);
CREATE INDEX IF NOT EXISTS idx_schedules_updated
    ON schedules(updated_at DESC, id);

CREATE TABLE IF NOT EXISTS job_runs (
    id TEXT PRIMARY KEY,
    schedule_id TEXT NOT NULL,
    state TEXT NOT NULL,
    trigger TEXT NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 0,
    execution_key TEXT NOT NULL,
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_token TEXT NOT NULL DEFAULT '',
    lease_until TEXT,
    scheduled_for TEXT NOT NULL,
    retry_at TEXT,
    started_at TEXT,
    finished_at TEXT,
    error TEXT NOT NULL DEFAULT '',
    result_ref TEXT NOT NULL DEFAULT '',
    version INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (schedule_id) REFERENCES schedules(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_job_runs_execution_key
    ON job_runs(schedule_id, execution_key);
CREATE INDEX IF NOT EXISTS idx_job_runs_queue
    ON job_runs(state, retry_at, created_at, id);
CREATE INDEX IF NOT EXISTS idx_job_runs_schedule_state
    ON job_runs(schedule_id, state, lease_until, created_at, id);
CREATE INDEX IF NOT EXISTS idx_job_runs_lease
    ON job_runs(state, lease_until);
