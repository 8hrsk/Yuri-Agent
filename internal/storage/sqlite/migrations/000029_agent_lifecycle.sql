-- Owner-deleted agents remain durable historical identities. Their persona,
-- memory, conversations and peer relationships keep their original foreign
-- keys, while active roster queries exclude the tombstone.

ALTER TABLE agent_profiles ADD COLUMN deleted_at TEXT;

CREATE INDEX IF NOT EXISTS idx_agent_profiles_active_created
    ON agent_profiles(created_at, id)
    WHERE deleted_at IS NULL;

-- A stale renderer or a run racing with deletion must not resurrect a
-- tombstoned identity. Unknown legacy owner markers remain accepted; only a
-- durable profile that is explicitly deleted is rejected.
CREATE TRIGGER IF NOT EXISTS agent_runs_reject_deleted_agent_insert
BEFORE INSERT ON agent_runs
WHEN EXISTS (
    SELECT 1 FROM agent_profiles AS profile
     WHERE profile.id = NEW.agent_id
       AND profile.deleted_at IS NOT NULL
)
BEGIN
    SELECT RAISE(ABORT, 'run agent is deleted');
END;

CREATE TRIGGER IF NOT EXISTS agent_runs_reject_deleted_agent_update
BEFORE UPDATE OF agent_id ON agent_runs
WHEN EXISTS (
    SELECT 1 FROM agent_profiles AS profile
     WHERE profile.id = NEW.agent_id
       AND profile.deleted_at IS NOT NULL
)
BEGIN
    SELECT RAISE(ABORT, 'run agent is deleted');
END;

INSERT OR IGNORE INTO app_metadata(key, value)
VALUES ('agent_lifecycle_generation', 'soft-delete-v1');
