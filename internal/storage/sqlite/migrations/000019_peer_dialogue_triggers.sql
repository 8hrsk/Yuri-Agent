-- Explainable provenance for Stage 8 autonomous peer-dialogue triggers.
-- Existing dialogues were explicitly requested by agent.talk_to_peer.

ALTER TABLE peer_dialogues ADD COLUMN trigger_kind TEXT NOT NULL DEFAULT 'agent_tool'
    CHECK (trigger_kind IN ('agent_tool', 'autonomous'));
ALTER TABLE peer_dialogues ADD COLUMN trigger_reason TEXT NOT NULL DEFAULT 'Агент явно запросил консультацию peer через tool.'
    CHECK (length(trim(trigger_reason)) BETWEEN 1 AND 512);

CREATE INDEX IF NOT EXISTS idx_peer_dialogues_trigger_kind_created
    ON peer_dialogues(trigger_kind, initiator_agent_id, created_at DESC, id DESC);

CREATE TRIGGER IF NOT EXISTS peer_dialogues_trigger_provenance_immutable
BEFORE UPDATE OF trigger_kind, trigger_reason ON peer_dialogues
WHEN NEW.trigger_kind <> OLD.trigger_kind OR NEW.trigger_reason <> OLD.trigger_reason
BEGIN
    SELECT RAISE(ABORT, 'peer dialogue trigger provenance is immutable');
END;
