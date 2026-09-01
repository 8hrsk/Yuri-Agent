package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// DelegationRepository persists bounded anonymous child-execution metadata.
// It deliberately has no methods for profile/persona/memory creation.
type DelegationRepository struct {
	db *sql.DB
}

func NewDelegationRepository(database *sql.DB) *DelegationRepository {
	return &DelegationRepository{db: database}
}

func (r *DelegationRepository) Create(ctx context.Context, delegation domain.Delegation) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := validateDelegation(delegation); err != nil {
		return err
	}
	if err := r.validateParent(ctx, delegation.PrincipalAgentID, delegation.ParentRunID); err != nil {
		return err
	}
	if err := r.validateChild(ctx, delegation); err != nil {
		return err
	}
	if existing, err := r.FindByIdempotencyKey(ctx, delegation.PrincipalAgentID, delegation.ParentRunID, delegation.IdempotencyKey); err == nil && !existing.ID.Empty() {
		return domain.ErrConflict
	} else if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin create delegation", err)
	}
	defer tx.Rollback()
	if err := insertDelegationTx(ctx, tx, delegation); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit create delegation", err)
	}
	return nil
}

func insertDelegationTx(ctx context.Context, tx *sql.Tx, delegation domain.Delegation) error {
	scope := normalizeDelegationScope(delegation.ScopeJSON)
	createdAt, err := timeValue(delegation.CreatedAt)
	if err != nil {
		return err
	}
	updatedAt, err := timeValue(delegation.UpdatedAt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO delegations(
			id, child_run_id, principal_agent_id, parent_run_id, scope_json, depth, status,
			max_steps, max_tokens, max_tool_calls, max_tool_output_bytes, max_duration_seconds,
			idempotency_key, request_hash, result_text, failure, version, created_at, updated_at, started_at, finished_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(delegation.ID), string(delegation.ChildRunID), string(delegation.PrincipalAgentID), string(delegation.ParentRunID), scope,
		delegation.Depth, string(delegation.Status), delegation.Budget.MaxSteps, delegation.Budget.MaxTokens,
		delegation.Budget.MaxToolCalls, delegation.Budget.MaxToolOutputBytes, delegation.Budget.MaxDurationSeconds,
		delegation.IdempotencyKey, delegation.RequestHash, delegation.ResultText, delegation.Failure, delegation.Version, createdAt, updatedAt,
		nullableTimeValue(delegation.StartedAt), nullableTimeValue(delegation.FinishedAt))
	return wrappedSQLError("create delegation", err)
}

// CreateDelegationWithChild atomically creates the anonymous child run and
// its delegation record. If idempotency or ownership validation fails, no
// orphan agent_runs row is left behind.
func (repositories *Repositories) CreateDelegationWithChild(ctx context.Context, child domain.AgentRun, delegation domain.Delegation) error {
	if repositories == nil || repositories.Delegations == nil || repositories.Runs == nil {
		return fmt.Errorf("%w: delegation repositories are unavailable", domain.ErrInvalidArgument)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := validateRun(child); err != nil {
		return err
	}
	if child.AgentID.Empty() || child.Kind != domain.RunKindSubagent || child.ParentRunID.Empty() {
		return fmt.Errorf("%w: child must be an owned subagent run with a parent", domain.ErrInvalidArgument)
	}
	if delegation.ChildRunID != child.ID || delegation.PrincipalAgentID != child.AgentID || delegation.ParentRunID != child.ParentRunID {
		return fmt.Errorf("%w: delegation and child ownership do not match", domain.ErrInvalidArgument)
	}
	if expected, ok := delegationStatusForRun(child.State); !ok || delegation.Status != expected || child.Version != 1 || delegation.Version != 1 {
		return fmt.Errorf("%w: new delegation child must start in created state at version one", domain.ErrInvalidArgument)
	}
	if err := delegation.Validate(); err != nil {
		return err
	}
	tx, err := repositories.Delegations.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin create delegation child", err)
	}
	defer tx.Rollback()
	if err := validateParentTx(ctx, tx, child.AgentID, child.ParentRunID); err != nil {
		return err
	}
	if !child.ConversationID.Empty() {
		var conversationAgent string
		if err := tx.QueryRowContext(ctx, `SELECT agent_id FROM conversations WHERE id = ?`, string(child.ConversationID)).Scan(&conversationAgent); err != nil {
			return wrappedSQLError("validate delegation child conversation", err)
		}
		if strings.TrimSpace(conversationAgent) != string(child.AgentID) {
			return domain.ErrConflict
		}
	}
	var existing string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM agent_runs WHERE id = ?`, string(child.ID)).Scan(&existing); err == nil {
		return domain.ErrConflict
	} else if !isNoRows(err) {
		return wrappedSQLError("check delegation child run", err)
	}
	var duplicate string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM delegations WHERE principal_agent_id = ? AND parent_run_id = ? AND idempotency_key = ?`, string(delegation.PrincipalAgentID), string(delegation.ParentRunID), delegation.IdempotencyKey).Scan(&duplicate); err == nil {
		return domain.ErrConflict
	} else if !isNoRows(err) {
		return wrappedSQLError("check delegation idempotency", err)
	}
	if err := insertRunTx(ctx, tx, child); err != nil {
		return err
	}
	if err := insertDelegationTx(ctx, tx, delegation); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit create delegation child", err)
	}
	return nil
}

