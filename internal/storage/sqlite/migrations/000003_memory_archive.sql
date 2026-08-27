-- Stage 2 durable memory journal and archive search projections.
-- memory_versions is authoritative and append-only. memory_heads is a
-- rebuildable projection that points at the current revision of each logical
-- memory. FTS tables are projections and can always be rebuilt from SQLite.

CREATE TABLE IF NOT EXISTS memory_versions (
    memory_id TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    revision_id TEXT NOT NULL,
    operation TEXT NOT NULL DEFAULT 'update',
    parent_version INTEGER NOT NULL DEFAULT 0 CHECK (parent_version >= 0),
    kind TEXT NOT NULL,
    nature TEXT NOT NULL,
    content_text TEXT NOT NULL,
    content_json TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    confidence REAL NOT NULL DEFAULT 0.5 CHECK (confidence >= 0 AND confidence <= 1),
    salience REAL NOT NULL DEFAULT 0.5 CHECK (salience >= 0 AND salience <= 1),
    valence REAL NOT NULL DEFAULT 0 CHECK (valence >= -1 AND valence <= 1),
    sensitivity TEXT NOT NULL DEFAULT 'private',
    retention_policy TEXT NOT NULL DEFAULT 'decay',
    lifecycle_state TEXT NOT NULL DEFAULT 'active',
    pinned INTEGER NOT NULL DEFAULT 0 CHECK (pinned IN (0, 1)),
    hidden_from_core INTEGER NOT NULL DEFAULT 0 CHECK (hidden_from_core IN (0, 1)),
    canonical_key TEXT NOT NULL DEFAULT '',
    embedding_version TEXT NOT NULL DEFAULT '',
    access_count INTEGER NOT NULL DEFAULT 0 CHECK (access_count >= 0),
    last_accessed_at TEXT,
    last_recalled_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    dormant_at TEXT,
    deleted_at TEXT,
    reason TEXT NOT NULL DEFAULT '',
    source_run_id TEXT,
    source_conversation_id TEXT,
    source_message_id TEXT,
    PRIMARY KEY (memory_id, version),
    UNIQUE (revision_id),
    FOREIGN KEY (source_conversation_id) REFERENCES conversations(id) ON DELETE SET NULL,
    FOREIGN KEY (source_message_id) REFERENCES messages(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_memory_versions_updated
    ON memory_versions(updated_at DESC, memory_id, version DESC);
CREATE INDEX IF NOT EXISTS idx_memory_versions_kind_state
    ON memory_versions(kind, lifecycle_state, salience DESC, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_memory_versions_canonical_key
    ON memory_versions(canonical_key, lifecycle_state, updated_at DESC)
    WHERE canonical_key <> '';

CREATE TABLE IF NOT EXISTS memory_heads (
    memory_id TEXT PRIMARY KEY,
    version INTEGER NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (memory_id, version) REFERENCES memory_versions(memory_id, version) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_memory_heads_updated
    ON memory_heads(updated_at DESC, memory_id);

CREATE TABLE IF NOT EXISTS memory_sources (
    id TEXT PRIMARY KEY,
    memory_id TEXT NOT NULL,
    memory_version INTEGER NOT NULL,
    source_type TEXT NOT NULL,
    source_id TEXT,
    run_id TEXT,
    conversation_id TEXT,
    message_id TEXT,
    excerpt_hash TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    FOREIGN KEY (memory_id, memory_version) REFERENCES memory_versions(memory_id, version) ON DELETE CASCADE,
    FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE SET NULL,
    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE SET NULL,
    UNIQUE (memory_id, memory_version, source_type, source_id, conversation_id, message_id, excerpt_hash)
);

CREATE INDEX IF NOT EXISTS idx_memory_sources_memory
    ON memory_sources(memory_id, memory_version, created_at, id);
CREATE INDEX IF NOT EXISTS idx_memory_sources_conversation
    ON memory_sources(conversation_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_memory_sources_message
    ON memory_sources(message_id, created_at, id);

-- FTS5 is intentionally a standalone projection. Message IDs and memory
-- revision identifiers are UNINDEXED columns used to resolve provenance after
-- a hit. Search code filters memory_fts through memory_heads, so old revisions
-- remain harmless and can be used for forensic rebuilds if required.
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    message_id UNINDEXED,
    conversation_id UNINDEXED,
    role UNINDEXED,
    content,
    created_at UNINDEXED,
    tokenize = 'unicode61'
);

CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
    memory_id UNINDEXED,
    memory_version UNINDEXED,
    kind UNINDEXED,
    nature UNINDEXED,
    content,
    summary,
    tokenize = 'unicode61'
);

-- Messages are immutable at the domain boundary, but triggers keep the
-- projection correct for imports and repair tooling as well.
CREATE TRIGGER IF NOT EXISTS messages_fts_ai AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(message_id, conversation_id, role, content, created_at)
    VALUES (new.id, new.conversation_id, new.role, new.content, new.created_at);
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_ad AFTER DELETE ON messages BEGIN
    DELETE FROM messages_fts WHERE message_id = old.id;
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_au AFTER UPDATE ON messages BEGIN
    DELETE FROM messages_fts WHERE message_id = old.id;
    INSERT INTO messages_fts(message_id, conversation_id, role, content, created_at)
    VALUES (new.id, new.conversation_id, new.role, new.content, new.created_at);
END;

-- Populate the archive projection for databases upgraded from Stage 1.
INSERT INTO messages_fts(message_id, conversation_id, role, content, created_at)
SELECT m.id, m.conversation_id, m.role, m.content, m.created_at
FROM messages AS m
WHERE NOT EXISTS (
    SELECT 1 FROM messages_fts AS f WHERE f.message_id = m.id
);

INSERT OR IGNORE INTO app_metadata(key, value) VALUES ('memory_schema_generation', 'memory-archive-v1');
