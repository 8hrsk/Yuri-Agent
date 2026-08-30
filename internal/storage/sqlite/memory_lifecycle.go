package sqlite

import (
	"context"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// MarkDormant appends a lifecycle revision that is excluded from ordinary
// retrieval. A deliberate search can request IncludeDormant/Deliberate.
func (r *MemoryRepository) MarkDormant(ctx context.Context, id domain.ID, expectedVersion uint64, at time.Time, reason string) (domain.Memory, error) {
	return r.transitionLifecycle(ctx, id, expectedVersion, domain.MemoryLifecycleDormant, at, reason)
}

func (r *MemoryRepository) MarkDormantForAgent(ctx context.Context, agentID, id domain.ID, expectedVersion uint64, at time.Time, reason string) (domain.Memory, error) {
	return r.transitionLifecycleForAgent(ctx, agentID, id, expectedVersion, domain.MemoryLifecycleDormant, at, reason)
}

// Restore appends an active revision for a dormant or deleted memory while
// retaining the old state in the immutable journal.
func (r *MemoryRepository) Restore(ctx context.Context, id domain.ID, expectedVersion uint64, at time.Time, reason string) (domain.Memory, error) {
	return r.transitionLifecycle(ctx, id, expectedVersion, domain.MemoryLifecycleActive, at, reason)
}

func (r *MemoryRepository) RestoreForAgent(ctx context.Context, agentID, id domain.ID, expectedVersion uint64, at time.Time, reason string) (domain.Memory, error) {
	return r.transitionLifecycleForAgent(ctx, agentID, id, expectedVersion, domain.MemoryLifecycleActive, at, reason)
}

// SoftDelete creates a tombstone revision. Original conversation messages and
// evidence rows from prior revisions are never physically removed here.
func (r *MemoryRepository) SoftDelete(ctx context.Context, id domain.ID, expectedVersion uint64, at time.Time, reason string) (domain.Memory, error) {
	return r.transitionLifecycle(ctx, id, expectedVersion, domain.MemoryLifecycleDeleted, at, reason)
}

func (r *MemoryRepository) SoftDeleteForAgent(ctx context.Context, agentID, id domain.ID, expectedVersion uint64, at time.Time, reason string) (domain.Memory, error) {
	return r.transitionLifecycleForAgent(ctx, agentID, id, expectedVersion, domain.MemoryLifecycleDeleted, at, reason)
}

// Forget is the user-facing alias for SoftDelete.
func (r *MemoryRepository) Forget(ctx context.Context, id domain.ID, expectedVersion uint64, at time.Time, reason string) (domain.Memory, error) {
	return r.SoftDelete(ctx, id, expectedVersion, at, reason)
}

// Pin appends a revision that changes explicit core curation without mutating
// a previous version.
func (r *MemoryRepository) Pin(ctx context.Context, id domain.ID, expectedVersion uint64, pinned bool, at time.Time, reason string) (domain.Memory, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	current, err := r.Get(ctx, id)
	if err != nil {
		return domain.Memory{}, err
	}
	if expectedVersion != current.Version {
		return domain.Memory{}, domain.ErrConflict
	}
	current.Version++
	current.Pinned = pinned
	current.UpdatedAt = at.UTC()
	current.Reason = reason
	if err := r.saveWithOperation(ctx, current, "pin"); err != nil {
		return domain.Memory{}, err
	}
	return current, nil
}

// HideFromCore changes only the core inclusion flag and keeps the complete
// record available to deliberate search and the memory UI.
func (r *MemoryRepository) HideFromCore(ctx context.Context, id domain.ID, expectedVersion uint64, hidden bool, at time.Time, reason string) (domain.Memory, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	current, err := r.Get(ctx, id)
	if err != nil {
		return domain.Memory{}, err
	}
	if expectedVersion != current.Version {
		return domain.Memory{}, domain.ErrConflict
	}
	current.Version++
	current.HiddenFromCore = hidden
	current.UpdatedAt = at.UTC()
	current.Reason = reason
	if err := r.saveWithOperation(ctx, current, "hide"); err != nil {
		return domain.Memory{}, err
	}
	return current, nil
}
