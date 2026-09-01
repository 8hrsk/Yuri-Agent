-- Optional per-agent model route. Empty values preserve the legacy behavior:
-- the agent follows the installation-wide enabled provider and its model.
-- Provider credentials remain exclusively in the system keyring.
ALTER TABLE agent_profiles ADD COLUMN provider_id TEXT NOT NULL DEFAULT ''
    CHECK (length(provider_id) <= 128);

ALTER TABLE agent_profiles ADD COLUMN model TEXT NOT NULL DEFAULT ''
    CHECK (length(model) <= 256);

CREATE INDEX IF NOT EXISTS idx_agent_profiles_provider
    ON agent_profiles(provider_id, id)
    WHERE provider_id <> '';
