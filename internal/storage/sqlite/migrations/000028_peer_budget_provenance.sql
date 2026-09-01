-- Durable provenance for owner-applied peer budget recommendations. Existing
-- exchanges predate the owner form and remain agent_default with no snapshot.
ALTER TABLE peer_dialogues
ADD COLUMN budget_origin TEXT NOT NULL DEFAULT 'agent_default'
CHECK (budget_origin IN ('agent_default', 'owner_custom', 'owner_recommendation'));

ALTER TABLE peer_dialogues
ADD COLUMN recommendation_basis TEXT NOT NULL DEFAULT ''
CHECK (recommendation_basis IN ('', 'purpose_only', 'pair_history', 'similar_history'));

ALTER TABLE peer_dialogues
ADD COLUMN recommendation_sample_count INTEGER NOT NULL DEFAULT 0
CHECK (recommendation_sample_count BETWEEN 0 AND 8);

ALTER TABLE peer_dialogues
ADD COLUMN recommended_min_turns INTEGER NOT NULL DEFAULT 0
CHECK (recommended_min_turns BETWEEN 0 AND 8);

ALTER TABLE peer_dialogues
ADD COLUMN recommended_max_turns INTEGER NOT NULL DEFAULT 0
CHECK (recommended_max_turns BETWEEN 0 AND 8);

ALTER TABLE peer_dialogues
ADD COLUMN recommended_max_tokens INTEGER NOT NULL DEFAULT 0
CHECK (recommended_max_tokens BETWEEN 0 AND 16000);

ALTER TABLE peer_dialogues
ADD COLUMN recommended_max_duration_seconds INTEGER NOT NULL DEFAULT 0
CHECK (recommended_max_duration_seconds BETWEEN 0 AND 300);

CREATE INDEX IF NOT EXISTS idx_peer_dialogues_budget_origin_finished
    ON peer_dialogues(budget_origin, finished_at DESC, id DESC);
