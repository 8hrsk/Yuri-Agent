-- Bounded provider/model usage reports filter by time before grouping. Existing
-- indexes all lead with conversation, agent or kind, so an installation-wide
-- report would otherwise scan the complete durable run history.
CREATE INDEX IF NOT EXISTS idx_agent_runs_created_route
    ON agent_runs(created_at, agent_id, provider_id, model);
