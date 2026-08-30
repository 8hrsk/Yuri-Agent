CREATE TABLE IF NOT EXISTS agent_peer_relationships (
    observer_agent_id TEXT NOT NULL,
    subject_agent_id TEXT NOT NULL,
    relationship_id TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (observer_agent_id, subject_agent_id),
    CHECK (observer_agent_id <> subject_agent_id),
    FOREIGN KEY (observer_agent_id) REFERENCES agent_profiles(id) ON DELETE CASCADE,
    FOREIGN KEY (subject_agent_id) REFERENCES agent_profiles(id) ON DELETE CASCADE,
    FOREIGN KEY (relationship_id) REFERENCES relationship_heads(relationship_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_agent_peer_relationships_subject
    ON agent_peer_relationships(subject_agent_id, observer_agent_id);

-- One terminal record per observer/dialogue is the durable idempotency marker.
-- It is inserted in the same transaction as relationship and affect revisions.
CREATE TABLE IF NOT EXISTS peer_social_reflections (
    dialogue_id TEXT NOT NULL,
    observer_agent_id TEXT NOT NULL,
    subject_agent_id TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('no_change', 'changed')),
    reason TEXT NOT NULL DEFAULT '',
    relationship_version INTEGER NOT NULL DEFAULT 0 CHECK (relationship_version >= 0),
    affect_version INTEGER NOT NULL DEFAULT 0 CHECK (affect_version >= 0),
    created_at TEXT NOT NULL,
    PRIMARY KEY (dialogue_id, observer_agent_id),
    CHECK (observer_agent_id <> subject_agent_id),
    FOREIGN KEY (dialogue_id) REFERENCES peer_dialogues(id) ON DELETE CASCADE,
    FOREIGN KEY (observer_agent_id, subject_agent_id)
        REFERENCES agent_peer_relationships(observer_agent_id, subject_agent_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_peer_social_reflections_observer
    ON peer_social_reflections(observer_agent_id, created_at DESC);
