-- Recall telemetry and memory search projection hygiene.
--
-- A recall ("touch") is not a content revision. Before this migration every
-- RecordRecall went through the full append-version path: a complete copy of
-- content_text/content_json/summary was appended to memory_versions, every
-- source of the previous revision was copied into memory_sources, and one more
-- full copy of the content was appended to memory_fts, which never dropped old
-- revisions. Database size and FTS query time therefore grew with the number
-- of *reads* rather than the number of writes.
--
-- memory_recalls is a bounded, monotonic access counter: exactly one row per
-- logical memory, never one row per recall. It is authoritative access
-- telemetry in its own right, not a projection of memory_versions, so
-- RebuildProjections must never clear it.
--
-- No FOREIGN KEY on memory_id is intentional: memory_heads is a rebuildable
-- projection that RebuildProjections deletes and re-inserts, and a cascade
-- would silently destroy recall history during an ordinary index repair.
CREATE TABLE IF NOT EXISTS memory_recalls (
    memory_id TEXT PRIMARY KEY,
    access_count INTEGER NOT NULL DEFAULT 0 CHECK (access_count >= 0),
    last_accessed_at TEXT NOT NULL,
    last_recalled_at TEXT NOT NULL
);

-- Carry the recall history that the old touch revisions accumulated over into
-- the counter, so existing installs keep their access counts and recency.
-- The journal itself is left untouched: those revisions are recorded history.
INSERT OR IGNORE INTO memory_recalls(memory_id, access_count, last_accessed_at, last_recalled_at)
SELECT mv.memory_id,
       mv.access_count,
       COALESCE(mv.last_accessed_at, mv.last_recalled_at, mv.updated_at),
       COALESCE(mv.last_recalled_at, mv.last_accessed_at, mv.updated_at)
FROM memory_versions AS mv
JOIN memory_heads AS mh ON mh.memory_id = mv.memory_id AND mh.version = mv.version
WHERE mv.access_count > 0
   OR mv.last_accessed_at IS NOT NULL
   OR mv.last_recalled_at IS NOT NULL;

-- memory_fts is a pure projection of memory_versions and is fully rebuildable
-- from it. Dead revisions were never deleted, so the index held one row per
-- revision, including one per recall: every MATCH scanned the postings of
-- every superseded copy and bm25 was computed over an inflated index. Keep
-- exactly one row per live memory. Superseded rows carry no information that
-- memory_versions does not already hold.
DELETE FROM memory_fts
WHERE NOT EXISTS (
    SELECT 1 FROM memory_heads AS mh
    WHERE mh.memory_id = memory_fts.memory_id
      AND mh.version = CAST(memory_fts.memory_version AS INTEGER)
);

-- Covering index for the List/ListCore sort key
-- (pinned, salience, confidence, updated_at, memory_id) under the agent/scope
-- and lifecycle predicates. Without it every core-prefix assembly sorted the
-- whole active corpus.
CREATE INDEX IF NOT EXISTS idx_memory_versions_agent_core
    ON memory_versions(
        agent_id, scope, lifecycle_state,
        pinned DESC, salience DESC, confidence DESC, updated_at DESC, memory_id
    );
