-- Per-agent execution budgets remain a compact owner preference. Concrete
-- limits are resolved at run start and persisted on each agent_run.
ALTER TABLE agent_profiles
ADD COLUMN execution_budget TEXT NOT NULL DEFAULT 'balanced'
CHECK (execution_budget IN ('efficient', 'balanced', 'extended'));

CREATE INDEX IF NOT EXISTS idx_agent_profiles_execution_budget
    ON agent_profiles(execution_budget, id);
