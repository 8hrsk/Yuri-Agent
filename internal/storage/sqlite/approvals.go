package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// ApprovalRepository is the SQLite implementation of
// domain.ApprovalRepository.
type ApprovalRepository struct {
	db *sql.DB
}

func NewApprovalRepository(database *sql.DB) *ApprovalRepository {
	return &ApprovalRepository{db: database}
}

var _ domain.ApprovalRepository = (*ApprovalRepository)(nil)

func (r *ApprovalRepository) Create(ctx context.Context, approval domain.Approval) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := validateApproval(approval); err != nil {
		return err
	}
	scope, err := json.Marshal(approval.Scope)
	if err != nil {
		return fmt.Errorf("%w: encode approval scope: %v", domain.ErrInvalidArgument, err)
	}
	requestedAt, err := timeValue(approval.RequestedAt)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO approvals(
			id, run_id, action_hash, action, tool_id, risk, scope_json, decision,
			requested_at, expires_at, decided_at, decided_by, reason, version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(approval.ID), string(approval.RunID), approval.ActionHash, approval.Action,
		approval.ToolID, string(approval.Risk), string(scope), string(approval.Decision), requestedAt,
		nullableTimeValue(approval.ExpiresAt), nullableTimeValue(approval.DecidedAt), nullableActor(approval.DecidedBy),
		approval.Reason, approval.Version)
	return wrappedSQLError("create approval", err)
}

func (r *ApprovalRepository) Get(ctx context.Context, id domain.ID) (domain.Approval, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.Approval{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.Approval{}, err
	}
	if id.Empty() {
		return domain.Approval{}, fmt.Errorf("%w: approval id is required", domain.ErrInvalidArgument)
	}
	return r.get(ctx, string(id))
}

// approvalSelect lists the full record so Get and ListByRun share one scanner
// and one round-trip per query.
const approvalSelect = `
	SELECT id, run_id, action_hash, action, tool_id, risk, scope_json, decision,
	       requested_at, expires_at, decided_at, decided_by, reason, version
	FROM approvals`

func (r *ApprovalRepository) get(ctx context.Context, id string) (domain.Approval, error) {
	return scanApproval(r.db.QueryRowContext(ctx, approvalSelect+` WHERE id = ?`, id))
}

func scanApproval(row rowScanner) (domain.Approval, error) {
	var (
		approval                                   domain.Approval
		idValue, runID, scopeJSON, requestedAt     string
		toolID, risk, decision, actionHash, action string
		expiresAt, decidedAt, decidedBy, reason    sql.NullString
	)
	err := row.Scan(
		&idValue, &runID, &actionHash, &action, &toolID, &risk, &scopeJSON, &decision,
		&requestedAt, &expiresAt, &decidedAt, &decidedBy, &reason, &approval.Version)
	if err != nil {
		return domain.Approval{}, wrappedSQLError("get approval", err)
	}
	approval.ID = domain.ID(idValue)
	approval.RunID = domain.ID(runID)
	approval.ActionHash = actionHash
	approval.Action = action
	approval.ToolID = toolID
	approval.Risk = domain.RiskLevel(risk)
	approval.Decision = domain.ApprovalDecision(decision)
	approval.Reason = reason.String
	if err := json.Unmarshal([]byte(scopeJSON), &approval.Scope); err != nil {
		return domain.Approval{}, fmt.Errorf("decode approval scope: %w", err)
	}
	if approval.RequestedAt, err = scanTime(requestedAt); err != nil {
		return domain.Approval{}, err
	}
	if approval.ExpiresAt, err = scanNullableTime(expiresAt); err != nil {
		return domain.Approval{}, err
	}
	if approval.DecidedAt, err = scanNullableTime(decidedAt); err != nil {
		return domain.Approval{}, err
	}
	if decidedBy.Valid {
		approval.DecidedBy = domain.Actor(decidedBy.String)
	}
	return approval, nil
}

// Save uses Approval.Version as an optimistic concurrency token.
func (r *ApprovalRepository) Save(ctx context.Context, approval domain.Approval) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := validateApproval(approval); err != nil {
		return err
	}
	if approval.Version < 2 {
		return fmt.Errorf("%w: approval version must be greater than one when saving", domain.ErrInvalidArgument)
	}
	scope, err := json.Marshal(approval.Scope)
	if err != nil {
		return fmt.Errorf("%w: encode approval scope: %v", domain.ErrInvalidArgument, err)
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE approvals SET
			run_id = ?, action_hash = ?, action = ?, tool_id = ?, risk = ?, scope_json = ?, decision = ?,
			requested_at = ?, expires_at = ?, decided_at = ?, decided_by = ?, reason = ?, version = ?
		WHERE id = ? AND version = ?`,
		string(approval.RunID), approval.ActionHash, approval.Action, approval.ToolID, string(approval.Risk), string(scope),
		string(approval.Decision), approval.RequestedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		nullableTimeValue(approval.ExpiresAt), nullableTimeValue(approval.DecidedAt), nullableActor(approval.DecidedBy),
		approval.Reason, approval.Version, string(approval.ID), approval.Version-1)
	if err != nil {
		return wrappedSQLError("save approval", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrappedSQLError("count saved approval", err)
	}
	if count == 1 {
		return nil
	}
	if _, err := r.get(ctx, string(approval.ID)); err != nil {
		return err
	}
	return domain.ErrConflict
}

// ListByRun reads the run's approvals in one query. The result is bounded by
// the run's own tool-call budget, and domain.ApprovalRepository fixes the
// signature, so no window is accepted here.
func (r *ApprovalRepository) ListByRun(ctx context.Context, runID domain.ID) ([]domain.Approval, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if runID.Empty() {
		return nil, fmt.Errorf("%w: run id is required", domain.ErrInvalidArgument)
	}
	rows, err := r.db.QueryContext(ctx, approvalSelect+` WHERE run_id = ?
		ORDER BY requested_at ASC, id ASC`, string(runID))
	if err != nil {
		return nil, wrappedSQLError("list approvals", err)
	}
	defer rows.Close()
	result := make([]domain.Approval, 0)
	for rows.Next() {
		item, scanErr := scanApproval(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate approvals", err)
	}
	return result, nil
}

func validateApproval(approval domain.Approval) error {
	if approval.ID.Empty() || approval.RunID.Empty() || approval.ActionHash == "" || approval.Action == "" ||
		!approval.Risk.Valid() || !approval.Scope.Valid() || !approval.Decision.Valid() || approval.Version == 0 ||
		approval.RequestedAt.IsZero() {
		return fmt.Errorf("%w: invalid approval", domain.ErrInvalidArgument)
	}
	if approval.Decision == domain.ApprovalPending {
		if !approval.DecidedAt.IsZero() || approval.DecidedBy != "" {
			return fmt.Errorf("%w: pending approval cannot be decided", domain.ErrInvalidArgument)
		}
	} else if approval.DecidedAt.IsZero() || !approval.DecidedBy.Valid() {
		return fmt.Errorf("%w: final approval requires decision metadata", domain.ErrInvalidArgument)
	}
	return nil
}

func nullableActor(actor domain.Actor) any {
	if !actor.Valid() {
		return nil
	}
	return string(actor)
}
