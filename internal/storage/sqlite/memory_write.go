package sqlite

import (
	"context"
	"fmt"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// Create appends version one. An optional source slice can be supplied for
// provenance. The slice is variadic to keep the common no-source candidate
// call concise while still making source attachment atomic with the memory.
func (r *MemoryRepository) Create(ctx context.Context, memory domain.Memory, sources ...[]domain.MemorySource) error {
	return r.createWithMetadata(ctx, memory, nil, sources...)
}

// CreateWithMetadata is the create-side counterpart to
// AppendVersionWithMetadata. The revision parent is always zero for a new
// logical memory.
func (r *MemoryRepository) CreateWithMetadata(ctx context.Context, memory domain.Memory, metadata *MemoryVersionMetadata, sources ...[]domain.MemorySource) error {
	return r.createWithMetadata(ctx, memory, metadata, sources...)
}

func (r *MemoryRepository) createWithMetadata(ctx context.Context, memory domain.Memory, metadata *MemoryVersionMetadata, sources ...[]domain.MemorySource) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	memory = normalizeMemoryForCreate(memory)
	if err := validateMemoryForStorage(memory); err != nil {
		return err
	}
	if metadata != nil && metadata.ParentVersion != 0 {
		return fmt.Errorf("%w: create memory cannot have a parent version", domain.ErrInvalidArgument)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin create memory", err)
	}
	defer tx.Rollback()
	var existing uint64
	err = tx.QueryRowContext(ctx, `SELECT version FROM memory_heads WHERE memory_id = ?`, string(memory.ID)).Scan(&existing)
	if err == nil {
		return domain.ErrConflict
	}
	if !isNoRows(err) {
		return wrappedSQLError("check memory head", err)
	}
	if err := r.appendVersionTx(ctx, tx, memory, firstSources(sources), 0, "create", metadata, nil); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit create memory", err)
	}
	return nil
}

// Save appends the next version. memory.Version is the desired new revision
// and therefore must equal current.Version+1. Sources are optional; existing
// provenance is copied forward atomically before new sources are appended.
func (r *MemoryRepository) Save(ctx context.Context, memory domain.Memory, sources ...[]domain.MemorySource) error {
	return r.saveWithOperation(ctx, memory, "update", sources...)
}

func (r *MemoryRepository) saveWithOperation(ctx context.Context, memory domain.Memory, operation string, sources ...[]domain.MemorySource) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if memory.Version == 0 {
		return fmt.Errorf("%w: memory version must be positive when saving", domain.ErrInvalidArgument)
	}
	if err := validateMemoryForStorage(memory); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin save memory", err)
	}
	defer tx.Rollback()
	currentVersion, err := memoryHeadVersion(ctx, tx, memory.ID)
	if err != nil {
		return err
	}
	if memory.Version != currentVersion+1 {
		return domain.ErrConflict
	}
	if err := ensureMemoryAgentTx(ctx, tx, memory.ID, memory.AgentID); err != nil {
		return err
	}
	if err := r.appendVersionTx(ctx, tx, memory, firstSources(sources), currentVersion, operation, nil, nil); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit save memory", err)
	}
	return nil
}

// SaveVersion is an explicit name for callers that want to emphasize the
// append-only behavior. It is equivalent to Save.
func (r *MemoryRepository) SaveVersion(ctx context.Context, memory domain.Memory, sources []domain.MemorySource) error {
	return r.Save(ctx, memory, sources)
}

// Update is a compatibility alias for Save used by application services that
// call all versioned current projections "updates".
func (r *MemoryRepository) Update(ctx context.Context, memory domain.Memory, sources ...[]domain.MemorySource) error {
	return r.Save(ctx, memory, sources...)
}

// AppendVersion appends a revision after checking expectedVersion. It returns
// the newly stored current memory, which is convenient for autonomous memory
// writes and lifecycle transitions.
func (r *MemoryRepository) AppendVersion(ctx context.Context, memory domain.Memory, expectedVersion uint64, sources ...[]domain.MemorySource) (domain.Memory, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.Memory{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.Memory{}, err
	}
	if memory.Version == 0 {
		return domain.Memory{}, fmt.Errorf("%w: memory version must be positive when appending", domain.ErrInvalidArgument)
	}
	if err := validateMemoryForStorage(memory); err != nil {
		return domain.Memory{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Memory{}, wrappedSQLError("begin append memory", err)
	}
	defer tx.Rollback()
	currentVersion, err := memoryHeadVersion(ctx, tx, memory.ID)
	if err != nil {
		return domain.Memory{}, err
	}
	if expectedVersion != currentVersion || memory.Version != currentVersion+1 {
		return domain.Memory{}, domain.ErrConflict
	}
	if err := ensureMemoryAgentTx(ctx, tx, memory.ID, memory.AgentID); err != nil {
		return domain.Memory{}, err
	}
	if err := r.appendVersionTx(ctx, tx, memory, firstSources(sources), currentVersion, "update", nil, nil); err != nil {
		return domain.Memory{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Memory{}, wrappedSQLError("commit append memory", err)
	}
	return memory, nil
}

// AppendVersionWithMetadata is the atomic application-service boundary for a
// memory change. It is equivalent to AppendVersion while retaining the
// caller's immutable revision ID, operation and reason in the journal.
func (r *MemoryRepository) AppendVersionWithMetadata(ctx context.Context, memory domain.Memory, expectedVersion uint64, metadata MemoryVersionMetadata, sources ...[]domain.MemorySource) (domain.Memory, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.Memory{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.Memory{}, err
	}
	if memory.Version == 0 {
		return domain.Memory{}, fmt.Errorf("%w: memory version must be positive when appending", domain.ErrInvalidArgument)
	}
	if err := validateMemoryForStorage(memory); err != nil {
		return domain.Memory{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Memory{}, wrappedSQLError("begin append memory metadata", err)
	}
	defer tx.Rollback()
	currentVersion, err := memoryHeadVersion(ctx, tx, memory.ID)
	if err != nil {
		return domain.Memory{}, err
	}
	if expectedVersion != currentVersion || memory.Version != currentVersion+1 {
		return domain.Memory{}, domain.ErrConflict
	}
	if metadata.ParentVersion != 0 && metadata.ParentVersion != currentVersion {
		return domain.Memory{}, domain.ErrConflict
	}
	if metadata.ParentVersion == 0 {
		metadata.ParentVersion = currentVersion
	}
	if err := ensureMemoryAgentTx(ctx, tx, memory.ID, memory.AgentID); err != nil {
		return domain.Memory{}, err
	}
	if err := r.appendVersionTx(ctx, tx, memory, firstSources(sources), currentVersion, metadata.Operation, &metadata, nil); err != nil {
		return domain.Memory{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Memory{}, wrappedSQLError("commit append memory metadata", err)
	}
	return memory, nil
}

func firstSources(sources [][]domain.MemorySource) []domain.MemorySource {
	if len(sources) == 0 {
		return nil
	}
	return sources[0]
}
