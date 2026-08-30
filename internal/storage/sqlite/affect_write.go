package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func (r *AffectiveRepository) Create(ctx context.Context, state domain.AffectiveState) error {
	return r.CreateState(ctx, state)
}

func (r *AffectiveRepository) CreateState(ctx context.Context, state domain.AffectiveState) error {
	return r.createWithMetadata(ctx, state, nil)
}

func (r *AffectiveRepository) CreateStateWithMetadata(ctx context.Context, state domain.AffectiveState, metadata AffectiveVersionMetadata) error {
	return r.createWithMetadata(ctx, state, &metadata)
}

func (r *AffectiveRepository) createWithMetadata(ctx context.Context, state domain.AffectiveState, metadata *AffectiveVersionMetadata) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	state = normalizeAffectForCreate(state)
	if err := state.Validate(); err != nil {
		return err
	}
	if metadata != nil && (metadata.ParentVersion != 0 || !metadata.ParentID.Empty()) {
		return fmt.Errorf("%w: create affect state cannot have a parent", domain.ErrInvalidArgument)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin create affect state", err)
	}
	defer tx.Rollback()
	var existing uint64
	err = tx.QueryRowContext(ctx, `SELECT version FROM affective_heads WHERE affect_id = ?`, string(state.ID)).Scan(&existing)
	if err == nil {
		return domain.ErrConflict
	}
	if !isNoRows(err) {
		return wrappedSQLError("check affect head", err)
	}
	if err := r.appendAffectStateTx(ctx, tx, state, 0, metadata); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit create affect state", err)
	}
	return nil
}

func (r *AffectiveRepository) AppendVersion(ctx context.Context, state domain.AffectiveState, expectedVersion uint64, metadata ...any) (domain.AffectiveState, error) {
	var decoded *AffectiveVersionMetadata
	if len(metadata) > 1 {
		return domain.AffectiveState{}, fmt.Errorf("%w: at most one affect metadata value is allowed", domain.ErrInvalidArgument)
	}
	if len(metadata) == 1 {
		var err error
		decoded, err = affectMetadata(metadata[0])
		if err != nil {
			return domain.AffectiveState{}, err
		}
	}
	return r.appendState(ctx, state, expectedVersion, decoded)
}

func (r *AffectiveRepository) AppendState(ctx context.Context, state domain.AffectiveState, expectedVersion uint64, metadata ...any) (domain.AffectiveState, error) {
	return r.AppendVersion(ctx, state, expectedVersion, metadata...)
}

func (r *AffectiveRepository) AppendVersionWithMetadata(ctx context.Context, state domain.AffectiveState, expectedVersion uint64, metadata AffectiveVersionMetadata) (domain.AffectiveState, error) {
	return r.appendState(ctx, state, expectedVersion, &metadata)
}

func (r *AffectiveRepository) appendState(ctx context.Context, state domain.AffectiveState, expectedVersion uint64, metadata *AffectiveVersionMetadata) (domain.AffectiveState, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.AffectiveState{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.AffectiveState{}, err
	}
	if expectedVersion == 0 {
		return domain.AffectiveState{}, fmt.Errorf("%w: expected affect version must be positive", domain.ErrInvalidArgument)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.AffectiveState{}, wrappedSQLError("begin append affect state", err)
	}
	defer tx.Rollback()
	current, err := getAffectVersionTx(ctx, tx, state.ID, 0)
	if err != nil {
		return domain.AffectiveState{}, err
	}
	if current.Version != expectedVersion || state.Version != expectedVersion+1 {
		return domain.AffectiveState{}, domain.ErrConflict
	}
	state = normalizeAffectForAppend(state, current)
	if state.ParentVersion == current.ParentVersion {
		state.ParentVersion = 0
	}
	if metadata != nil && !metadata.RevisionID.Empty() {
		state.RevisionID = metadata.RevisionID
	}
	if state.RevisionID == current.RevisionID {
		state.RevisionID = ""
	}
	if state.RevisionID.Empty() {
		state.RevisionID = domain.ID(fmt.Sprintf("%s:v%d", state.ID, state.Version))
	}
	if metadata != nil && !metadata.ParentID.Empty() {
		state.ParentID = metadata.ParentID
	}
	if state.ParentID.Empty() {
		state.ParentID = current.RevisionID
	}
	state.ParentVersion = current.Version
	if metadata != nil && metadata.Operation != "" {
		state.Operation = metadata.Operation
	} else {
		state.Operation = domain.AffectOperationUpdate
	}
	if metadata != nil && strings.TrimSpace(metadata.Reason) != "" {
		state.Reason = metadata.Reason
	}
	if metadata != nil && !metadata.AuthorRunID.Empty() {
		state.AuthorRunID = metadata.AuthorRunID
	}
	if !state.Operation.Valid() {
		return domain.AffectiveState{}, fmt.Errorf("%w: invalid affect operation %q", domain.ErrInvalidArgument, state.Operation)
	}
	if err := state.Validate(); err != nil {
		return domain.AffectiveState{}, err
	}
	if err := r.appendAffectStateTx(ctx, tx, state, current.Version, metadata); err != nil {
		return domain.AffectiveState{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AffectiveState{}, wrappedSQLError("commit append affect state", err)
	}
	return state, nil
}
