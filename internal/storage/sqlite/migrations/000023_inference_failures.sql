-- Provider-neutral, secret-free failure metadata for actionable UI. Existing
-- failures remain uncategorized rather than being guessed from old strings.

ALTER TABLE agent_runs ADD COLUMN failure_kind TEXT NOT NULL DEFAULT ''
    CHECK (failure_kind IN ('', 'unknown', 'authentication', 'rate_limit', 'quota_exhausted', 'context_limit', 'model_unavailable', 'timeout', 'transient', 'invalid_request', 'budget_exceeded'));
ALTER TABLE agent_runs ADD COLUMN failure_retryable INTEGER NOT NULL DEFAULT 0 CHECK (failure_retryable IN (0, 1));
ALTER TABLE agent_runs ADD COLUMN failure_retry_after_seconds INTEGER NOT NULL DEFAULT 0 CHECK (failure_retry_after_seconds BETWEEN 0 AND 86400);

ALTER TABLE peer_dialogues ADD COLUMN failure_kind TEXT NOT NULL DEFAULT ''
    CHECK (failure_kind IN ('', 'unknown', 'authentication', 'rate_limit', 'quota_exhausted', 'context_limit', 'model_unavailable', 'timeout', 'transient', 'invalid_request', 'budget_exceeded'));
ALTER TABLE peer_dialogues ADD COLUMN failure_retryable INTEGER NOT NULL DEFAULT 0 CHECK (failure_retryable IN (0, 1));
ALTER TABLE peer_dialogues ADD COLUMN failure_retry_after_seconds INTEGER NOT NULL DEFAULT 0 CHECK (failure_retry_after_seconds BETWEEN 0 AND 86400);

INSERT OR REPLACE INTO app_metadata(key, value)
VALUES ('inference_failure_metadata', 'provider-neutral-v1');
