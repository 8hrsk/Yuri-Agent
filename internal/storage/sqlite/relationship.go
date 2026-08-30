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

type RelationshipVersionMetadata struct {
	RevisionID    domain.ID
	ParentID      domain.ID
	ParentVersion uint64
	Operation     domain.RelationshipOperation
	Reason        string
	Evidence      []domain.EvidenceLink
	AuthorRunID   domain.ID
}

type RelationshipVersionRecord = domain.RelationshipVersionRecord

// RelationshipRepository stores the subjective relationship snapshot as an
// append-only journal. It never writes factual memories and has no access to
// immutable policy/grants.
type RelationshipRepository struct {
	db       *sql.DB
	maxDelta float64
}

var _ domain.RelationshipRepository = (*RelationshipRepository)(nil)

func NewRelationshipRepository(database *sql.DB) *RelationshipRepository {
	return &RelationshipRepository{db: database, maxDelta: 0.25}
}

func (r *RelationshipRepository) SetMaxDelta(value float64) error {
	if value < 0 || value > 1 {
		return fmt.Errorf("%w: relationship max delta is out of range", domain.ErrInvalidArgument)
	}
	r.maxDelta = value
	return nil
}

func (r *RelationshipRepository) Create(ctx context.Context, state domain.RelationshipState) error {
	return r.createWithMetadata(ctx, state, nil)
}

func (r *RelationshipRepository) CreateWithMetadata(ctx context.Context, state domain.RelationshipState, metadata RelationshipVersionMetadata) error {
	return r.createWithMetadata(ctx, state, &metadata)
}

func (r *RelationshipRepository) createWithMetadata(ctx context.Context, state domain.RelationshipState, metadata *RelationshipVersionMetadata) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	state = normalizeRelationshipForCreate(state)
	if err := state.Validate(); err != nil {
		return err
	}
	if metadata != nil && (metadata.ParentVersion != 0 || !metadata.ParentID.Empty()) {
		return fmt.Errorf("%w: create relationship cannot have a parent", domain.ErrInvalidArgument)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin create relationship", err)
	}
	defer tx.Rollback()
	var existing uint64
	err = tx.QueryRowContext(ctx, `SELECT version FROM relationship_heads WHERE relationship_id = ?`, string(state.ID)).Scan(&existing)
	if err == nil {
		return domain.ErrConflict
	}
	if !isNoRows(err) {
		return wrappedSQLError("check relationship head", err)
	}
	if err := r.appendRelationshipTx(ctx, tx, state, 0, metadata); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit create relationship", err)
	}
	return nil
}