// SaveDelegationWithChild updates the child run and delegation lifecycle as a
// single optimistic transaction. Both versions must advance together; a
// stale retry therefore cannot leave the pair out of sync.
func (repositories *Repositories) SaveDelegationWithChild(ctx context.Context, child domain.AgentRun, delegation domain.Delegation) error {
	if repositories == nil || repositories.Delegations == nil || repositories.Runs == nil {
		return fmt.Errorf("%w: delegation repositories are unavailable", domain.ErrInvalidArgument)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := validateRun(child); err != nil {
		return err
	}
	if child.AgentID.Empty() || child.Kind != domain.RunKindSubagent || child.ParentRunID.Empty() {
		return fmt.Errorf("%w: child must be an owned subagent run with a parent", domain.ErrInvalidArgument)
	}
	if delegation.ChildRunID != child.ID || delegation.PrincipalAgentID != child.AgentID || delegation.ParentRunID != child.ParentRunID {
		return fmt.Errorf("%w: delegation and child ownership do not match", domain.ErrInvalidArgument)
	}
	if err := delegation.Validate(); err != nil {
		return err
	}
	expectedStatus, ok := delegationStatusForRun(child.State)
	if !ok || delegation.Status != expectedStatus {
		return fmt.Errorf("%w: child and delegation statuses do not match", domain.ErrInvalidArgument)
	}
	if child.Version < 2 || delegation.Version < 2 {
		return fmt.Errorf("%w: child and delegation versions must advance", domain.ErrInvalidArgument)
	}
	tx, err := repositories.Delegations.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin save delegation child", err)
	}
	defer tx.Rollback()
	if err := validateParentTx(ctx, tx, child.AgentID, child.ParentRunID); err != nil {
		return err
	}
	var childVersion uint64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM agent_runs WHERE id = ?`, string(child.ID)).Scan(&childVersion); err != nil {
		return wrappedSQLError("get delegation child version", err)
	}
	var delegationVersion uint64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM delegations WHERE id = ? AND child_run_id = ? AND principal_agent_id = ?`, string(delegation.ID), string(child.ID), string(delegation.PrincipalAgentID)).Scan(&delegationVersion); err != nil {
		return wrappedSQLError("get delegation version", err)
	}
	if child.Version != childVersion+1 || delegation.Version != delegationVersion+1 {
		return domain.ErrConflict
	}
	updatedAt, err := timeValue(child.UpdatedAt)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_runs SET state = ?, max_steps = ?, max_tokens = ?, max_tool_calls = ?,
			max_tool_output_bytes = ?, max_duration_seconds = ?, provider_id = ?, model = ?,
			input_tokens = ?, output_tokens = ?, total_tokens = ?, failure = ?,
			failure_kind = ?, failure_retryable = ?, failure_retry_after_seconds = ?,
			version = ?, updated_at = ?, started_at = ?, finished_at = ?
		WHERE id = ? AND version = ?`, string(child.State), child.Budget.MaxSteps, child.Budget.MaxTokens, child.Budget.MaxToolCalls,
		child.Budget.MaxToolOutputBytes, child.Budget.MaxDurationSeconds, strings.TrimSpace(child.Inference.ProviderID), strings.TrimSpace(child.Inference.Model),
		child.Usage.InputTokens, child.Usage.OutputTokens, child.Usage.TotalTokens, nullableStringValue(child.Failure),
		string(child.FailureInfo.Kind), child.FailureInfo.Retryable, child.FailureInfo.RetryAfterSeconds, child.Version, updatedAt,
		nullableTimeValue(child.StartedAt), nullableTimeValue(child.FinishedAt), string(child.ID), childVersion); err != nil {
		return wrappedSQLError("save delegation child", err)
	}
	delegationUpdatedAt, err := timeValue(delegation.UpdatedAt)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE delegations SET scope_json = ?, status = ?, max_steps = ?, max_tokens = ?, max_tool_calls = ?,
			max_tool_output_bytes = ?, max_duration_seconds = ?, idempotency_key = ?, request_hash = ?, result_text = ?, failure = ?,
			version = ?, updated_at = ?, started_at = ?, finished_at = ?
		WHERE id = ? AND version = ?`, normalizeDelegationScope(delegation.ScopeJSON), string(delegation.Status), delegation.Budget.MaxSteps,
		delegation.Budget.MaxTokens, delegation.Budget.MaxToolCalls, delegation.Budget.MaxToolOutputBytes, delegation.Budget.MaxDurationSeconds,
		delegation.IdempotencyKey, delegation.RequestHash, delegation.ResultText, delegation.Failure, delegation.Version, delegationUpdatedAt,
		nullableTimeValue(delegation.StartedAt), nullableTimeValue(delegation.FinishedAt), string(delegation.ID), delegationVersion)
	if err != nil {
		return wrappedSQLError("save delegation", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return wrappedSQLError("count saved delegation", err)
	} else if affected != 1 {
		return domain.ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit save delegation child", err)
	}
	return nil
}

