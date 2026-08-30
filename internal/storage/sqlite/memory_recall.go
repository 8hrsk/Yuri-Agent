package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// RecordRecall records that a memory was read, so decay services can base
// their decisions on durable recall history.
//
// A recall is deliberately not a content revision: it appends nothing to
// memory_versions, copies no sources forward and reindexes nothing. Recording
// it through the append-version path made database size and FTS query time
// grow with the number of reads instead of the number of writes. The counter
// lives in memory_recalls, which holds exactly one bounded row per logical
// memory regardless of how often it is recalled.
//
// The returned memory is the unchanged head revision with the new counters
// applied; its Version is intentionally not incremented, so an optimistic
// Save/AppendVersion issued after a recall still sees the version it read.
func (r *MemoryRepository) RecordRecall(ctx context.Context, id domain.ID, at time.Time) (domain.Memory, error) {
	return r.recordRecall(ctx, domain.ID(""), id, at)
}

func (r *MemoryRepository) RecordRecallForAgent(ctx context.Context, agentID, id domain.ID, at time.Time) (domain.Memory, error) {
	if agentID.Empty() {
		return domain.Memory{}, fmt.Errorf("%w: memory agent id is required", domain.ErrInvalidArgument)
	}
	return r.recordRecall(ctx, agentID, id, at)
}

func (r *MemoryRepository) recordRecall(ctx context.Context, agentID, id domain.ID, at time.Time) (domain.Memory, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.Memory{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.Memory{}, err
	}
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	var current domain.Memory
	var err error
	if agentID.Empty() {
		current, err = r.Get(ctx, id)
	} else {
		current, err = r.GetForAgent(ctx, agentID, id)
	}
	if err != nil {
		return domain.Memory{}, err
	}
	timestamp := formatTime(at)
	// The seed value carries the count already visible on the head revision,
	// so a database migrated from the old touch-revision scheme continues from
	// its historical count instead of restarting at one.
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO memory_recalls(memory_id, access_count, last_accessed_at, last_recalled_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(memory_id) DO UPDATE SET
			access_count = memory_recalls.access_count + 1,
			last_accessed_at = excluded.last_accessed_at,
			last_recalled_at = excluded.last_recalled_at`,
		string(current.ID), current.AccessCount+1, timestamp, timestamp); err != nil {
		return domain.Memory{}, wrappedSQLError("record memory recall", err)
	}
	current.AccessCount++
	current.LastAccessedAt = at
	current.LastRecalledAt = at
	return current, nil
}
