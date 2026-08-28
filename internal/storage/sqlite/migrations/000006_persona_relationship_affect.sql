-- Stage 5 reflection state. Every persona, relationship, and affect snapshot
-- is append-only; *_heads are rebuildable current projections. JSON payloads
-- contain only bounded, validated internal state and provenance links.

CREATE TABLE IF NOT EXISTS persona_versions (
    persona_id TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    revision_id TEXT NOT NULL,
    parent_id TEXT,
    parent_version INTEGER NOT NULL DEFAULT 0 CHECK (parent_version >= 0),
    operation TEXT NOT NULL DEFAULT 'update',
    traits_json TEXT NOT NULL DEFAULT '{}',
    diff_json TEXT NOT NULL DEFAULT '{}',
    pinned_traits_json TEXT NOT NULL DEFAULT '[]',
    prompt_text TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    evidence_json TEXT NOT NULL DEFAULT '[]',
    author_run_id TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (persona_id, version),
    UNIQUE (revision_id),
    CHECK (json_valid(traits_json)),
    CHECK (json_valid(diff_json)),
    CHECK (json_valid(pinned_traits_json)),
    CHECK (json_valid(evidence_json))
);

CREATE INDEX IF NOT EXISTS idx_persona_versions_updated
    ON persona_versions(persona_id, updated_at DESC, version DESC);