func delegationStatusForRun(state domain.RunState) (domain.DelegationStatus, bool) {
	switch state {
	case domain.RunStateCreated:
		return domain.DelegationStatusCreated, true
	case domain.RunStateQueued:
		return domain.DelegationStatusQueued, true
	case domain.RunStateRunning:
		return domain.DelegationStatusRunning, true
	case domain.RunStateCancelling:
		return domain.DelegationStatusCancelling, true
	case domain.RunStateCompleted:
		return domain.DelegationStatusCompleted, true
	case domain.RunStateFailed:
		return domain.DelegationStatusFailed, true
	case domain.RunStateCancelled:
		return domain.DelegationStatusCancelled, true
	default:
		return "", false
	}
}

func (r *DelegationRepository) Get(ctx context.Context, id domain.ID) (domain.Delegation, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.Delegation{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.Delegation{}, err
	}
	if id.Empty() {
		return domain.Delegation{}, fmt.Errorf("%w: delegation id is required", domain.ErrInvalidArgument)
	}
	return r.get(ctx, string(id))
}

func (r *DelegationRepository) GetForPrincipal(ctx context.Context, principalAgentID, id domain.ID) (domain.Delegation, error) {
	delegation, err := r.Get(ctx, id)
	if err != nil {
		return domain.Delegation{}, err
	}
	if delegation.PrincipalAgentID != principalAgentID {
		return domain.Delegation{}, domain.ErrNotFound
	}
	return delegation, nil
}

// delegationSelect lists the full record so Get and list share one scanner and
// one round-trip per query.
const delegationSelect = `
	SELECT id, child_run_id, principal_agent_id, parent_run_id, scope_json, depth, status,
	       max_steps, max_tokens, max_tool_calls, max_tool_output_bytes, max_duration_seconds,
	       idempotency_key, request_hash, result_text, failure, version, created_at, updated_at, started_at, finished_at
	FROM delegations`

func (r *DelegationRepository) get(ctx context.Context, id string) (domain.Delegation, error) {
	return scanDelegation(r.db.QueryRowContext(ctx, delegationSelect+` WHERE id = ?`, id))
}

