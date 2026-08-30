package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// Get returns the current revision. Deleted memories are returned as
// tombstones so the caller can explain or restore a user-visible deletion.
func (r *MemoryRepository) Get(ctx context.Context, id domain.ID) (domain.Memory, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.Memory{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.Memory{}, err
	}
	if id.Empty() {
		return domain.Memory{}, fmt.Errorf("%w: memory id is required", domain.ErrInvalidArgument)
	}
	return getCurrentMemory(ctx, r.db, id)
}

// GetForAgent returns a memory only when it belongs to the requested agent.
// Ownership is stable across private/shared scope changes.
// This scoped variant is used by the desktop adapter; the unscoped method is
// retained for migrations and administrative tooling.
func (r *MemoryRepository) GetForAgent(ctx context.Context, agentID, id domain.ID) (domain.Memory, error) {
	if agentID.Empty() {
		return domain.Memory{}, fmt.Errorf("%w: memory agent id is required", domain.ErrInvalidArgument)
	}
	if err := requireDatabase(r.db); err != nil {
		return domain.Memory{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.Memory{}, err
	}
	return getCurrentMemoryForAgent(ctx, r.db, agentID, id)
}

// GetVersion returns an immutable journal revision rather than the current
// projection. It is used by provenance viewers and rollback tooling.
func (r *MemoryRepository) GetVersion(ctx context.Context, id domain.ID, version uint64) (domain.Memory, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.Memory{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.Memory{}, err
	}
	if id.Empty() || version == 0 {
		return domain.Memory{}, fmt.Errorf("%w: memory id and positive version are required", domain.ErrInvalidArgument)
	}
	return getMemoryVersion(ctx, r.db, id, version)
}

// GetVersionRecord returns an immutable snapshot together with journal
// metadata used for audit, rollback and reflection explainability.
func (r *MemoryRepository) GetVersionRecord(ctx context.Context, id domain.ID, version uint64) (MemoryVersionRecord, error) {
	if err := requireDatabase(r.db); err != nil {
		return MemoryVersionRecord{}, err
	}
	if err := contextErr(ctx); err != nil {
		return MemoryVersionRecord{}, err
	}
	if id.Empty() || version == 0 {
		return MemoryVersionRecord{}, fmt.Errorf("%w: memory id and positive version are required", domain.ErrInvalidArgument)
	}
	row := r.db.QueryRowContext(ctx, memoryVersionSelect+`
		WHERE mv.memory_id = ? AND mv.version = ?`, string(id), version)
	record, err := scanMemoryVersionRecord(row)
	if err != nil {
		return MemoryVersionRecord{}, wrappedSQLError("get memory version record", err)
	}
	return record, nil
}

// ListVersions returns immutable revisions newest first. A positive limit
// bounds the result; zero or omitted means all revisions.
func (r *MemoryRepository) ListVersions(ctx context.Context, id domain.ID, limit ...int) ([]MemoryVersionRecord, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if id.Empty() {
		return nil, fmt.Errorf("%w: memory id is required", domain.ErrInvalidArgument)
	}
	if len(limit) > 0 && limit[0] < 0 {
		return nil, fmt.Errorf("%w: memory version limit cannot be negative", domain.ErrInvalidArgument)
	}
	query := memoryVersionSelect + `
		WHERE mv.memory_id = ?
		ORDER BY mv.version DESC`
	args := []any{string(id)}
	if len(limit) > 0 && limit[0] > 0 {
		query += " LIMIT ?"
		args = append(args, limit[0])
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list memory versions", err)
	}
	defer rows.Close()
	result := make([]MemoryVersionRecord, 0)
	for rows.Next() {
		record, err := scanMemoryVersionRecord(rows)
		if err != nil {
			return nil, wrappedSQLError("scan memory version", err)
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate memory versions", err)
	}
	return result, nil
}

// ListMemoryVersions is an architectural alias for ListVersions.
func (r *MemoryRepository) ListMemoryVersions(ctx context.Context, id domain.ID, limit ...int) ([]MemoryVersionRecord, error) {
	return r.ListVersions(ctx, id, limit...)
}

func (r *MemoryRepository) ListVersionsForAgent(ctx context.Context, agentID, id domain.ID, limit ...int) ([]MemoryVersionRecord, error) {
	item, err := r.GetForAgent(ctx, agentID, id)
	if err != nil {
		return nil, err
	}
	versions, err := r.ListVersions(ctx, id, limit...)
	if err != nil {
		return nil, err
	}
	for index := range versions {
		if versions[index].Memory.AgentID != item.AgentID || versions[index].Memory.Scope != item.Scope {
			return nil, domain.ErrConflict
		}
	}
	return versions, nil
}

// GetCurrent is an explicit alias for Get for context assemblers that need to
// distinguish the current projection from an immutable journal revision.
func (r *MemoryRepository) GetCurrent(ctx context.Context, id domain.ID) (domain.Memory, error) {
	return r.Get(ctx, id)
}

// FindByCanonicalKey returns the current non-deleted memory used for
// deduplication. Empty keys are rejected because an empty key would collapse
// unrelated candidates into one record.
func (r *MemoryRepository) FindByCanonicalKey(ctx context.Context, key string, includeDormant ...bool) (domain.Memory, error) {
	return r.findByCanonicalKey(ctx, domain.ID(""), key, includeDormant...)
}

func (r *MemoryRepository) FindByCanonicalKeyForAgent(ctx context.Context, agentID domain.ID, key string, includeDormant ...bool) (domain.Memory, error) {
	if agentID.Empty() {
		return domain.Memory{}, fmt.Errorf("%w: memory agent id is required", domain.ErrInvalidArgument)
	}
	return r.findByCanonicalKey(ctx, agentID, key, includeDormant...)
}

func (r *MemoryRepository) findByCanonicalKey(ctx context.Context, agentID domain.ID, key string, includeDormant ...bool) (domain.Memory, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.Memory{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.Memory{}, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return domain.Memory{}, fmt.Errorf("%w: canonical key is required", domain.ErrInvalidArgument)
	}
	dormant := len(includeDormant) > 0 && includeDormant[0]
	state := "mv.lifecycle_state = 'active'"
	if dormant {
		state = "mv.lifecycle_state IN ('active', 'dormant')"
	}
	whereAgent := ""
	args := []any{key}
	if !agentID.Empty() {
		whereAgent = " AND mv.agent_id = ? AND mv.scope = 'agent_private'"
		args = append(args, string(agentID))
	}
	row := r.db.QueryRowContext(ctx, memoryHeadSelectPrefix+`
		FROM memory_heads AS mh
		JOIN memory_versions AS mv ON mv.memory_id = mh.memory_id AND mv.version = mh.version`+memoryRecallJoin+`
		WHERE mv.canonical_key = ? AND `+state+whereAgent+`
		ORDER BY mv.salience DESC, mv.updated_at DESC, mv.memory_id ASC
		LIMIT 1`, args...)
	item, err := scanMemory(row)
	if err != nil {
		return domain.Memory{}, wrappedSQLError("find memory by canonical key", err)
	}
	return item, nil
}
