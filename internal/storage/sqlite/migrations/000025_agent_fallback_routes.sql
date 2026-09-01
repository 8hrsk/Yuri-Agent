-- Explicit per-agent fallback routes. They are owner configuration, not a
-- silent provider policy: the runtime must opt in and record every switch.
-- Provider credentials remain outside SQLite in the system keyring.

ALTER TABLE agent_profiles ADD COLUMN fallback_enabled INTEGER NOT NULL DEFAULT 0
    CHECK (fallback_enabled IN (0, 1));
ALTER TABLE agent_profiles ADD COLUMN fallback_provider_id TEXT NOT NULL DEFAULT ''
    CHECK (length(fallback_provider_id) <= 128);
ALTER TABLE agent_profiles ADD COLUMN fallback_model TEXT NOT NULL DEFAULT ''
    CHECK (length(fallback_model) <= 256);

-- A running durable run may switch once to its explicitly configured fallback
-- before any visible output or side effect. The original attribution trigger
-- remains strict for ordinary saves; this guarded counter is the only durable
-- proof that a switch was intentional and one-shot.
ALTER TABLE agent_runs ADD COLUMN inference_route_switches INTEGER NOT NULL DEFAULT 0
    CHECK (inference_route_switches BETWEEN 0 AND 1);
ALTER TABLE agent_runs ADD COLUMN initial_provider_id TEXT NOT NULL DEFAULT ''
    CHECK (length(initial_provider_id) <= 128);
ALTER TABLE agent_runs ADD COLUMN initial_model TEXT NOT NULL DEFAULT ''
    CHECK (length(initial_model) <= 256);

UPDATE agent_runs
SET initial_provider_id = provider_id,
    initial_model = model;

DROP TRIGGER IF EXISTS agent_runs_inference_route_immutable;
CREATE TRIGGER agent_runs_inference_route_immutable
BEFORE UPDATE OF provider_id, model, initial_provider_id, initial_model, inference_route_switches ON agent_runs
WHEN NEW.provider_id <> OLD.provider_id
  OR NEW.model <> OLD.model
  OR NEW.initial_provider_id <> OLD.initial_provider_id
  OR NEW.initial_model <> OLD.initial_model
  OR NEW.inference_route_switches <> OLD.inference_route_switches
BEGIN
    SELECT CASE WHEN NEW.initial_provider_id <> OLD.initial_provider_id
                  OR NEW.initial_model <> OLD.initial_model
                  OR (NEW.provider_id = OLD.provider_id AND NEW.model = OLD.model)
                  OR OLD.state <> 'running'
                  OR OLD.inference_route_switches <> 0
                  OR NEW.inference_route_switches <> OLD.inference_route_switches + 1
                  OR NEW.provider_id = ''
                  OR NEW.model = ''
                  OR OLD.input_tokens <> 0
                  OR OLD.output_tokens <> 0
                  OR OLD.total_tokens <> 0
                THEN RAISE(ABORT, 'agent run inference route is immutable') END;
END;

CREATE INDEX IF NOT EXISTS idx_agent_profiles_fallback_provider
    ON agent_profiles(fallback_provider_id, id)
    WHERE fallback_enabled = 1 AND fallback_provider_id <> '';

CREATE TRIGGER IF NOT EXISTS agent_profiles_validate_fallback_insert
BEFORE INSERT ON agent_profiles
WHEN (NEW.fallback_enabled = 1 AND (trim(NEW.fallback_provider_id) = '' OR trim(NEW.fallback_model) = ''))
  OR ((trim(NEW.fallback_provider_id) = '') <> (trim(NEW.fallback_model) = ''))
BEGIN
    SELECT RAISE(ABORT, 'enabled fallback requires a provider and model');
END;

CREATE TRIGGER IF NOT EXISTS agent_profiles_validate_fallback_update
BEFORE UPDATE OF fallback_enabled, fallback_provider_id, fallback_model ON agent_profiles
WHEN (NEW.fallback_enabled = 1 AND (trim(NEW.fallback_provider_id) = '' OR trim(NEW.fallback_model) = ''))
  OR ((trim(NEW.fallback_provider_id) = '') <> (trim(NEW.fallback_model) = ''))
BEGIN
    SELECT RAISE(ABORT, 'enabled fallback requires a provider and model');
END;

INSERT OR REPLACE INTO app_metadata(key, value)
VALUES ('agent_fallback_routes', 'explicit-owner-opt-in-v1');

INSERT OR REPLACE INTO app_metadata(key, value)
VALUES ('run_inference_fallback', 'guarded-one-shot-before-visible-output-v1');