func scanDelegation(row rowScanner) (domain.Delegation, error) {
	var (
		delegation                                             domain.Delegation
		idValue, childID, principalID, parentID, scope, status string
		createdAt, updatedAt, startedAt, finishedAt            sql.NullString
	)
	err := row.Scan(
		&idValue, &childID, &principalID, &parentID, &scope, &delegation.Depth, &status,
		&delegation.Budget.MaxSteps, &delegation.Budget.MaxTokens, &delegation.Budget.MaxToolCalls,
		&delegation.Budget.MaxToolOutputBytes, &delegation.Budget.MaxDurationSeconds, &delegation.IdempotencyKey, &delegation.RequestHash,
		&delegation.ResultText, &delegation.Failure, &delegation.Version, &createdAt, &updatedAt, &startedAt, &finishedAt)
	if err != nil {
		return domain.Delegation{}, wrappedSQLError("get delegation", err)
	}
	delegation.ID = domain.ID(idValue)
	delegation.ChildRunID = domain.ID(childID)
	delegation.PrincipalAgentID = domain.ID(principalID)
	delegation.ParentRunID = domain.ID(parentID)
	delegation.ScopeJSON = scope
	delegation.Status = domain.DelegationStatus(status)
	var timeErr error
	if delegation.CreatedAt, timeErr = scanTime(createdAt.String); timeErr != nil {
		return domain.Delegation{}, timeErr
	}
	if delegation.UpdatedAt, timeErr = scanTime(updatedAt.String); timeErr != nil {
		return domain.Delegation{}, timeErr
	}
	if delegation.StartedAt, timeErr = scanNullableTime(startedAt); timeErr != nil {
		return domain.Delegation{}, timeErr
	}
	if delegation.FinishedAt, timeErr = scanNullableTime(finishedAt); timeErr != nil {
		return domain.Delegation{}, timeErr
	}
	if err := validateDelegation(delegation); err != nil {
		return domain.Delegation{}, err
	}
	return delegation, nil
}

