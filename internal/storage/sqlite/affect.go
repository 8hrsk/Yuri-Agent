package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

type AffectiveVersionMetadata struct {
	RevisionID    domain.ID
	ParentID      domain.ID
	ParentVersion uint64
	Operation     domain.AffectOperation
	Reason        string
	AuthorRunID   domain.ID
}

type AffectiveVersionRecord = domain.AffectiveVersionRecord

// AffectiveRepository persists both the current affect snapshot and its
// append-only event journal. Event insertion and the resulting state revision
// are committed in one SQLite transaction.
type AffectiveRepository struct{ db *sql.DB }

var _ domain.AffectiveRepository = (*AffectiveRepository)(nil)

// AffectRepository is a compatibility alias for callers using the shorter
// feature name.
type AffectRepository = AffectiveRepository

func NewAffectiveRepository(database *sql.DB) *AffectiveRepository {
	return &AffectiveRepository{db: database}
}

func NewAffectRepository(database *sql.DB) *AffectiveRepository {
	return NewAffectiveRepository(database)
}

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

func (r *AffectiveRepository) ListVersions(ctx context.Context, id domain.ID, limit ...int) ([]AffectiveVersionRecord, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if id.Empty() {
		return nil, fmt.Errorf("%w: affect state id is required", domain.ErrInvalidArgument)
	}
	if len(limit) > 0 && limit[0] < 0 {
		return nil, fmt.Errorf("%w: affect history limit cannot be negative", domain.ErrInvalidArgument)
	}
	query := `SELECT version FROM affective_states WHERE affect_id = ? ORDER BY version DESC`
	args := []any{string(id)}
	if len(limit) > 0 && limit[0] > 0 {
		query += " LIMIT ?"
		args = append(args, limit[0])
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list affect versions", err)
	}
	defer rows.Close()
	versions := make([]uint64, 0)
	for rows.Next() {
		var version uint64
		if err := rows.Scan(&version); err != nil {
			return nil, wrappedSQLError("scan affect version", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate affect versions", err)
	}
	result := make([]AffectiveVersionRecord, 0, len(versions))
	for _, version := range versions {
		item, err := r.GetVersionRecord(ctx, id, version)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func (r *AffectiveRepository) ListHistory(ctx context.Context, id domain.ID, limit ...int) ([]AffectiveVersionRecord, error) {
	return r.ListVersions(ctx, id, limit...)
}

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
// returns newest events first, while each event remains immutable.
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
	query := affectEventSelect + ` FROM affective_events WHERE affect_id = ?`
	args := []any{string(id)}
	if !from.IsZero() {
		query += " AND created_at >= ?"
		args = append(args, from.UTC().Format(time.RFC3339Nano))
	}
	if !to.IsZero() {
		query += " AND created_at <= ?"
		args = append(args, to.UTC().Format(time.RFC3339Nano))
	}
	query += " ORDER BY created_at DESC, id DESC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
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

// Decay appends a snapshot calculated from all durable events at at. args may
// contain expectedVersion, timestamp, and reason. The event journal remains
// untouched, making decay deterministic and reversible.
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
	events, err := r.ListEvents(ctx, id)
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

func (r *AffectiveRepository) appendAffectStateTx(ctx context.Context, tx *sql.Tx, state domain.AffectiveState, previousVersion uint64, metadata *AffectiveVersionMetadata) error {
	if err := state.Validate(); err != nil {
		return err
	}
	if metadata == nil {
		metadata = &AffectiveVersionMetadata{}
	}
	if previousVersion == 0 && (metadata.ParentVersion != 0 || !metadata.ParentID.Empty()) {
		return fmt.Errorf("%w: initial affect revision cannot have a parent", domain.ErrInvalidArgument)
	}
	if previousVersion > 0 && metadata.ParentVersion != 0 && metadata.ParentVersion != previousVersion {
		return domain.ErrConflict
	}
	if state.RevisionID.Empty() {
		state.RevisionID = domain.ID(fmt.Sprintf("%s:v%d", state.ID, state.Version))
	}
	if metadata.RevisionID.Empty() {
		metadata.RevisionID = state.RevisionID
	}
	if metadata.ParentVersion == 0 && previousVersion > 0 {
		metadata.ParentVersion = previousVersion
	}
	if metadata.ParentID.Empty() && previousVersion > 0 {
		var parentRevision string
		if err := tx.QueryRowContext(ctx, `SELECT revision_id FROM affective_states WHERE affect_id = ? AND version = ?`, string(state.ID), previousVersion).Scan(&parentRevision); err != nil {
			return wrappedSQLError("get affect parent", err)
		}
		metadata.ParentID = domain.ID(parentRevision)
	}
	if metadata.Operation == "" {
		metadata.Operation = state.Operation
	}
	if metadata.Operation == "" {
		metadata.Operation = domain.AffectOperationUpdate
	}
	if metadata.Reason == "" {
		metadata.Reason = state.Reason
	}
	if metadata.AuthorRunID.Empty() {
		metadata.AuthorRunID = state.AuthorRunID
	}
	emotions := state.Emotions
	if emotions == nil {
		if state.Dimensions != nil {
			emotions = state.Dimensions
		} else {
			emotions = state.Values
		}
	}
	emotionsJSON, err := marshalJSON(emotions, "{}")
	if err != nil {
		return err
	}
	createdAt, err := timeValue(state.CreatedAt)
	if err != nil {
		return err
	}
	updatedAt, err := timeValue(state.UpdatedAt)
	if err != nil {
		return err
	}
	var asOf any
	if !state.AsOf.IsZero() {
		asOf = state.AsOf.UTC().Format(time.RFC3339Nano)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO affective_states(
			affect_id, version, revision_id, parent_id, parent_version, operation,
			emotions_json, summary, reason, author_run_id, as_of, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(state.ID), state.Version, string(metadata.RevisionID), nullableID(metadata.ParentID), metadata.ParentVersion,
		string(metadata.Operation), emotionsJSON, strings.TrimSpace(state.Summary), metadata.Reason,
		nullableID(metadata.AuthorRunID), asOf, createdAt, updatedAt)
	if err != nil {
		return wrappedSQLError("insert affect state", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO affective_heads(affect_id, version, revision_id, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(affect_id) DO UPDATE SET version = excluded.version, revision_id = excluded.revision_id, updated_at = excluded.updated_at`,
		string(state.ID), state.Version, string(metadata.RevisionID), updatedAt)
	if err != nil {
		return wrappedSQLError("update affect head", err)
	}
	return nil
}

const affectSelect = `SELECT av.affect_id, av.version, av.revision_id, av.parent_id, av.parent_version,
	av.operation, av.emotions_json, av.summary, av.reason, av.author_run_id, av.as_of,
	av.created_at, av.updated_at`

func getAffectVersion(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id domain.ID, version uint64) (domain.AffectiveState, error) {
	if version == 0 {
		return scanAffect(queryer.QueryRowContext(ctx, affectSelect+` FROM affective_heads AS ah
			JOIN affective_states AS av ON av.affect_id = ah.affect_id AND av.version = ah.version
			WHERE ah.affect_id = ?`, string(id)))
	}
	return scanAffect(queryer.QueryRowContext(ctx, affectSelect+` FROM affective_states AS av WHERE av.affect_id = ? AND av.version = ?`, string(id), version))
}

func getAffectVersionTx(ctx context.Context, tx *sql.Tx, id domain.ID, version uint64) (domain.AffectiveState, error) {
	return getAffectVersion(ctx, tx, id, version)
}

func scanAffect(row interface{ Scan(...any) error }) (domain.AffectiveState, error) {
	var (
		state                          domain.AffectiveState
		idValue, revisionID, operation string
		emotionsJSON, summary, reason  string
		createdAt, updatedAt           string
		asOf                           sql.NullString
		parentID, authorRunID          sql.NullString
	)
	if err := row.Scan(&idValue, &state.Version, &revisionID, &parentID, &state.ParentVersion,
		&operation, &emotionsJSON, &summary, &reason, &authorRunID, &asOf, &createdAt, &updatedAt); err != nil {
		return domain.AffectiveState{}, wrappedSQLError("scan affect state", err)
	}
	state.ID = domain.ID(idValue)
	state.RevisionID = domain.ID(revisionID)
	state.Operation = domain.AffectOperation(operation)
	state.Summary = summary
	state.Reason = reason
	if parentID.Valid {
		state.ParentID = domain.ID(parentID.String)
	}
	if authorRunID.Valid {
		state.AuthorRunID = domain.ID(authorRunID.String)
	}
	if err := json.Unmarshal([]byte(emotionsJSON), &state.Emotions); err != nil {
		return domain.AffectiveState{}, fmt.Errorf("decode affect emotions: %w", err)
	}
	var err error
	if asOf.Valid {
		if state.AsOf, err = scanTime(asOf.String); err != nil {
			return domain.AffectiveState{}, err
		}
	}
	if state.CreatedAt, err = scanTime(createdAt); err != nil {
		return domain.AffectiveState{}, err
	}
	if state.UpdatedAt, err = scanTime(updatedAt); err != nil {
		return domain.AffectiveState{}, err
	}
	return state, nil
}

const affectEventSelect = `SELECT id, affect_id, state_version, source_id, source_type, run_id,
	conversation_id, emotion, intensity, valence, decay_policy, decay_rate, half_life_seconds,
	decays_at, provenance, evidence_json, metadata_json, created_at`

func insertAffectiveEvent(ctx context.Context, tx *sql.Tx, event domain.AffectiveEvent) error {
	evidenceJSON, err := marshalJSON(event.Evidence, "[]")
	if err != nil {
		return err
	}
	createdAt, err := timeValue(event.CreatedAt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO affective_events(
			id, affect_id, state_version, source_id, source_type, run_id, conversation_id,
			emotion, intensity, valence, decay_policy, decay_rate, half_life_seconds,
			decays_at, provenance, evidence_json, metadata_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(event.ID), string(event.AffectID), event.StateVersion, nullableID(event.SourceID), event.SourceType,
		nullableID(event.RunID), nullableID(event.ConversationID), event.Emotion, event.Intensity, event.Valence,
		string(event.DecayPolicy), event.DecayRate, event.HalfLifeSeconds, nullableTimeValue(event.DecaysAt),
		event.Provenance, evidenceJSON, event.MetadataJSON, createdAt)
	return wrappedSQLError("insert affective event", err)
}

func scanAffectiveEvent(row interface{ Scan(...any) error }) (domain.AffectiveEvent, error) {
	var (
		event                                               domain.AffectiveEvent
		idValue, affectID, sourceType, emotion, decayPolicy string
		provenance, evidenceJSON, metadataJSON, createdAt   string
		stateVersion, halfLife                              uint64
		intensity, valence, decayRate                       float64
		decaysAt                                            sql.NullString
		sourceID, runID, conversationID                     sql.NullString
	)
	if err := row.Scan(&idValue, &affectID, &stateVersion, &sourceID, &sourceType, &runID,
		&conversationID, &emotion, &intensity, &valence, &decayPolicy, &decayRate, &halfLife,
		&decaysAt, &provenance, &evidenceJSON, &metadataJSON, &createdAt); err != nil {
		return domain.AffectiveEvent{}, wrappedSQLError("scan affect event", err)
	}
	event.ID = domain.ID(idValue)
	event.AffectID = domain.ID(affectID)
	event.StateVersion = stateVersion
	event.SourceType = sourceType
	event.Emotion = emotion
	event.Intensity = intensity
	event.Valence = valence
	event.DecayPolicy = domain.AffectiveDecayPolicy(decayPolicy)
	event.DecayRate = decayRate
	event.HalfLifeSeconds = int64(halfLife)
	event.Provenance = provenance
	event.MetadataJSON = metadataJSON
	if sourceID.Valid {
		event.SourceID = domain.ID(sourceID.String)
	}
	if runID.Valid {
		event.RunID = domain.ID(runID.String)
	}
	if conversationID.Valid {
		event.ConversationID = domain.ID(conversationID.String)
	}
	if decaysAt.Valid {
		var err error
		if event.DecaysAt, err = scanTime(decaysAt.String); err != nil {
			return domain.AffectiveEvent{}, err
		}
	}
	if err := json.Unmarshal([]byte(evidenceJSON), &event.Evidence); err != nil {
		return domain.AffectiveEvent{}, fmt.Errorf("decode affect event evidence: %w", err)
	}
	var err error
	if event.CreatedAt, err = scanTime(createdAt); err != nil {
		return domain.AffectiveEvent{}, err
	}
	return event, nil
}

func normalizeAffectForCreate(state domain.AffectiveState) domain.AffectiveState {
	if state.Version == 0 {
		state.Version = 1
	}
	if state.Operation == "" {
		state.Operation = domain.AffectOperationCreate
	}
	if state.CreatedAt.IsZero() && !state.UpdatedAt.IsZero() {
		state.CreatedAt = state.UpdatedAt
	}
	if state.UpdatedAt.IsZero() && !state.CreatedAt.IsZero() {
		state.UpdatedAt = state.CreatedAt
	}
	if state.Emotions == nil {
		if state.Dimensions != nil {
			state.Emotions = state.Dimensions
		} else {
			state.Emotions = state.Values
		}
	}
	state.Emotions = cloneFloatMap(state.Emotions)
	if state.AsOf.IsZero() {
		state.AsOf = state.UpdatedAt
	}
	return state
}

func normalizeAffectForAppend(state, current domain.AffectiveState) domain.AffectiveState {
	state.CreatedAt = current.CreatedAt
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	if state.Operation == "" || state.Operation == domain.AffectOperationCreate {
		state.Operation = domain.AffectOperationUpdate
	}
	if state.Emotions == nil {
		state.Emotions = state.Dimensions
		if state.Emotions == nil {
			state.Emotions = state.Values
		}
	}
	state.Emotions = cloneFloatMap(state.Emotions)
	state.Dimensions = nil
	state.Values = nil
	return state
}

func cloneAffect(state domain.AffectiveState) domain.AffectiveState {
	state.Emotions = cloneFloatMap(state.Emotions)
	state.Dimensions = cloneFloatMap(state.Dimensions)
	state.Values = cloneFloatMap(state.Values)
	return state
}

func affectVersionRecord(state domain.AffectiveState) AffectiveVersionRecord {
	return AffectiveVersionRecord{State: state, RevisionID: state.RevisionID, ParentID: state.ParentID,
		ParentVersion: state.ParentVersion, Operation: state.Operation, Reason: state.Reason, AuthorRunID: state.AuthorRunID}
}

func affectMetadata(value any) (*AffectiveVersionMetadata, error) {
	switch metadata := value.(type) {
	case AffectiveVersionMetadata:
		return &metadata, nil
	case *AffectiveVersionMetadata:
		if metadata == nil {
			return nil, fmt.Errorf("%w: nil affect metadata", domain.ErrInvalidArgument)
		}
		copy := *metadata
		return &copy, nil
	default:
		return nil, fmt.Errorf("%w: unsupported affect metadata %T", domain.ErrInvalidArgument, value)
	}
}

func parseAffectEventArgs(args ...any) (id domain.ID, expected uint64, event domain.AffectiveEvent, err error) {
	for _, value := range args {
		switch item := value.(type) {
		case domain.ID:
			if id.Empty() {
				id = item
			}
		case string:
			if id.Empty() {
				id = domain.ID(item)
			}
		case uint64:
			expected = item
		case uint:
			expected = uint64(item)
		case int:
			if item < 0 {
				err = fmt.Errorf("%w: affect version cannot be negative", domain.ErrInvalidArgument)
			} else {
				expected = uint64(item)
			}
		case domain.AffectiveEvent:
			event = item
		case *domain.AffectiveEvent:
			if item == nil {
				err = fmt.Errorf("%w: nil affect event", domain.ErrInvalidArgument)
			} else {
				event = *item
			}
		default:
			err = fmt.Errorf("%w: unsupported affect event argument %T", domain.ErrInvalidArgument, value)
		}
		if err != nil {
			return
		}
	}
	if id.Empty() && !event.AffectID.Empty() {
		id = event.AffectID
	}
	if id.Empty() {
		err = fmt.Errorf("%w: affect state id is required", domain.ErrInvalidArgument)
	}
	return
}

func parseAffectDecayArgs(args ...any) (expected uint64, at time.Time, reason string, err error) {
	for _, value := range args {
		switch item := value.(type) {
		case uint64:
			expected = item
		case uint:
			expected = uint64(item)
		case int:
			if item < 0 {
				err = fmt.Errorf("%w: affect version cannot be negative", domain.ErrInvalidArgument)
			} else {
				expected = uint64(item)
			}
		case time.Time:
			at = item
		case string:
			reason = item
		default:
			err = fmt.Errorf("%w: unsupported affect decay argument %T", domain.ErrInvalidArgument, value)
		}
		if err != nil {
			return
		}
	}
	return
}

func parseAffectRevisionArgs(args ...any) (expected, target uint64, reason string, at time.Time, err error) {
	versions := make([]uint64, 0, 2)
	for _, value := range args {
		switch item := value.(type) {
		case uint64:
			versions = append(versions, item)
		case uint:
			versions = append(versions, uint64(item))
		case int:
			if item < 0 {
				err = fmt.Errorf("%w: affect version cannot be negative", domain.ErrInvalidArgument)
			} else {
				versions = append(versions, uint64(item))
			}
		case string:
			reason = item
		case time.Time:
			at = item
		default:
			err = fmt.Errorf("%w: unsupported affect rollback argument %T", domain.ErrInvalidArgument, value)
		}
		if err != nil {
			return
		}
	}
	if err == nil {
		switch len(versions) {
		case 0:
		case 1:
			target = versions[0]
		case 2:
			expected, target = versions[0], versions[1]
		default:
			err = fmt.Errorf("%w: too many affect versions", domain.ErrInvalidArgument)
		}
	}
	return
}

func parseAffectResetArgs(args ...any) (expected uint64, reason string, at time.Time, err error) {
	for _, value := range args {
		switch item := value.(type) {
		case uint64:
			expected = item
		case uint:
			expected = uint64(item)
		case int:
			if item < 0 {
				err = fmt.Errorf("%w: affect version cannot be negative", domain.ErrInvalidArgument)
			} else {
				expected = uint64(item)
			}
		case string:
			reason = item
		case time.Time:
			at = item
		default:
			err = fmt.Errorf("%w: unsupported affect reset argument %T", domain.ErrInvalidArgument, value)
		}
		if err != nil {
			return
		}
	}
	return
}
