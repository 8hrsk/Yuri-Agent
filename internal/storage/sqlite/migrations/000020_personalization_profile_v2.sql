-- Personalization Profile v2 is the immutable owner-authored baseline used by
-- the future Personality Compiler and creation flow. It is intentionally
-- separate from mutable persona, relationship, and affect journals.

CREATE TABLE IF NOT EXISTS personalization_seed_versions (
    agent_id TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    revision_id TEXT NOT NULL UNIQUE,
    parent_id TEXT,
    parent_version INTEGER NOT NULL DEFAULT 0 CHECK (parent_version >= 0),
    schema_version INTEGER NOT NULL CHECK (schema_version = 2),
    operation TEXT NOT NULL CHECK (operation IN ('create', 'update', 'reset', 'migration')),
    identity_json TEXT NOT NULL CHECK (json_valid(identity_json)),
    communication_style_json TEXT NOT NULL CHECK (json_valid(communication_style_json)),
    temperament_json TEXT NOT NULL CHECK (json_valid(temperament_json)),
    emotional_dynamics_json TEXT NOT NULL CHECK (json_valid(emotional_dynamics_json)),
    relationship_seed_json TEXT NOT NULL CHECK (json_valid(relationship_seed_json)),
    backstory_json TEXT NOT NULL CHECK (json_valid(backstory_json)),
    evolution_policy_json TEXT NOT NULL CHECK (json_valid(evolution_policy_json)),
    reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (agent_id, version),
    FOREIGN KEY (agent_id) REFERENCES agent_profiles(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_id) REFERENCES personalization_seed_versions(revision_id) DEFERRABLE INITIALLY DEFERRED,
    CHECK ((version = 1 AND parent_version = 0 AND parent_id IS NULL) OR (version > 1 AND parent_version = version - 1 AND parent_id IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_personalization_seed_versions_updated
    ON personalization_seed_versions(agent_id, updated_at DESC, version DESC);

CREATE TABLE IF NOT EXISTS personalization_seed_heads (
    agent_id TEXT PRIMARY KEY,
    version INTEGER NOT NULL,
    revision_id TEXT NOT NULL UNIQUE,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (agent_id, version) REFERENCES personalization_seed_versions(agent_id, version) ON DELETE CASCADE,
    FOREIGN KEY (revision_id) REFERENCES personalization_seed_versions(revision_id) ON DELETE CASCADE
);

-- Existing installations keep their current persona values. The new owner
-- seed is not used by the runtime yet, so this migration cannot change current
-- behavior. Custom safe traits are retained under temperament.custom.
WITH legacy AS (
    SELECT
        ap.id,
        ap.preferences,
        ap.backstory,
        ap.created_at,
        ap.updated_at,
        COALESCE((
            SELECT pv.traits_json
            FROM persona_heads AS ph
            JOIN persona_versions AS pv ON pv.persona_id = ph.persona_id AND pv.version = ph.version
            WHERE ph.persona_id = ap.id
        ), '{}') AS traits_json,
        COALESCE((
            SELECT rv.dimensions_json
            FROM relationship_versions AS rv
            WHERE rv.relationship_id = ap.id
            ORDER BY rv.version ASC
            LIMIT 1
        ), '{"trust":0.35,"attachment":0.25,"respect":0.50,"closeness":0.20,"reliability":0.40,"irritation":0,"jealousy":0,"resentment":0,"gratitude":0.15}') AS relationship_json
    FROM agent_profiles AS ap
)
INSERT OR IGNORE INTO personalization_seed_versions(
    agent_id, version, revision_id, parent_id, parent_version, schema_version, operation,
    identity_json, communication_style_json, temperament_json, emotional_dynamics_json,
    relationship_seed_json, backstory_json, evolution_policy_json, reason, created_at, updated_at
)
SELECT
    id,
    1,
    id || ':personalization:v1',
    NULL,
    0,
    2,
    'migration',
    json_object(
        'preferred_language', 'ru-RU',
        'pronouns', '',
        'user_address', '',
        'self_description', preferences,
        'role', ''
    ),
    json_object(
        'verbosity', 0.55,
        'softness', COALESCE(json_extract(traits_json, '$.warmth'), 0.58),
        'humor', COALESCE(json_extract(traits_json, '$.playfulness'), 0.55),
        'figurativeness', 0.35,
        'expressiveness', COALESCE(json_extract(traits_json, '$.emotionality'), 0.62),
        'supportiveness', COALESCE(json_extract(traits_json, '$.empathy'), 0.72),
        'formality', COALESCE(json_extract(traits_json, '$.formality'), 0.20),
        'teasing', COALESCE(json_extract(traits_json, '$.tsundere'), 0.52),
        'emoji_frequency', 0.10,
        'flirtation', COALESCE(json_extract(traits_json, '$.romantic_tone'), 0.25),
        'conversational_initiative', COALESCE(json_extract(traits_json, '$.initiative'), 0.48)
    ),
    json_object(
        'warmth', COALESCE(json_extract(traits_json, '$.warmth'), 0.58),
        'directness', COALESCE(json_extract(traits_json, '$.directness'), 0.72),
        'emotionality', COALESCE(json_extract(traits_json, '$.emotionality'), 0.62),
        'playfulness', COALESCE(json_extract(traits_json, '$.playfulness'), 0.55),
        'jealousy', COALESCE(json_extract(traits_json, '$.jealousy'), 0.20),
        'irritability', COALESCE(json_extract(traits_json, '$.irritability'), 0.18),
        'empathy', COALESCE(json_extract(traits_json, '$.empathy'), 0.72),
        'sociability', COALESCE(json_extract(traits_json, '$.sociability'), 0.48),
        'shyness', COALESCE(json_extract(traits_json, '$.shyness'), 0.34),
        'anxiety', COALESCE(json_extract(traits_json, '$.anxiety'), 0.22),
        'fearfulness', COALESCE(json_extract(traits_json, '$.fearfulness'), 0.18),
        'emotional_stability', COALESCE(json_extract(traits_json, '$.emotional_stability'), 0.64),
        'sensitivity', COALESCE(json_extract(traits_json, '$.sensitivity'), 0.58),
        'possessiveness', COALESCE(json_extract(traits_json, '$.possessiveness'), 0.16),
        'romantic_tone', COALESCE(json_extract(traits_json, '$.romantic_tone'), 0.25),
        'initiative', COALESCE(json_extract(traits_json, '$.initiative'), 0.48),
        'impulsivity', COALESCE(json_extract(traits_json, '$.impulsivity'), 0.22),
        'stubbornness', COALESCE(json_extract(traits_json, '$.stubbornness'), 0.38),
        'optimism', COALESCE(json_extract(traits_json, '$.optimism'), 0.58),
        'curiosity', COALESCE(json_extract(traits_json, '$.curiosity'), 0.72),
        'suspicion', COALESCE(json_extract(traits_json, '$.suspicion'), 0.18),
        'trust', COALESCE(json_extract(traits_json, '$.trust'), 0.45),
        'attachment', COALESCE(json_extract(traits_json, '$.attachment'), 0.35),
        'formality', COALESCE(json_extract(traits_json, '$.formality'), 0.20),
        'tsundere', COALESCE(json_extract(traits_json, '$.tsundere'), 0.52),
        'custom', json(json_remove(
            traits_json,
            '$.warmth', '$.directness', '$.emotionality', '$.playfulness', '$.jealousy', '$.irritability',
            '$.empathy', '$.sociability', '$.shyness', '$.anxiety', '$.fearfulness', '$.emotional_stability',
            '$.sensitivity', '$.possessiveness', '$.romantic_tone', '$.initiative', '$.impulsivity', '$.stubbornness',
            '$.optimism', '$.curiosity', '$.suspicion', '$.trust', '$.attachment', '$.formality', '$.tsundere'
        ))
    ),
    json_object(
        'reactivity', COALESCE(json_extract(traits_json, '$.sensitivity'), 0.58),
        'response_intensity', COALESCE(json_extract(traits_json, '$.emotionality'), 0.62),
        'recovery_speed', COALESCE(json_extract(traits_json, '$.emotional_stability'), 0.64),
        'positive_persistence', 0.50,
        'negative_persistence', 1.0 - COALESCE(json_extract(traits_json, '$.emotional_stability'), 0.64),
        'expression', COALESCE(json_extract(traits_json, '$.emotionality'), 0.62),
        'masking', 0.25,
        'conflict_style', 'adaptive',
        'triggers', json('{}'),
        'soothing_strategies', json('[]')
    ),
    json_object(
        'preset', 'new_acquaintances',
        'dimensions', json(relationship_json),
        'summary', 'Связь только начинает формироваться.'
    ),
    json_object(
        'narrative', backstory,
        'summary', '',
        'episodes', json('[]')
    ),
    json_object(
        'locked_fields', json_array('identity', 'backstory'),
        'trait_bounds', json_object(
            'warmth', json('{"min":0,"max":1}'),
            'directness', json('{"min":0,"max":1}'),
            'emotionality', json('{"min":0,"max":1}'),
            'playfulness', json('{"min":0,"max":1}'),
            'jealousy', json('{"min":0,"max":1}'),
            'irritability', json('{"min":0,"max":1}'),
            'empathy', json('{"min":0,"max":1}'),
            'sociability', json('{"min":0,"max":1}'),
            'shyness', json('{"min":0,"max":1}'),
            'anxiety', json('{"min":0,"max":1}'),
            'fearfulness', json('{"min":0,"max":1}'),
            'emotional_stability', json('{"min":0,"max":1}'),
            'sensitivity', json('{"min":0,"max":1}'),
            'possessiveness', json('{"min":0,"max":1}'),
            'romantic_tone', json('{"min":0,"max":1}'),
            'initiative', json('{"min":0,"max":1}'),
            'impulsivity', json('{"min":0,"max":1}'),
            'stubbornness', json('{"min":0,"max":1}'),
            'optimism', json('{"min":0,"max":1}'),
            'curiosity', json('{"min":0,"max":1}'),
            'suspicion', json('{"min":0,"max":1}'),
            'trust', json('{"min":0,"max":1}'),
            'attachment', json('{"min":0,"max":1}'),
            'formality', json('{"min":0,"max":1}'),
            'tsundere', json('{"min":0,"max":1}')
        )
    ),
    'imported from existing agent profile',
    created_at,
    updated_at
FROM legacy;

INSERT OR IGNORE INTO personalization_seed_heads(agent_id, version, revision_id, updated_at)
SELECT agent_id, version, revision_id, updated_at
FROM personalization_seed_versions
WHERE version = 1;

INSERT OR REPLACE INTO app_metadata(key, value)
VALUES ('personalization_schema_generation', 'personalization-profile-v2');
