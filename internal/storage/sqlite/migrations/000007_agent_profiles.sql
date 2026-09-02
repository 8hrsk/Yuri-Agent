-- Stage 8 named top-level agents. These profiles are owner-controlled
-- identity seeds; mutable persona, relationship, and affect continue to live
-- in their append-only journals under the same logical ID.

CREATE TABLE IF NOT EXISTS agent_profiles (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    age INTEGER NOT NULL DEFAULT 0 CHECK (age = 0 OR (age >= 1 AND age <= 200)),
    gender TEXT NOT NULL,
    preferences TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (length(trim(name)) BETWEEN 1 AND 64),
    CHECK (length(trim(gender)) BETWEEN 1 AND 64),
    CHECK (length(preferences) <= 2000)
);

CREATE INDEX IF NOT EXISTS idx_agent_profiles_updated
    ON agent_profiles(updated_at DESC, id);

-- Existing single-Yuri installations already have a persona head. Preserve
-- that identity as a backward-compatible roster entry; a clean database has
-- no persona head and therefore still enters the agent creation flow.
INSERT OR IGNORE INTO agent_profiles(id, name, age, gender, preferences, created_at, updated_at)
SELECT ph.persona_id, 'Yuri', 0, 'female', '', ph.updated_at, ph.updated_at
FROM persona_heads AS ph;

INSERT OR IGNORE INTO app_metadata(key, value)
VALUES ('agent_profile_schema_generation', 'named-agents-v1');
