package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// PruneEvents applies retention to the affect journal of one state. It
// deletes only events that are both older than AffectEventRetention and
// already contributing exactly zero at at, so a decay computed at or after at
// produces the same result before and after the prune. It returns the number
// of deleted events.
//
// The one behaviour it does not preserve is recomputing a decay for a
// timestamp earlier than the at it was called with; affect decay is always
// evaluated forward from the current clock, so that is not a supported query.
func (r *AffectiveRepository) PruneEvents(ctx context.Context, id domain.ID, at time.Time) (int64, error) {
	if err := requireDatabase(r.db); err != nil {
		return 0, err
	}
	if err := contextErr(ctx); err != nil {
		return 0, err
	}
	if id.Empty() {
		return 0, fmt.Errorf("%w: affect state id is required", domain.ErrInvalidArgument)
	}
	if at.IsZero() {
		return 0, fmt.Errorf("%w: affect retention timestamp is required", domain.ErrInvalidArgument)
	}
	at = at.UTC()
	bound := formatTime(at)
	floor := formatTime(at.Add(-AffectEventRetention))
	result, err := r.db.ExecContext(ctx, `
		DELETE FROM affective_events
		WHERE affect_id = ?
		  AND julianday(created_at) < julianday(?)
		  AND NOT (`+affectEventContributes+`)`,
		string(id), floor, bound, bound, bound, affectDecayHalfLives)
	if err != nil {
		return 0, wrappedSQLError("prune affect events", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, wrappedSQLError("prune affect events", err)
	}
	return deleted, nil
}

// Decay appends a snapshot calculated from the durable events that still
// contribute at at. args may contain expectedVersion, timestamp, and reason.
// Retention is applied first, and it only removes events whose contribution is
// already zero, so the snapshot is the same one an unbounded journal would
// have produced.
func (r *AffectiveRepository) Decay(ctx context.Context, id domain.ID, args ...any) (domain.AffectiveState, error) {
	expected, at, reason, err := parseAffectDecayArgs(args...)
	if err != nil {
		return domain.AffectiveState{}, err
	}
	current, err := r.GetState(ctx, id)
	if err != nil {
		return domain.AffectiveState{}, err
	}
	if expected != 0 && expected != current.Version {
		return domain.AffectiveState{}, domain.ErrConflict
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if _, err := r.PruneEvents(ctx, id, at); err != nil {
		return domain.AffectiveState{}, err
	}
	events, err := r.listDecayContributors(ctx, id, at)
	if err != nil {
		return domain.AffectiveState{}, err
	}
	next := current.Decay(events, at)
	next.ID = current.ID
	next.Version = current.Version + 1
	next.ParentVersion = current.Version
	next.ParentID = current.RevisionID
	next.RevisionID = ""
	next.Operation = domain.AffectOperationUpdate
	next.Reason = reason
	return r.AppendVersion(ctx, next, current.Version)
}