func (r *DelegationRepository) Save(ctx context.Context, delegation domain.Delegation) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := validateDelegation(delegation); err != nil {
		return err
	}
	if delegation.Version < 2 {
		return fmt.Errorf("%w: delegation version must be greater than one when saving", domain.ErrInvalidArgument)
	}
	if err := r.validateParent(ctx, delegation.PrincipalAgentID, delegation.ParentRunID); err != nil {
		return err
	}
	if err := r.validateChild(ctx, delegation); err != nil {
		return err
	}
	updatedAt, err := timeValue(delegation.UpdatedAt)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE delegations SET
			child_run_id = ?, principal_agent_id = ?, parent_run_id = ?, scope_json = ?, depth = ?, status = ?,
			max_steps = ?, max_tokens = ?, max_tool_calls = ?, max_tool_output_bytes = ?, max_duration_seconds = ?,
			idempotency_key = ?, request_hash = ?, result_text = ?, failure = ?, version = ?, updated_at = ?, started_at = ?, finished_at = ?
		WHERE id = ? AND version = ?`,
		string(delegation.ChildRunID), string(delegation.PrincipalAgentID), string(delegation.ParentRunID), normalizeDelegationScope(delegation.ScopeJSON), delegation.Depth,
		string(delegation.Status), delegation.Budget.MaxSteps, delegation.Budget.MaxTokens, delegation.Budget.MaxToolCalls,
		delegation.Budget.MaxToolOutputBytes, delegation.Budget.MaxDurationSeconds, delegation.IdempotencyKey, delegation.RequestHash,
		delegation.ResultText, delegation.Failure, delegation.Version, updatedAt, nullableTimeValue(delegation.StartedAt), nullableTimeValue(delegation.FinishedAt), string(delegation.ID), delegation.Version-1)
	if err != nil {
		return wrappedSQLError("save delegation", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrappedSQLError("count saved delegation", err)
	}
	if count == 1 {
		return nil
	}
	if _, err := r.Get(ctx, delegation.ID); err != nil {
		return err
	}
	return domain.ErrConflict
}

func (r *DelegationRepository) FindByIdempotencyKey(ctx context.Context, principalAgentID, parentRunID domain.ID, key string) (domain.Delegation, error) {
	if principalAgentID.Empty() || parentRunID.Empty() || strings.TrimSpace(key) == "" {
		return domain.Delegation{}, fmt.Errorf("%w: delegation idempotency scope is required", domain.ErrInvalidArgument)
	}
	return scanDelegation(r.db.QueryRowContext(ctx,
		delegationSelect+` WHERE principal_agent_id = ? AND parent_run_id = ? AND idempotency_key = ?`,
		string(principalAgentID), string(parentRunID), strings.TrimSpace(key)))
}

func (r *DelegationRepository) ListByParent(ctx context.Context, principalAgentID, parentRunID domain.ID, window ...int) ([]domain.Delegation, error) {
	if principalAgentID.Empty() || parentRunID.Empty() {
		return nil, fmt.Errorf("%w: delegation ownership ids are required", domain.ErrInvalidArgument)
	}
	return r.list(ctx, "principal_agent_id = ? AND parent_run_id = ?", []any{string(principalAgentID), string(parentRunID)}, window...)
}

func (r *DelegationRepository) ListByPrincipal(ctx context.Context, principalAgentID domain.ID, window ...int) ([]domain.Delegation, error) {
	if principalAgentID.Empty() {
		return nil, fmt.Errorf("%w: principal agent id is required", domain.ErrInvalidArgument)
	}
	return r.list(ctx, "principal_agent_id = ?", []any{string(principalAgentID)}, window...)
}

// list reads the matching delegations in one query. The deferred Close is what
// keeps a Scan error from stranding the pool's single connection, which would
// hang every other database caller in the process.
func (r *DelegationRepository) list(ctx context.Context, predicate string, args []any, window ...int) ([]domain.Delegation, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	limit, offset, err := listWindow("delegation", window)
	if err != nil {
		return nil, err
	}
	query := delegationSelect + ` WHERE ` + predicate + ` ORDER BY created_at ASC, id ASC`
	query, args = appendWindow(query, args, limit, offset)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list delegations", err)
	}
	defer rows.Close()
	result := make([]domain.Delegation, 0)
	for rows.Next() {
		item, scanErr := scanDelegation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate delegations", err)
	}
	return result, nil
}

func (r *DelegationRepository) validateParent(ctx context.Context, principalAgentID, parentRunID domain.ID) error {
	var agentID, kind, ancestorID string
	if err := r.db.QueryRowContext(ctx, `SELECT agent_id, kind, parent_run_id FROM agent_runs WHERE id = ?`, string(parentRunID)).Scan(&agentID, &kind, &nullableString{Value: &ancestorID}); err != nil {
		return wrappedSQLError("validate delegation parent", err)
	}
	if strings.TrimSpace(agentID) != string(principalAgentID) || domain.RunKind(strings.TrimSpace(kind)) == domain.RunKindSubagent || strings.TrimSpace(ancestorID) != "" {
		return domain.ErrConflict
	}
	return nil
}

func validateParentTx(ctx context.Context, tx *sql.Tx, principalAgentID, parentRunID domain.ID) error {
	var agentID, kind, ancestorID string
	if err := tx.QueryRowContext(ctx, `SELECT agent_id, kind, parent_run_id FROM agent_runs WHERE id = ?`, string(parentRunID)).Scan(&agentID, &kind, &nullableString{Value: &ancestorID}); err != nil {
		return wrappedSQLError("validate delegation parent", err)
	}
	if strings.TrimSpace(agentID) != string(principalAgentID) || domain.RunKind(strings.TrimSpace(kind)) == domain.RunKindSubagent || strings.TrimSpace(ancestorID) != "" {
		return domain.ErrConflict
	}
	return nil
}

func (r *DelegationRepository) validateChild(ctx context.Context, delegation domain.Delegation) error {
	var agentID, kind, conversationID, parentID string
	if err := r.db.QueryRowContext(ctx, `SELECT agent_id, parent_run_id, kind, conversation_id FROM agent_runs WHERE id = ?`, string(delegation.ChildRunID)).Scan(&agentID, &nullableString{Value: &parentID}, &kind, &nullableString{Value: &conversationID}); err != nil {
		return wrappedSQLError("validate delegation child", err)
	}
	if strings.TrimSpace(agentID) != string(delegation.PrincipalAgentID) || strings.TrimSpace(parentID) != string(delegation.ParentRunID) || domain.RunKind(strings.TrimSpace(kind)) != domain.RunKindSubagent || strings.TrimSpace(conversationID) != "" {
		return domain.ErrConflict
	}
	return nil
}

func validateDelegation(delegation domain.Delegation) error {
	if err := delegation.Validate(); err != nil {
		return err
	}
	return nil
}

func normalizeDelegationScope(scope string) string {
	if strings.TrimSpace(scope) == "" {
		return "{}"
	}
	return strings.TrimSpace(scope)
}

var _ interface {
	Create(context.Context, domain.Delegation) error
	Get(context.Context, domain.ID) (domain.Delegation, error)
	Save(context.Context, domain.Delegation) error
} = (*DelegationRepository)(nil)
