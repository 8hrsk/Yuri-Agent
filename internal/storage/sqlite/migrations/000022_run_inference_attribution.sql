-- Immutable inference provenance and provider-reported accounting for every
-- durable AgentRun. Existing runs remain explicitly unattributed rather than
-- being mislabeled with whichever provider happens to be active at migration
-- time.

ALTER TABLE agent_runs ADD COLUMN provider_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_runs ADD COLUMN model TEXT NOT NULL DEFAULT '' CHECK (provider_id <> '' OR model = '');
ALTER TABLE agent_runs ADD COLUMN input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0);
ALTER TABLE agent_runs ADD COLUMN output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0);
ALTER TABLE agent_runs ADD COLUMN total_tokens INTEGER NOT NULL DEFAULT 0 CHECK (total_tokens >= 0 AND (total_tokens = 0 OR total_tokens >= input_tokens + output_tokens));

CREATE TRIGGER IF NOT EXISTS agent_runs_inference_route_immutable
BEFORE UPDATE OF provider_id, model ON agent_runs
WHEN NEW.provider_id <> OLD.provider_id OR NEW.model <> OLD.model
BEGIN
    SELECT RAISE(ABORT, 'agent run inference route is immutable');
END;

CREATE TRIGGER IF NOT EXISTS agent_runs_usage_monotonic
BEFORE UPDATE OF input_tokens, output_tokens, total_tokens ON agent_runs
WHEN NEW.input_tokens < OLD.input_tokens
  OR NEW.output_tokens < OLD.output_tokens
  OR NEW.total_tokens < OLD.total_tokens
BEGIN
    SELECT RAISE(ABORT, 'agent run usage cannot decrease');
END;

INSERT OR REPLACE INTO app_metadata(key, value)
VALUES ('run_inference_attribution', 'immutable-provider-model-v1');