func (r *RelationshipRepository) AppendVersion(ctx context.Context, state domain.RelationshipState, expectedVersion uint64, metadata ...any) (domain.RelationshipState, error) {
	var decoded *RelationshipVersionMetadata
	if len(metadata) > 1 {
		return domain.RelationshipState{}, fmt.Errorf("%w: at most one relationship metadata value is allowed", domain.ErrInvalidArgument)
	}
	if len(metadata) == 1 {
		var err error
		decoded, err = relationshipMetadata(metadata[0])
		if err != nil {
			return domain.RelationshipState{}, err
		}
	}
	if err := requireDatabase(r.db); err != nil {
		return domain.RelationshipState{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.RelationshipState{}, err
	}
	if expectedVersion == 0 {
		return domain.RelationshipState{}, fmt.Errorf("%w: expected relationship version must be positive", domain.ErrInvalidArgument)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RelationshipState{}, wrappedSQLError("begin append relationship", err)
	}
	defer tx.Rollback()
	current, err := getRelationshipVersionTx(ctx, tx, state.ID, 0)
	if err != nil {
		return domain.RelationshipState{}, err
	}
	if current.Version != expectedVersion || state.Version != expectedVersion+1 {
		return domain.RelationshipState{}, domain.ErrConflict
	}
	state = normalizeRelationshipForAppend(state, current)
	if state.ParentVersion == current.ParentVersion {
		state.ParentVersion = 0
	}
	operation := state.Operation
	if decoded != nil && decoded.Operation != "" {
		operation = decoded.Operation
	}
	if decoded == nil || operation == "" || operation == domain.RelationshipOperationCreate {
		operation = domain.RelationshipOperationUpdate
	}
	if decoded != nil && strings.TrimSpace(decoded.Reason) != "" {
		state.Reason = decoded.Reason
	}
	if decoded != nil && len(decoded.Evidence) > 0 {
		state.Evidence = append([]domain.EvidenceLink(nil), decoded.Evidence...)
	}
	if operation != domain.RelationshipOperationRollback && operation != domain.RelationshipOperationReset {
		if err := domain.ValidateRelationshipEvolution(current, state, r.maxDelta); err != nil {
			return domain.RelationshipState{}, err
		}
	}
	state.Operation = operation
	if decoded != nil && !decoded.RevisionID.Empty() {
		state.RevisionID = decoded.RevisionID
	}
	if state.RevisionID == current.RevisionID {
		state.RevisionID = ""
	}
	if state.RevisionID.Empty() {
		state.RevisionID = domain.ID(fmt.Sprintf("%s:v%d", state.ID, state.Version))
	}
	if decoded != nil && !decoded.ParentID.Empty() {
		state.ParentID = decoded.ParentID
	}
	if state.ParentID.Empty() {
		state.ParentID = current.RevisionID
	}
	state.ParentVersion = current.Version
	if decoded != nil && strings.TrimSpace(decoded.Reason) != "" {
		state.Reason = decoded.Reason
	}
	if decoded != nil && len(decoded.Evidence) > 0 {
		state.Evidence = append([]domain.EvidenceLink(nil), decoded.Evidence...)
	}
	if decoded != nil && !decoded.AuthorRunID.Empty() {
		state.AuthorRunID = decoded.AuthorRunID
	}
	if err := r.appendRelationshipTx(ctx, tx, state, current.Version, decoded); err != nil {
		return domain.RelationshipState{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RelationshipState{}, wrappedSQLError("commit append relationship", err)
	}
	return state, nil
}

func (r *RelationshipRepository) AppendVersionWithMetadata(ctx context.Context, state domain.RelationshipState, expectedVersion uint64, metadata RelationshipVersionMetadata) (domain.RelationshipState, error) {
	return r.AppendVersion(ctx, state, expectedVersion, metadata)
}

func (r *RelationshipRepository) Get(ctx context.Context, id domain.ID) (domain.RelationshipState, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.RelationshipState{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.RelationshipState{}, err
	}
	if id.Empty() {
		return domain.RelationshipState{}, fmt.Errorf("%w: relationship id is required", domain.ErrInvalidArgument)
	}
	return getRelationshipVersion(ctx, r.db, id, 0)
}

func (r *RelationshipRepository) GetCurrent(ctx context.Context, id domain.ID) (domain.RelationshipState, error) {
	return r.Get(ctx, id)
}

func (r *RelationshipRepository) Current(ctx context.Context, id domain.ID) (domain.RelationshipState, error) {
	return r.Get(ctx, id)
}

func (r *RelationshipRepository) GetVersion(ctx context.Context, id domain.ID, version uint64) (domain.RelationshipState, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.RelationshipState{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.RelationshipState{}, err
	}
	if id.Empty() || version == 0 {
		return domain.RelationshipState{}, fmt.Errorf("%w: relationship id and positive version are required", domain.ErrInvalidArgument)
	}
	return getRelationshipVersion(ctx, r.db, id, version)
}

func (r *RelationshipRepository) GetVersionRecord(ctx context.Context, id domain.ID, version uint64) (RelationshipVersionRecord, error) {
	state, err := r.GetVersion(ctx, id, version)
	if err != nil {
		return RelationshipVersionRecord{}, err
	}
	return relationshipVersionRecord(state), nil
}

// ListVersions reads each revision in full in one query. window is the
// optional (limit, offset) tail.
func (r *RelationshipRepository) ListVersions(ctx context.Context, id domain.ID, window ...int) ([]RelationshipVersionRecord, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if id.Empty() {
		return nil, fmt.Errorf("%w: relationship id is required", domain.ErrInvalidArgument)
	}
	limit, offset, err := listWindow("relationship history", window)
	if err != nil {
		return nil, err
	}
	query := relationshipSelect + ` FROM relationship_versions AS rv
		WHERE rv.relationship_id = ? ORDER BY rv.version DESC`
	args := []any{string(id)}
	query, args = appendWindow(query, args, limit, offset)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list relationship versions", err)
	}
	defer rows.Close()
	result := make([]RelationshipVersionRecord, 0)
	for rows.Next() {
		state, scanErr := scanRelationship(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, relationshipVersionRecord(state))
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate relationship versions", err)
	}
	return result, nil
}

func (r *RelationshipRepository) ListHistory(ctx context.Context, id domain.ID, window ...int) ([]RelationshipVersionRecord, error) {
	return r.ListVersions(ctx, id, window...)
}

// RecordOpinion appends one opinion while preserving all other relationship
// dimensions/opinions. An opinion ID already present in the snapshot is
// replaced; a new ID is assigned deterministically when omitted.
func (r *RelationshipRepository) RecordOpinion(ctx context.Context, id domain.ID, expectedVersion uint64, opinion domain.RelationshipOpinion, at time.Time, reason string) (domain.RelationshipState, error) {
	current, err := r.Get(ctx, id)
	if err != nil {
		return domain.RelationshipState{}, err
	}
	if current.Version != expectedVersion {
		return domain.RelationshipState{}, domain.ErrConflict
	}
	if opinion.ID.Empty() {
		opinion.ID = domain.ID(fmt.Sprintf("%s:opinion:%d", id, current.Version+1))
	}
	if opinion.CreatedAt.IsZero() {
		opinion.CreatedAt = revisionTime(at, current.UpdatedAt)
	}
	if opinion.UpdatedAt.IsZero() {
		opinion.UpdatedAt = opinion.CreatedAt
	}
	if err := opinion.Validate(); err != nil {
		return domain.RelationshipState{}, err
	}
	next := cloneRelationship(current)
	next.Version++
	next.ParentVersion = current.Version
	next.ParentID = current.RevisionID
	next.RevisionID = ""
	next.Operation = domain.RelationshipOperationUpdate
	next.Reason = strings.TrimSpace(reason)
	next.UpdatedAt = revisionTime(at, current.UpdatedAt)
	replaced := false
	for index := range next.Opinions {
		if next.Opinions[index].ID == opinion.ID {
			next.Opinions[index] = opinion
			replaced = true
			break
		}
	}
	if !replaced {
		next.Opinions = append(next.Opinions, opinion)
	}
	return r.AppendVersion(ctx, next, expectedVersion)
}

// Rollback appends a copy of targetVersion. args may contain expectedVersion,
// target version, reason, and timestamp in any order.
func (r *RelationshipRepository) Rollback(ctx context.Context, id domain.ID, args ...any) (domain.RelationshipState, error) {
	expected, target, reason, at, err := parseRelationshipRevisionArgs(args...)
	if err != nil {
		return domain.RelationshipState{}, err
	}
	if id.Empty() {
		return domain.RelationshipState{}, fmt.Errorf("%w: relationship id is required", domain.ErrInvalidArgument)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RelationshipState{}, wrappedSQLError("begin relationship rollback", err)
	}
	defer tx.Rollback()
	current, err := getRelationshipVersionTx(ctx, tx, id, 0)
	if err != nil {
		return domain.RelationshipState{}, err
	}
	if expected == 0 {
		expected = current.Version
	}
	if expected != current.Version {
		return domain.RelationshipState{}, domain.ErrConflict
	}
	if target == 0 || target > current.Version {
		return domain.RelationshipState{}, fmt.Errorf("%w: relationship rollback target is invalid", domain.ErrInvalidArgument)
	}
	targetState, err := getRelationshipVersionTx(ctx, tx, id, target)
	if err != nil {
		return domain.RelationshipState{}, err
	}
	next := cloneRelationship(targetState)
	next.Version = current.Version + 1
	next.ParentVersion = current.Version
	next.ParentID = current.RevisionID
	next.RevisionID = ""
	next.Operation = domain.RelationshipOperationRollback
	next.Reason = strings.TrimSpace(reason)
	next.CreatedAt = current.CreatedAt
	next.UpdatedAt = revisionTime(at, current.UpdatedAt)
	if err := r.appendRelationshipTx(ctx, tx, next, current.Version, nil); err != nil {
		return domain.RelationshipState{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RelationshipState{}, wrappedSQLError("commit relationship rollback", err)
	}
	return next, nil
}

func (r *RelationshipRepository) Reset(ctx context.Context, id domain.ID, args ...any) (domain.RelationshipState, error) {
	expected, reason, at, err := parseRelationshipResetArgs(args...)
	if err != nil {
		return domain.RelationshipState{}, err
	}
	if id.Empty() {
		return domain.RelationshipState{}, fmt.Errorf("%w: relationship id is required", domain.ErrInvalidArgument)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RelationshipState{}, wrappedSQLError("begin relationship reset", err)
	}
	defer tx.Rollback()
	current, err := getRelationshipVersionTx(ctx, tx, id, 0)
	if err != nil {
		return domain.RelationshipState{}, err
	}
	if expected == 0 {
		expected = current.Version
	}
	if expected != current.Version {
		return domain.RelationshipState{}, domain.ErrConflict
	}
	seed, err := getRelationshipVersionTx(ctx, tx, id, 1)
	if err != nil {
		return domain.RelationshipState{}, err
	}
	next := cloneRelationship(seed)
	next.Version = current.Version + 1
	next.ParentVersion = current.Version
	next.ParentID = current.RevisionID
	next.RevisionID = ""
	next.Operation = domain.RelationshipOperationReset
	next.Reason = strings.TrimSpace(reason)
	next.CreatedAt = current.CreatedAt
	next.UpdatedAt = revisionTime(at, current.UpdatedAt)
	if err := r.appendRelationshipTx(ctx, tx, next, current.Version, nil); err != nil {
		return domain.RelationshipState{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RelationshipState{}, wrappedSQLError("commit relationship reset", err)
	}
	return next, nil
}

func (r *RelationshipRepository) appendRelationshipTx(ctx context.Context, tx *sql.Tx, state domain.RelationshipState, previousVersion uint64, metadata *RelationshipVersionMetadata) error {
	if metadata == nil {
		metadata = &RelationshipVersionMetadata{}
	}
	if previousVersion == 0 && (metadata.ParentVersion != 0 || !metadata.ParentID.Empty()) {
		return fmt.Errorf("%w: initial relationship revision cannot have a parent", domain.ErrInvalidArgument)
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
		if err := tx.QueryRowContext(ctx, `SELECT revision_id FROM relationship_versions WHERE relationship_id = ? AND version = ?`, string(state.ID), previousVersion).Scan(&parentRevision); err != nil {
			return wrappedSQLError("get relationship parent", err)
		}
		metadata.ParentID = domain.ID(parentRevision)
	}
	if metadata.Operation == "" {
		metadata.Operation = state.Operation
	}
	if metadata.Operation == "" {
		metadata.Operation = domain.RelationshipOperationUpdate
	}
	if metadata.Reason == "" {
		metadata.Reason = state.Reason
	}
	if metadata.Evidence == nil {
		metadata.Evidence = append([]domain.EvidenceLink(nil), state.Evidence...)
	}
	if metadata.AuthorRunID.Empty() {
		metadata.AuthorRunID = state.AuthorRunID
	}
	dimensions := state.Dimensions
	if dimensions == nil && state.DimensionsJSON != "" {
		if err := json.Unmarshal([]byte(state.DimensionsJSON), &dimensions); err != nil {
			return fmt.Errorf("%w: relationship dimensions_json must be valid JSON", domain.ErrInvalidArgument)
		}
	}
	dimensionsJSON, err := marshalJSON(dimensions, "{}")
	if err != nil {
		return err
	}
	opinionsJSON, err := marshalJSON(state.Opinions, "[]")
	if err != nil {
		return err
	}
	evidenceJSON, err := marshalJSON(metadata.Evidence, "[]")
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
	_, err = tx.ExecContext(ctx, `
		INSERT INTO relationship_versions(
			relationship_id, version, revision_id, parent_id, parent_version, operation,
			dimensions_json, opinions_json, summary, reason, evidence_json,
			author_run_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(state.ID), state.Version, string(metadata.RevisionID), nullableID(metadata.ParentID), metadata.ParentVersion,
		string(metadata.Operation), dimensionsJSON, opinionsJSON, strings.TrimSpace(state.Summary), metadata.Reason,
		evidenceJSON, nullableID(metadata.AuthorRunID), createdAt, updatedAt)
	if err != nil {
		return wrappedSQLError("insert relationship version", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO relationship_heads(relationship_id, version, revision_id, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(relationship_id) DO UPDATE SET version = excluded.version, revision_id = excluded.revision_id, updated_at = excluded.updated_at`,
		string(state.ID), state.Version, string(metadata.RevisionID), updatedAt)
	if err != nil {
		return wrappedSQLError("update relationship head", err)
	}
	return nil
}

func getRelationshipVersion(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id domain.ID, version uint64) (domain.RelationshipState, error) {
	if version == 0 {
		return scanRelationship(queryer.QueryRowContext(ctx, relationshipSelect+` FROM relationship_heads AS rh
			JOIN relationship_versions AS rv ON rv.relationship_id = rh.relationship_id AND rv.version = rh.version
			WHERE rh.relationship_id = ?`, string(id)))
	}
	return scanRelationship(queryer.QueryRowContext(ctx, relationshipSelect+` FROM relationship_versions AS rv
		WHERE rv.relationship_id = ? AND rv.version = ?`, string(id), version))
}

func getRelationshipVersionTx(ctx context.Context, tx *sql.Tx, id domain.ID, version uint64) (domain.RelationshipState, error) {
	return getRelationshipVersion(ctx, tx, id, version)
}

const relationshipSelect = `SELECT rv.relationship_id, rv.version, rv.revision_id, rv.parent_id, rv.parent_version,
	rv.operation, rv.dimensions_json, rv.opinions_json, rv.summary, rv.reason, rv.evidence_json,
	rv.author_run_id, rv.created_at, rv.updated_at`

func scanRelationship(row interface{ Scan(...any) error }) (domain.RelationshipState, error) {
	var (
		state                                          domain.RelationshipState
		idValue, revisionID, operation, dimensionsJSON string
		opinionsJSON, summary, reason, evidenceJSON    string
		createdAt, updatedAt                           string
		parentID, authorRunID                          sql.NullString
	)
	if err := row.Scan(&idValue, &state.Version, &revisionID, &parentID, &state.ParentVersion,
		&operation, &dimensionsJSON, &opinionsJSON, &summary, &reason, &evidenceJSON,
		&authorRunID, &createdAt, &updatedAt); err != nil {
		return domain.RelationshipState{}, wrappedSQLError("scan relationship", err)
	}
	state.ID = domain.ID(idValue)
	state.RevisionID = domain.ID(revisionID)
	state.Operation = domain.RelationshipOperation(operation)
	state.DimensionsJSON = dimensionsJSON
	state.Summary = summary
	state.Reason = reason
	if parentID.Valid {
		state.ParentID = domain.ID(parentID.String)
	}
	if authorRunID.Valid {
		state.AuthorRunID = domain.ID(authorRunID.String)
	}
	if err := json.Unmarshal([]byte(dimensionsJSON), &state.Dimensions); err != nil {
		return domain.RelationshipState{}, fmt.Errorf("decode relationship dimensions: %w", err)
	}
	if err := json.Unmarshal([]byte(opinionsJSON), &state.Opinions); err != nil {
		return domain.RelationshipState{}, fmt.Errorf("decode relationship opinions: %w", err)
	}
	if err := json.Unmarshal([]byte(evidenceJSON), &state.Evidence); err != nil {
		return domain.RelationshipState{}, fmt.Errorf("decode relationship evidence: %w", err)
	}
	var err error
	if state.CreatedAt, err = scanTime(createdAt); err != nil {
		return domain.RelationshipState{}, err
	}
	if state.UpdatedAt, err = scanTime(updatedAt); err != nil {
		return domain.RelationshipState{}, err
	}
	return state, nil
}

func normalizeRelationshipForCreate(state domain.RelationshipState) domain.RelationshipState {
	if state.Version == 0 {
		state.Version = 1
	}
	if state.Operation == "" {
		state.Operation = domain.RelationshipOperationCreate
	}
	if state.CreatedAt.IsZero() && !state.UpdatedAt.IsZero() {
		state.CreatedAt = state.UpdatedAt
	}
	if state.UpdatedAt.IsZero() && !state.CreatedAt.IsZero() {
		state.UpdatedAt = state.CreatedAt
	}
	if state.Dimensions == nil && state.DimensionsJSON != "" {
		_ = json.Unmarshal([]byte(state.DimensionsJSON), &state.Dimensions)
	}
	state.Dimensions = cloneFloatMap(state.Dimensions)
	state.Summary = strings.TrimSpace(state.Summary)
	return state
}

func normalizeRelationshipForAppend(state, current domain.RelationshipState) domain.RelationshipState {
	state.CreatedAt = current.CreatedAt
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	if state.Operation == "" {
		state.Operation = domain.RelationshipOperationUpdate
	}
	if state.Dimensions == nil && state.DimensionsJSON != "" {
		_ = json.Unmarshal([]byte(state.DimensionsJSON), &state.Dimensions)
	}
	state.Dimensions = cloneFloatMap(state.Dimensions)
	state.DimensionsJSON = ""
	return state
}

func cloneRelationship(state domain.RelationshipState) domain.RelationshipState {
	state.Dimensions = cloneFloatMap(state.Dimensions)
	state.Opinions = append([]domain.RelationshipOpinion(nil), state.Opinions...)
	state.Evidence = append([]domain.EvidenceLink(nil), state.Evidence...)
	return state
}

func relationshipVersionRecord(state domain.RelationshipState) RelationshipVersionRecord {
	return RelationshipVersionRecord{Relationship: state, RevisionID: state.RevisionID, ParentID: state.ParentID,
		ParentVersion: state.ParentVersion, Operation: state.Operation, Reason: state.Reason,
		Evidence: append([]domain.EvidenceLink(nil), state.Evidence...), AuthorRunID: state.AuthorRunID}
}

func relationshipMetadata(value any) (*RelationshipVersionMetadata, error) {
	switch metadata := value.(type) {
	case RelationshipVersionMetadata:
		return &metadata, nil
	case *RelationshipVersionMetadata:
		if metadata == nil {
			return nil, fmt.Errorf("%w: nil relationship metadata", domain.ErrInvalidArgument)
		}
		copy := *metadata
		return &copy, nil
	default:
		return nil, fmt.Errorf("%w: unsupported relationship metadata %T", domain.ErrInvalidArgument, value)
	}
}

func parseRelationshipRevisionArgs(args ...any) (expected, target uint64, reason string, at time.Time, err error) {
	versions := make([]uint64, 0, 2)
	for _, value := range args {
		switch item := value.(type) {
		case uint64:
			versions = append(versions, item)
		case uint:
			versions = append(versions, uint64(item))
		case int:
			if item < 0 {
				err = fmt.Errorf("%w: relationship version cannot be negative", domain.ErrInvalidArgument)
			} else {
				versions = append(versions, uint64(item))
			}
		case string:
			reason = item
		case time.Time:
			at = item
		default:
			err = fmt.Errorf("%w: unsupported relationship rollback argument %T", domain.ErrInvalidArgument, value)
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
			err = fmt.Errorf("%w: too many relationship versions", domain.ErrInvalidArgument)
		}
	}
	return
}

func parseRelationshipResetArgs(args ...any) (expected uint64, reason string, at time.Time, err error) {
	for _, value := range args {
		switch item := value.(type) {
		case uint64:
			expected = item
		case uint:
			expected = uint64(item)
		case int:
			if item < 0 {
				err = fmt.Errorf("%w: relationship version cannot be negative", domain.ErrInvalidArgument)
			} else {
				expected = uint64(item)
			}
		case string:
			reason = item
		case time.Time:
			at = item
		default:
			err = fmt.Errorf("%w: unsupported relationship reset argument %T", domain.ErrInvalidArgument, value)
		}
		if err != nil {
			return
		}
	}
	return
}
