package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func (r *AffectiveRepository) Rollback(ctx context.Context, id domain.ID, args ...any) (domain.AffectiveState, error) {
	expected, target, reason, at, err := parseAffectRevisionArgs(args...)
	if err != nil {
		return domain.AffectiveState{}, err
	}
	if id.Empty() {
		return domain.AffectiveState{}, fmt.Errorf("%w: affect state id is required", domain.ErrInvalidArgument)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.AffectiveState{}, wrappedSQLError("begin affect rollback", err)
	}
	defer tx.Rollback()
	current, err := getAffectVersionTx(ctx, tx, id, 0)
	if err != nil {
		return domain.AffectiveState{}, err
	}
	if expected == 0 {
		expected = current.Version
	}
	if expected != current.Version {
		return domain.AffectiveState{}, domain.ErrConflict
	}
	if target == 0 || target > current.Version {
		return domain.AffectiveState{}, fmt.Errorf("%w: affect rollback target is invalid", domain.ErrInvalidArgument)
	}
	targetState, err := getAffectVersionTx(ctx, tx, id, target)
	if err != nil {
		return domain.AffectiveState{}, err
	}
	next := cloneAffect(targetState)
	next.Version = current.Version + 1
	next.ParentVersion = current.Version
	next.ParentID = current.RevisionID
	next.RevisionID = ""
	next.Operation = domain.AffectOperationRollback
	next.Reason = strings.TrimSpace(reason)
	next.CreatedAt = current.CreatedAt
	next.UpdatedAt = revisionTime(at, current.UpdatedAt)
	if err := r.appendAffectStateTx(ctx, tx, next, current.Version, nil); err != nil {
		return domain.AffectiveState{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AffectiveState{}, wrappedSQLError("commit affect rollback", err)
	}
	return next, nil
}

func (r *AffectiveRepository) Reset(ctx context.Context, id domain.ID, args ...any) (domain.AffectiveState, error) {
	expected, reason, at, err := parseAffectResetArgs(args...)
	if err != nil {
		return domain.AffectiveState{}, err
	}
	if id.Empty() {
		return domain.AffectiveState{}, fmt.Errorf("%w: affect state id is required", domain.ErrInvalidArgument)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.AffectiveState{}, wrappedSQLError("begin affect reset", err)
	}
	defer tx.Rollback()
	current, err := getAffectVersionTx(ctx, tx, id, 0)
	if err != nil {
		return domain.AffectiveState{}, err
	}
	if expected == 0 {
		expected = current.Version
	}
	if expected != current.Version {
		return domain.AffectiveState{}, domain.ErrConflict
	}
	seed, err := getAffectVersionTx(ctx, tx, id, 1)
	if err != nil {
		return domain.AffectiveState{}, err
	}
	next := cloneAffect(seed)
	next.Version = current.Version + 1
	next.ParentVersion = current.Version
	next.ParentID = current.RevisionID
	next.RevisionID = ""
	next.Operation = domain.AffectOperationReset
	next.Reason = strings.TrimSpace(reason)
	next.CreatedAt = current.CreatedAt
	next.UpdatedAt = revisionTime(at, current.UpdatedAt)
	if err := r.appendAffectStateTx(ctx, tx, next, current.Version, nil); err != nil {
		return domain.AffectiveState{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AffectiveState{}, wrappedSQLError("commit affect reset", err)
	}
	return next, nil
}
