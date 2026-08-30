-- Indexes for the list queries that had no covering index.
--
-- agent_runs is indexed on (conversation_id, …), (state, …) and (agent_id, …),
-- but RunRepository.ListByKind filters on kind alone and fell back to a full
-- table scan. The recovery paths that enumerate background runs by kind grow
-- with the whole run history, not with the number of matches.
CREATE INDEX IF NOT EXISTS idx_agent_runs_kind_created
    ON agent_runs(kind, created_at, id);

-- tool_calls has a partial unique index on (run_id, idempotency_key) with a
-- "WHERE idempotency_key <> ''" clause. A partial index is only usable when
-- the planner can prove the WHERE clause holds, which it cannot for the
-- parameterized lookup in FindByIdempotencyKey, so that query fell back to
-- idx_tool_calls_run_created and filtered in the scan. This unconditional
-- index covers the lookup; the partial one still enforces uniqueness.
CREATE INDEX IF NOT EXISTS idx_tool_calls_run_idempotency_lookup
    ON tool_calls(run_id, idempotency_key);
