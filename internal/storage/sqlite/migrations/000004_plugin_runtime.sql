-- Authoritative plugin installation, source verification and capability grants.
-- Executable payloads live below the application-owned plugins directory;
-- SQLite stores only validated metadata and never plugin credentials.

CREATE TABLE IF NOT EXISTS plugins (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    publisher TEXT NOT NULL,
    version TEXT NOT NULL,
    protocol_version TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    install_path TEXT NOT NULL,
    manifest_json TEXT NOT NULL,
    signature_status TEXT NOT NULL,
    checksum TEXT NOT NULL DEFAULT '',
    runtime_status TEXT NOT NULL DEFAULT 'stopped',
    last_error TEXT NOT NULL DEFAULT '',
    installed_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_plugins_enabled_status
    ON plugins(enabled, runtime_status, id);

CREATE TABLE IF NOT EXISTS plugin_sources (
    plugin_id TEXT PRIMARY KEY,
    repository_url TEXT NOT NULL DEFAULT '',
    release_tag TEXT NOT NULL DEFAULT '',
    commit_sha TEXT NOT NULL DEFAULT '',
    checksum TEXT NOT NULL DEFAULT '',
    checked_at TEXT NOT NULL,
    FOREIGN KEY (plugin_id) REFERENCES plugins(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS permission_grants (
    id TEXT PRIMARY KEY,
    subject_id TEXT NOT NULL,
    capability TEXT NOT NULL,
    scope_json TEXT NOT NULL,
    granted_at TEXT NOT NULL,
    expires_at TEXT,
    FOREIGN KEY (subject_id) REFERENCES plugins(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_permission_grants_subject
    ON permission_grants(subject_id, capability, granted_at);

CREATE UNIQUE INDEX IF NOT EXISTS idx_permission_grants_exact
    ON permission_grants(subject_id, capability, scope_json);
