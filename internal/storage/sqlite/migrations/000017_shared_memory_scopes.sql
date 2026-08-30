-- Stage 8 explicit cross-agent memory publication. Existing records remain
-- private; only a new version created by an owner action may widen scope.

DROP TRIGGER IF EXISTS memory_versions_require_private_scope_insert;
DROP TRIGGER IF EXISTS memory_versions_require_private_scope_update;

CREATE TRIGGER IF NOT EXISTS memory_versions_require_known_scope_insert
BEFORE INSERT ON memory_versions
WHEN NEW.scope NOT IN ('agent_private', 'owner_shared', 'installation_shared')
BEGIN
    SELECT RAISE(ABORT, 'unsupported memory scope');
END;

CREATE TRIGGER IF NOT EXISTS memory_versions_require_known_scope_update
BEFORE UPDATE OF scope ON memory_versions
WHEN NEW.scope NOT IN ('agent_private', 'owner_shared', 'installation_shared')
BEGIN
    SELECT RAISE(ABORT, 'unsupported memory scope');
END;

CREATE TRIGGER IF NOT EXISTS memory_versions_reject_sensitive_shared_insert
BEFORE INSERT ON memory_versions
WHEN NEW.scope IN ('owner_shared', 'installation_shared')
 AND NEW.sensitivity = 'highly_sensitive'
BEGIN
    SELECT RAISE(ABORT, 'highly sensitive memory cannot be shared');
END;

CREATE TRIGGER IF NOT EXISTS memory_versions_reject_sensitive_shared_update
BEFORE UPDATE OF scope, sensitivity ON memory_versions
WHEN NEW.scope IN ('owner_shared', 'installation_shared')
 AND NEW.sensitivity = 'highly_sensitive'
BEGIN
    SELECT RAISE(ABORT, 'highly sensitive memory cannot be shared');
END;

CREATE INDEX IF NOT EXISTS idx_memory_versions_scope_state
    ON memory_versions(scope, lifecycle_state, salience DESC, updated_at DESC, memory_id);

INSERT OR REPLACE INTO app_metadata(key, value)
VALUES ('memory_scope_generation', 'explicit-shared-v2');
