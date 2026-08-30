package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// AppendEvent inserts an immutable event and appends the state it produces in
// one transaction. Supported argument forms are (id, expectedVersion, event)
// and (event, id, expectedVersion); accepting both keeps integration callers
// independent of event-first versus aggregate-first vocabulary.
func (r *AffectiveRepository) AppendEvent(ctx context.Context, args ...any) (domain.AffectiveState, error) {
	id, expected, event, err := parseAffectEventArgs(args...)
	if err != nil {
		return domain.AffectiveState{}, err
	}
	if err := requireDatabase(r.db); err != nil {
		return domain.AffectiveState{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.AffectiveState{}, err
	}
	if err := event.Validate(); err != nil {
		return domain.AffectiveState{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.AffectiveState{}, wrappedSQLError("begin append affect event", err)
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
	when := event.CreatedAt
	next, err := current.ApplyEvent(event, when)
	if err != nil {
		return domain.AffectiveState{}, err
	}
	next.ID = id
	next.ParentID = current.RevisionID
	next.ParentVersion = current.Version
	next.RevisionID = ""
	next.Operation = domain.AffectOperationEvent
	next.Reason = "affective event"
	if err := r.appendAffectStateTx(ctx, tx, next, current.Version, nil); err != nil {
		return domain.AffectiveState{}, err
	}
	event.AffectID = id
	event.StateVersion = next.Version
	if err := insertAffectiveEvent(ctx, tx, event); err != nil {
		return domain.AffectiveState{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AffectiveState{}, wrappedSQLError("commit affect event", err)
	}
	return next, nil
}

func (r *AffectiveRepository) AppendAffectiveEvent(ctx context.Context, args ...any) (domain.AffectiveState, error) {
	return r.AppendEvent(ctx, args...)
}

func (r *AffectiveRepository) GetEvent(ctx context.Context, id domain.ID) (domain.AffectiveEvent, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.AffectiveEvent{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.AffectiveEvent{}, err
	}
	if id.Empty() {
		return domain.AffectiveEvent{}, fmt.Errorf("%w: affect event id is required", domain.ErrInvalidArgument)
	}
	return scanAffectiveEvent(r.db.QueryRowContext(ctx, affectEventSelect+` FROM affective_events WHERE id = ?`, string(id)))
}

// ListEvents accepts optional start/end timestamps and a positive limit. It
// returns newest events first, while each event remains immutable. An omitted
// limit is bounded by defaultListLimit rather than reading the whole
// append-only journal: the pool is a single connection, so an unbounded read
// of a long-lived state's events blocks every writer for its whole duration.
// A caller that wants more says so with an explicit limit, up to maxListLimit.
func (r *AffectiveRepository) ListEvents(ctx context.Context, id domain.ID, options ...any) ([]domain.AffectiveEvent, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if id.Empty() {
		return nil, fmt.Errorf("%w: affect state id is required", domain.ErrInvalidArgument)
	}
	var from, to time.Time
	limit := 0
	for _, value := range options {
		switch item := value.(type) {
		case time.Time:
			if from.IsZero() {
				from = item
			} else {
				to = item
			}
		case int:
			if item < 0 {
				return nil, fmt.Errorf("%w: affect event limit cannot be negative", domain.ErrInvalidArgument)
			}
			limit = item
		case uint64:
			limit = int(item)
		default:
			return nil, fmt.Errorf("%w: unsupported affect event option %T", domain.ErrInvalidArgument, value)
		}
	}
	limit, _, err := boundedListWindow("affect event", []int{limit})
	if err != nil {
		return nil, err
	}
	query := affectEventSelect + ` FROM affective_events WHERE affect_id = ?`
	args := []any{string(id)}
	if !from.IsZero() {
		query += " AND created_at >= ?"
		args = append(args, formatTime(from))
	}
	if !to.IsZero() {
		query += " AND created_at <= ?"
		args = append(args, formatTime(to))
	}
	query += " ORDER BY created_at DESC, id DESC"
	query, args = appendWindow(query, args, limit, 0)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list affect events", err)
	}
	defer rows.Close()
	result := make([]domain.AffectiveEvent, 0)
	for rows.Next() {
		item, err := scanAffectiveEvent(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate affect events", err)
	}
	return result, nil
}

// listDecayContributors returns only the events that can still change the
// decayed state at at. Reading the whole append-only journal made every
// recomputation deserialize every evidence_json/metadata_json blob ever
// written, so the cost of a decay tick grew with the age of the installation
// even though long-expired events contribute exactly zero.
func (r *AffectiveRepository) listDecayContributors(ctx context.Context, id domain.ID, at time.Time) ([]domain.AffectiveEvent, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if id.Empty() {
		return nil, fmt.Errorf("%w: affect state id is required", domain.ErrInvalidArgument)
	}
	bound := formatTime(at)
	rows, err := r.db.QueryContext(ctx,
		affectEventSelect+` FROM affective_events WHERE affect_id = ? AND (`+affectEventContributes+`)
		ORDER BY created_at DESC, id DESC`,
		string(id), bound, bound, bound, affectDecayHalfLives)
	if err != nil {
		return nil, wrappedSQLError("list affect decay contributors", err)
	}
	defer rows.Close()
	result := make([]domain.AffectiveEvent, 0)
	for rows.Next() {
		item, err := scanAffectiveEvent(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate affect decay contributors", err)
	}
	return result, nil
}
