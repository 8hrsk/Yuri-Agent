package sqlite

import (
	"context"
	"fmt"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func (r *AffectiveRepository) Get(ctx context.Context, id domain.ID) (domain.AffectiveState, error) {
	return r.GetState(ctx, id)
}

func (r *AffectiveRepository) GetState(ctx context.Context, id domain.ID) (domain.AffectiveState, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.AffectiveState{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.AffectiveState{}, err
	}
	if id.Empty() {
		return domain.AffectiveState{}, fmt.Errorf("%w: affect state id is required", domain.ErrInvalidArgument)
	}
	return getAffectVersion(ctx, r.db, id, 0)
}

func (r *AffectiveRepository) GetCurrent(ctx context.Context, id domain.ID) (domain.AffectiveState, error) {
	return r.GetState(ctx, id)
}

func (r *AffectiveRepository) Current(ctx context.Context, id domain.ID) (domain.AffectiveState, error) {
	return r.GetState(ctx, id)
}

func (r *AffectiveRepository) GetVersion(ctx context.Context, id domain.ID, version uint64) (domain.AffectiveState, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.AffectiveState{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.AffectiveState{}, err
	}
	if id.Empty() || version == 0 {
		return domain.AffectiveState{}, fmt.Errorf("%w: affect state id and positive version are required", domain.ErrInvalidArgument)
	}
	return getAffectVersion(ctx, r.db, id, version)
}

func (r *AffectiveRepository) GetVersionRecord(ctx context.Context, id domain.ID, version uint64) (AffectiveVersionRecord, error) {
	state, err := r.GetVersion(ctx, id, version)
	if err != nil {
		return AffectiveVersionRecord{}, err
	}
	return affectVersionRecord(state), nil
}

// ListVersions reads each revision in full in one query, newest first, the
// way listing.go documents: the package-level rowScanner lets scanAffect serve
// both the single-row get and this list, so the history no longer selects
// version numbers and re-reads each revision with its own QueryRowContext.
// window is the optional (limit, offset) tail; an omitted limit is bounded,
// because the pool is a single connection and an unbounded history read blocks
// every writer for its whole duration.
func (r *AffectiveRepository) ListVersions(ctx context.Context, id domain.ID, window ...int) ([]AffectiveVersionRecord, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if id.Empty() {
		return nil, fmt.Errorf("%w: affect state id is required", domain.ErrInvalidArgument)
	}
	limit, offset, err := boundedListWindow("affect history", window)
	if err != nil {
		return nil, err
	}
	query := affectSelect + ` FROM affective_states AS av WHERE av.affect_id = ? ORDER BY av.version DESC`
	args := []any{string(id)}
	query, args = appendWindow(query, args, limit, offset)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list affect versions", err)
	}
	defer rows.Close()
	result := make([]AffectiveVersionRecord, 0)
	for rows.Next() {
		state, scanErr := scanAffect(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, affectVersionRecord(state))
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate affect versions", err)
	}
	return result, nil
}

func (r *AffectiveRepository) ListHistory(ctx context.Context, id domain.ID, window ...int) ([]AffectiveVersionRecord, error) {
	return r.ListVersions(ctx, id, window...)
}
