package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// AuditRepository stores append-only redacted action metadata.
type AuditRepository struct {
	db *sql.DB
}

func NewAuditRepository(database *sql.DB) *AuditRepository {
	return &AuditRepository{db: database}
}

func (r *AuditRepository) Append(ctx context.Context, event AuditEvent) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := validateAuditEvent(event); err != nil {
		return err
	}
	createdAt, err := timeValue(event.CreatedAt)
	if err != nil {
		return err
	}
	payload := strings.TrimSpace(event.PayloadRedacted)
	if payload == "" {
		payload = "{}"
	}
	if err := validJSON(payload, "payload_redacted"); err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO audit_events(
			id, run_id, tool_call_id, approval_id, actor, action, target, decision,
			payload_redacted, duration_ms, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(event.ID), nullableID(event.RunID), nullableID(event.ToolCallID), nullableID(event.ApprovalID),
		string(event.Actor), event.Action, event.Target, string(event.Decision), payload,
		event.Duration.Milliseconds(), createdAt)
	return wrappedSQLError("append audit event", err)
}

// Create is an alias for Append for adapters that use the repository naming
// convention shared by the other SQLite records.
func (r *AuditRepository) Create(ctx context.Context, event AuditEvent) error {
	return r.Append(ctx, event)
}

func (r *AuditRepository) Get(ctx context.Context, id domain.ID) (AuditEvent, error) {
	if err := requireDatabase(r.db); err != nil {
		return AuditEvent{}, err
	}
	if err := contextErr(ctx); err != nil {
		return AuditEvent{}, err
	}
	if id.Empty() {
		return AuditEvent{}, fmt.Errorf("%w: audit event id is required", domain.ErrInvalidArgument)
	}
	return r.get(ctx, string(id))
}

func (r *AuditRepository) get(ctx context.Context, id string) (AuditEvent, error) {
	var (
		event                                    AuditEvent
		idValue, actor, action, target, decision string
		payload, createdAt                       string
		runID, toolCallID, approvalID            sql.NullString
		durationMS                               int64
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT id, run_id, tool_call_id, approval_id, actor, action, target, decision,
		       payload_redacted, duration_ms, created_at
		FROM audit_events WHERE id = ?`, id).Scan(
		&idValue, &runID, &toolCallID, &approvalID, &actor, &action, &target, &decision,
		&payload, &durationMS, &createdAt)
	if err != nil {
		return AuditEvent{}, wrappedSQLError("get audit event", err)
	}
	event.ID = domain.ID(idValue)
	if runID.Valid {
		event.RunID = domain.ID(runID.String)
	}
	if toolCallID.Valid {
		event.ToolCallID = domain.ID(toolCallID.String)
	}
	if approvalID.Valid {
		event.ApprovalID = domain.ID(approvalID.String)
	}
	event.Actor = domain.Actor(actor)
	event.Action = action
	event.Target = target
	event.Decision = domain.PermissionDecision(decision)
	event.PayloadRedacted = payload
	event.Duration = time.Duration(durationMS) * time.Millisecond
	if event.CreatedAt, err = scanTime(createdAt); err != nil {
		return AuditEvent{}, err
	}
	return event, nil
}

// List returns newest audit events first. The optional limit is an application
// bound and is not allowed to be negative.
func (r *AuditRepository) List(ctx context.Context, limit ...int) ([]AuditEvent, error) {
	return r.list(ctx, "", nil, limit...)
}

func (r *AuditRepository) ListByRun(ctx context.Context, runID domain.ID, limit ...int) ([]AuditEvent, error) {
	if runID.Empty() {
		return nil, fmt.Errorf("%w: run id is required", domain.ErrInvalidArgument)
	}
	return r.list(ctx, "run_id = ?", []any{string(runID)}, limit...)
}

func (r *AuditRepository) list(ctx context.Context, predicate string, args []any, limit ...int) ([]AuditEvent, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	query := "SELECT id FROM audit_events"
	if predicate != "" {
		query += " WHERE " + predicate
	}
	query += " ORDER BY created_at DESC, id DESC"
	if len(limit) > 0 {
		if limit[0] < 0 {
			return nil, fmt.Errorf("%w: audit limit cannot be negative", domain.ErrInvalidArgument)
		}
		if limit[0] > 0 {
			query += " LIMIT ?"
			args = append(args, limit[0])
		}
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list audit events", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, wrappedSQLError("scan audit event id", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, wrappedSQLError("iterate audit events", err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrappedSQLError("close audit events", err)
	}
	result := make([]AuditEvent, 0, len(ids))
	for _, id := range ids {
		item, err := r.get(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func validateAuditEvent(event AuditEvent) error {
	if event.ID.Empty() || !event.Actor.Valid() || strings.TrimSpace(event.Action) == "" || event.CreatedAt.IsZero() {
		return fmt.Errorf("%w: invalid audit event", domain.ErrInvalidArgument)
	}
	if event.Duration < 0 {
		return fmt.Errorf("%w: audit duration cannot be negative", domain.ErrInvalidArgument)
	}
	if event.Decision != "" && event.Decision != domain.PermissionAllow && event.Decision != domain.PermissionDeny && event.Decision != domain.PermissionNeedsApproval {
		return fmt.Errorf("%w: invalid audit decision %q", domain.ErrInvalidArgument, event.Decision)
	}
	return nil
}