CREATE INDEX IF NOT EXISTS idx_persona_versions_author_run
    ON persona_versions(author_run_id, created_at DESC)
    WHERE author_run_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS persona_heads (
    persona_id TEXT PRIMARY KEY,
    version INTEGER NOT NULL,
    revision_id TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (persona_id, version) REFERENCES persona_versions(persona_id, version) ON DELETE CASCADE,
    FOREIGN KEY (revision_id) REFERENCES persona_versions(revision_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS relationship_versions (
    relationship_id TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    revision_id TEXT NOT NULL,
    parent_id TEXT,
    parent_version INTEGER NOT NULL DEFAULT 0 CHECK (parent_version >= 0),
    operation TEXT NOT NULL DEFAULT 'update',
    dimensions_json TEXT NOT NULL DEFAULT '{}',
    opinions_json TEXT NOT NULL DEFAULT '[]',
    summary TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    evidence_json TEXT NOT NULL DEFAULT '[]',
    author_run_id TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (relationship_id, version),
    UNIQUE (revision_id),
    CHECK (json_valid(dimensions_json)),
    CHECK (json_valid(opinions_json)),
    CHECK (json_valid(evidence_json))
);

CREATE INDEX IF NOT EXISTS idx_relationship_versions_updated
    ON relationship_versions(relationship_id, updated_at DESC, version DESC);
CREATE INDEX IF NOT EXISTS idx_relationship_versions_author_run
    ON relationship_versions(author_run_id, created_at DESC)
    WHERE author_run_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS relationship_heads (
    relationship_id TEXT PRIMARY KEY,
    version INTEGER NOT NULL,
    revision_id TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (relationship_id, version) REFERENCES relationship_versions(relationship_id, version) ON DELETE CASCADE,
    FOREIGN KEY (revision_id) REFERENCES relationship_versions(revision_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS affective_states (
    affect_id TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    revision_id TEXT NOT NULL,
    parent_id TEXT,
    parent_version INTEGER NOT NULL DEFAULT 0 CHECK (parent_version >= 0),
    operation TEXT NOT NULL DEFAULT 'update',
    emotions_json TEXT NOT NULL DEFAULT '{}',
    summary TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    author_run_id TEXT,
    as_of TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (affect_id, version),
    UNIQUE (revision_id),
    CHECK (json_valid(emotions_json))
);

CREATE INDEX IF NOT EXISTS idx_affective_states_updated
    ON affective_states(affect_id, updated_at DESC, version DESC);
CREATE INDEX IF NOT EXISTS idx_affective_states_author_run
    ON affective_states(author_run_id, created_at DESC)
    WHERE author_run_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS affective_heads (
    affect_id TEXT PRIMARY KEY,
    version INTEGER NOT NULL,
    revision_id TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (affect_id, version) REFERENCES affective_states(affect_id, version) ON DELETE CASCADE,
    FOREIGN KEY (revision_id) REFERENCES affective_states(revision_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS affective_events (
    id TEXT PRIMARY KEY,
    affect_id TEXT NOT NULL DEFAULT 'default',
    state_version INTEGER NOT NULL DEFAULT 0 CHECK (state_version >= 0),
    source_id TEXT,
    source_type TEXT NOT NULL DEFAULT '',
    run_id TEXT,
    conversation_id TEXT,
    emotion TEXT NOT NULL,
    intensity REAL NOT NULL CHECK (intensity >= 0 AND intensity <= 1),
    valence REAL NOT NULL CHECK (valence >= -1 AND valence <= 1),
    decay_policy TEXT NOT NULL DEFAULT 'none',
    decay_rate REAL NOT NULL DEFAULT 0 CHECK (decay_rate >= 0),
    half_life_seconds INTEGER NOT NULL DEFAULT 0 CHECK (half_life_seconds >= 0),
    decays_at TEXT,
    provenance TEXT NOT NULL DEFAULT '',
    evidence_json TEXT NOT NULL DEFAULT '[]',
    metadata_json TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    CHECK (json_valid(evidence_json)),
    CHECK (metadata_json = '' OR json_valid(metadata_json))
);

CREATE INDEX IF NOT EXISTS idx_affective_events_affect_created
    ON affective_events(affect_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_affective_events_decay
    ON affective_events(decays_at, created_at)
    WHERE decays_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_affective_events_source
    ON affective_events(source_type, source_id, created_at);

-- Personality state and its security audit entry must commit together. These
-- triggers cover every repository writer (UI curation, reflection, rollback,
-- reset, and future adapters) instead of relying on best-effort application
-- logging after the state transaction has already committed.
CREATE TRIGGER IF NOT EXISTS trg_persona_versions_audit
AFTER INSERT ON persona_versions
BEGIN
    INSERT INTO audit_events(
        id, run_id, actor, action, target, decision, payload_redacted, duration_ms, created_at
    ) VALUES (
        'audit-persona-' || lower(hex(randomblob(16))),
        CASE WHEN NEW.author_run_id IS NOT NULL AND EXISTS(SELECT 1 FROM agent_runs WHERE id = NEW.author_run_id) THEN NEW.author_run_id ELSE NULL END,
		CASE WHEN NEW.operation IN ('pin', 'rollback', 'reset') THEN 'user' ELSE 'system' END,
		'persona.' || NEW.operation, NEW.persona_id, 'allow',
        json_object('profile_id', NEW.persona_id, 'version', NEW.version, 'revision_id', NEW.revision_id),
        0, NEW.updated_at
    );
END;

CREATE TRIGGER IF NOT EXISTS trg_relationship_versions_audit
AFTER INSERT ON relationship_versions
BEGIN
    INSERT INTO audit_events(
        id, run_id, actor, action, target, decision, payload_redacted, duration_ms, created_at
    ) VALUES (
        'audit-relationship-' || lower(hex(randomblob(16))),
        CASE WHEN NEW.author_run_id IS NOT NULL AND EXISTS(SELECT 1 FROM agent_runs WHERE id = NEW.author_run_id) THEN NEW.author_run_id ELSE NULL END,
		CASE WHEN NEW.operation IN ('rollback', 'reset') THEN 'user' ELSE 'system' END,
		'relationship.' || NEW.operation, NEW.relationship_id, 'allow',
        json_object('profile_id', NEW.relationship_id, 'version', NEW.version, 'revision_id', NEW.revision_id),
        0, NEW.updated_at
    );
END;

CREATE TRIGGER IF NOT EXISTS trg_affective_states_audit
AFTER INSERT ON affective_states
BEGIN
    INSERT INTO audit_events(
        id, run_id, actor, action, target, decision, payload_redacted, duration_ms, created_at
    ) VALUES (
        'audit-affect-' || lower(hex(randomblob(16))),
        CASE WHEN NEW.author_run_id IS NOT NULL AND EXISTS(SELECT 1 FROM agent_runs WHERE id = NEW.author_run_id) THEN NEW.author_run_id ELSE NULL END,
		CASE WHEN NEW.operation = 'reset' THEN 'user' ELSE 'system' END,
		'affect.' || NEW.operation, NEW.affect_id, 'allow',
        json_object('profile_id', NEW.affect_id, 'version', NEW.version, 'revision_id', NEW.revision_id),
        0, NEW.updated_at
    );
END;

INSERT OR IGNORE INTO app_metadata(key, value)
VALUES ('persona_schema_generation', 'persona-relationship-affect-v1');
