package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

const (
	ToolCallPending   = "pending"
	ToolCallRunning   = "running"
	ToolCallSucceeded = "succeeded"
	ToolCallFailed    = "failed"
	ToolCallCancelled = "cancelled"
)

// ToolCallRepository persists redacted tool intents and outcomes.
type ToolCallRepository struct {
	db *sql.DB
}

func NewToolCallRepository(database *sql.DB) *ToolCallRepository {
	return &ToolCallRepository{db: database}
}

func (r *ToolCallRepository) Create(ctx context.Context, call ToolCall) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := validateToolCall(call); err != nil {
		return err
	}
	createdAt, err := timeValue(call.CreatedAt)
	if err != nil {
		return err
	}
	updatedAt, err := timeValue(call.UpdatedAt)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO tool_calls(
			id, run_id, tool_id, args_redacted, risk, approval_id, status,
			result_ref, idempotency_key, version, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(call.ID), string(call.RunID), strings.TrimSpace(call.ToolID), call.ArgsRedacted,
		string(call.Risk), nullableID(call.ApprovalID), strings.TrimSpace(call.Status), call.ResultRef,
		call.IdempotencyKey, call.Version, createdAt, updatedAt)
	return wrappedSQLError("create tool call", err)
}

func (r *ToolCallRepository) Get(ctx context.Context, id domain.ID) (ToolCall, error) {
	if err := requireDatabase(r.db); err != nil {
		return ToolCall{}, err
	}
	if err := contextErr(ctx); err != nil {
		return ToolCall{}, err
	}
	if id.Empty() {
		return ToolCall{}, fmt.Errorf("%w: tool call id is required", domain.ErrInvalidArgument)
	}
	return r.get(ctx, string(id))
}

func (r *ToolCallRepository) get(ctx context.Context, id string) (ToolCall, error) {
	var (
		call                                       ToolCall
		idValue, runID, toolID, args, risk, status string
		approvalID, resultRef, idempotencyKey      sql.NullString
		createdAt, updatedAt                       string
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT id, run_id, tool_id, args_redacted, risk, approval_id, status,
		       result_ref, idempotency_key, version, created_at, updated_at
		FROM tool_calls WHERE id = ?`, id).Scan(
		&idValue, &runID, &toolID, &args, &risk, &approvalID, &status,
		&resultRef, &idempotencyKey, &call.Version, &createdAt, &updatedAt)
	if err != nil {
		return ToolCall{}, wrappedSQLError("get tool call", err)
	}
	call.ID = domain.ID(idValue)
	call.RunID = domain.ID(runID)
	call.ToolID = toolID
	call.ArgsRedacted = args
	call.Risk = domain.RiskLevel(risk)
	if approvalID.Valid {
		call.ApprovalID = domain.ID(approvalID.String)
	}
	call.Status = status
	call.ResultRef = resultRef.String
	call.IdempotencyKey = idempotencyKey.String
	if call.CreatedAt, err = scanTime(createdAt); err != nil {
		return ToolCall{}, err
	}
	if call.UpdatedAt, err = scanTime(updatedAt); err != nil {
		return ToolCall{}, err
	}
	return call, nil
}

// Save uses ToolCall.Version as an optimistic concurrency token.
func (r *ToolCallRepository) Save(ctx context.Context, call ToolCall) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := validateToolCall(call); err != nil {
		return err
	}
	if call.Version < 2 {
		return fmt.Errorf("%w: tool call version must be greater than one when saving", domain.ErrInvalidArgument)
	}
	updatedAt, err := timeValue(call.UpdatedAt)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE tool_calls SET
			run_id = ?, tool_id = ?, args_redacted = ?, risk = ?, approval_id = ?, status = ?,
			result_ref = ?, idempotency_key = ?, version = ?, updated_at = ?
		WHERE id = ? AND version = ?`,
		string(call.RunID), strings.TrimSpace(call.ToolID), call.ArgsRedacted, string(call.Risk), nullableID(call.ApprovalID),
		strings.TrimSpace(call.Status), call.ResultRef, call.IdempotencyKey, call.Version, updatedAt,
		string(call.ID), call.Version-1)
	if err != nil {
		return wrappedSQLError("save tool call", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrappedSQLError("count saved tool call", err)
	}
	if count == 1 {
		return nil
	}
	if _, err := r.get(ctx, string(call.ID)); err != nil {
		return err
	}
	return domain.ErrConflict
}

func (r *ToolCallRepository) ListByRun(ctx context.Context, runID domain.ID, limit ...int) ([]ToolCall, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if runID.Empty() {
		return nil, fmt.Errorf("%w: run id is required", domain.ErrInvalidArgument)
	}
	query := `SELECT id FROM tool_calls WHERE run_id = ? ORDER BY created_at ASC, id ASC`
	args := []any{string(runID)}
	if len(limit) > 0 && limit[0] > 0 {
		query += " LIMIT ?"
		args = append(args, limit[0])
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list tool calls", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, wrappedSQLError("scan tool call id", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, wrappedSQLError("iterate tool calls", err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrappedSQLError("close tool calls", err)
	}
	result := make([]ToolCall, 0, len(ids))
	for _, id := range ids {
		item, err := r.get(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func (r *ToolCallRepository) FindByIdempotencyKey(ctx context.Context, runID domain.ID, key string) (ToolCall, error) {
	if err := requireDatabase(r.db); err != nil {
		return ToolCall{}, err
	}
	if err := contextErr(ctx); err != nil {
		return ToolCall{}, err
	}
	if runID.Empty() || strings.TrimSpace(key) == "" {
		return ToolCall{}, fmt.Errorf("%w: run id and idempotency key are required", domain.ErrInvalidArgument)
	}
	var id string
	err := r.db.QueryRowContext(ctx, `
		SELECT id FROM tool_calls WHERE run_id = ? AND idempotency_key = ?`, string(runID), key).Scan(&id)
	if err != nil {
		return ToolCall{}, wrappedSQLError("find tool call by idempotency key", err)
	}
	return r.get(ctx, id)
}

func validateToolCall(call ToolCall) error {
	if call.ID.Empty() || call.RunID.Empty() || strings.TrimSpace(call.ToolID) == "" || !call.Risk.Valid() ||
		strings.TrimSpace(call.Status) == "" || call.Version == 0 || call.CreatedAt.IsZero() || call.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: invalid tool call", domain.ErrInvalidArgument)
	}
	if err := validJSON(call.ArgsRedacted, "args_redacted"); err != nil {
		return err
	}
	return nil
}
