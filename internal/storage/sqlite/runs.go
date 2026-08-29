package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// RunRepository is the SQLite implementation of domain.RunRepository.
type RunRepository struct {
	db *sql.DB
}

func NewRunRepository(database *sql.DB) *RunRepository {
	return &RunRepository{db: database}
}

var _ domain.RunRepository = (*RunRepository)(nil)

func (r *RunRepository) Create(ctx context.Context, run domain.AgentRun) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := validateRun(run); err != nil {
		return err
	}
	run, err := r.resolveOwnership(ctx, run)
	if err != nil {
		return err
	}
	createdAt, err := timeValue(run.CreatedAt)
	if err != nil {
		return err
	}
	updatedAt, err := timeValue(run.UpdatedAt)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO agent_runs(
			id, agent_id, kind, conversation_id, parent_run_id, state,
			max_steps, max_tokens, max_tool_calls, max_tool_output_bytes, max_duration_seconds,
			failure, version, created_at, updated_at, started_at, finished_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(run.ID), string(run.AgentID), string(run.Kind), nullableID(run.ConversationID), nullableID(run.ParentRunID),
		string(run.State), run.Budget.MaxSteps, run.Budget.MaxTokens, run.Budget.MaxToolCalls, run.Budget.MaxToolOutputBytes,
		run.Budget.MaxDurationSeconds, nullableStringValue(run.Failure), run.Version, createdAt, updatedAt,
		nullableTimeValue(run.StartedAt), nullableTimeValue(run.FinishedAt))
	return wrappedSQLError("create run", err)
}

func (r *RunRepository) Get(ctx context.Context, id domain.ID) (domain.AgentRun, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.AgentRun{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.AgentRun{}, err
	}
	if id.Empty() {
		return domain.AgentRun{}, fmt.Errorf("%w: run id is required", domain.ErrInvalidArgument)
	}
	return r.get(ctx, string(id))
}

func (r *RunRepository) get(ctx context.Context, id string) (domain.AgentRun, error) {
	var (
		run                                              domain.AgentRun
		idValue, agentID, kind, conversationID, parentID string
		state, failure, createdAt, updatedAt             string
		startedAt, finishedAt                            sql.NullString
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT id, agent_id, kind, conversation_id, parent_run_id, state,
		       max_steps, max_tokens, max_tool_calls, max_tool_output_bytes, max_duration_seconds,
		       failure, version, created_at, updated_at, started_at, finished_at
		FROM agent_runs WHERE id = ?`, id).Scan(
		&idValue, &agentID, &kind, &nullableString{Value: &conversationID}, &nullableString{Value: &parentID}, &state,
		&run.Budget.MaxSteps, &run.Budget.MaxTokens, &run.Budget.MaxToolCalls, &run.Budget.MaxToolOutputBytes, &run.Budget.MaxDurationSeconds,
		&nullableString{Value: &failure}, &run.Version, &createdAt, &updatedAt, &startedAt, &finishedAt)
	if err != nil {
		return domain.AgentRun{}, wrappedSQLError("get run", err)
	}
	run.ID = domain.ID(idValue)
	run.AgentID = domain.ID(agentID)
	run.Kind = domain.RunKind(kind)
	run.ConversationID = domain.ID(conversationID)
	run.ParentRunID = domain.ID(parentID)
	run.State = domain.RunState(state)
	run.Failure = failure
	if run.CreatedAt, err = scanTime(createdAt); err != nil {
		return domain.AgentRun{}, err
	}
	if run.UpdatedAt, err = scanTime(updatedAt); err != nil {
		return domain.AgentRun{}, err
	}
	if run.StartedAt, err = scanNullableTime(startedAt); err != nil {
		return domain.AgentRun{}, err
	}
	if run.FinishedAt, err = scanNullableTime(finishedAt); err != nil {
		return domain.AgentRun{}, err
	}
	return run, nil
}

// Save uses AgentRun.Version as an optimistic concurrency token. A caller
// must save the next version produced by a domain transition.
func (r *RunRepository) Save(ctx context.Context, run domain.AgentRun) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := validateRun(run); err != nil {
		return err
	}
	run, err := r.resolveOwnership(ctx, run)
	if err != nil {
		return err
	}
	if run.Version < 2 {
		return fmt.Errorf("%w: run version must be greater than one when saving", domain.ErrInvalidArgument)
	}
	updatedAt, err := timeValue(run.UpdatedAt)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE agent_runs SET
			agent_id = ?, kind = ?, conversation_id = ?, parent_run_id = ?, state = ?,
			max_steps = ?, max_tokens = ?, max_tool_calls = ?, max_tool_output_bytes = ?, max_duration_seconds = ?,
			failure = ?, version = ?, updated_at = ?, started_at = ?, finished_at = ?
		WHERE id = ? AND version = ?`,
		string(run.AgentID), string(run.Kind), nullableID(run.ConversationID), nullableID(run.ParentRunID), string(run.State),
		run.Budget.MaxSteps, run.Budget.MaxTokens, run.Budget.MaxToolCalls, run.Budget.MaxToolOutputBytes, run.Budget.MaxDurationSeconds,
		nullableStringValue(run.Failure), run.Version, updatedAt, nullableTimeValue(run.StartedAt), nullableTimeValue(run.FinishedAt),
		string(run.ID), run.Version-1)
	if err != nil {
		return wrappedSQLError("save run", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrappedSQLError("count saved run", err)
	}
	if count == 1 {
		return nil
	}
	if _, err := r.get(ctx, string(run.ID)); err != nil {
		return err
	}
	return domain.ErrConflict
}

// ListByConversation returns runs in creation order. A nil/empty conversation
// id is rejected because a run without a conversation is only valid for
// explicitly background callers, which should use ListByKind or Get.
func (r *RunRepository) ListByConversation(ctx context.Context, conversationID domain.ID, limit ...int) ([]domain.AgentRun, error) {
	if conversationID.Empty() {
		return nil, fmt.Errorf("%w: conversation id is required", domain.ErrInvalidArgument)
	}
	return r.list(ctx, "conversation_id = ?", []any{string(conversationID)}, limit...)
}

func (r *RunRepository) ListByKind(ctx context.Context, kind domain.RunKind, limit ...int) ([]domain.AgentRun, error) {
	if !kind.Valid() {
		return nil, fmt.Errorf("%w: invalid run kind %q", domain.ErrInvalidArgument, kind)
	}
	return r.list(ctx, "kind = ?", []any{string(kind)}, limit...)
}

// ListByAgent returns runs owned by one named top-level agent. The filter is
// applied in SQLite so a caller cannot accidentally mix another agent's
// execution history while assembling activity or recovery views.
func (r *RunRepository) ListByAgent(ctx context.Context, agentID domain.ID, limit ...int) ([]domain.AgentRun, error) {
	if agentID.Empty() {
		return nil, fmt.Errorf("%w: agent id is required", domain.ErrInvalidArgument)
	}
	return r.list(ctx, "agent_id = ?", []any{strings.TrimSpace(string(agentID))}, limit...)
}

func (r *RunRepository) list(ctx context.Context, predicate string, args []any, limit ...int) ([]domain.AgentRun, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	query := `
		SELECT id FROM agent_runs WHERE ` + predicate + `
		ORDER BY created_at ASC, id ASC`
	if len(limit) > 0 && limit[0] > 0 {
		query += " LIMIT ?"
		args = append(args, limit[0])
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list runs", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, wrappedSQLError("scan run id", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, wrappedSQLError("iterate runs", err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrappedSQLError("close runs", err)
	}
	result := make([]domain.AgentRun, 0, len(ids))
	for _, id := range ids {
		item, err := r.get(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func validateRun(run domain.AgentRun) error {
	if run.ID.Empty() || !run.Kind.Valid() || !run.State.Valid() || !run.Budget.Valid() {
		return fmt.Errorf("%w: invalid run", domain.ErrInvalidArgument)
	}
	if run.Version == 0 {
		return fmt.Errorf("%w: run version must be positive", domain.ErrInvalidArgument)
	}
	if run.CreatedAt.IsZero() || run.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: run timestamps are required", domain.ErrInvalidArgument)
	}
	return nil
}

// resolveOwnership fills the agent for legacy NewRun callers from the
// conversation and checks that explicit ownership cannot cross an agent
// boundary. Parent runs must remain in the same ownership domain as their
// child; subagent-specific delegation is represented by a later stage and is
// deliberately not inferred here.
func (r *RunRepository) resolveOwnership(ctx context.Context, run domain.AgentRun) (domain.AgentRun, error) {
	if run.AgentID.Empty() {
		if run.ConversationID.Empty() {
			return domain.AgentRun{}, fmt.Errorf("%w: run agent id is required", domain.ErrInvalidArgument)
		}
		var agentID string
		if err := r.db.QueryRowContext(ctx,
			"SELECT agent_id FROM conversations WHERE id = ?", string(run.ConversationID)).Scan(&agentID); err != nil {
			return domain.AgentRun{}, wrappedSQLError("resolve run conversation owner", err)
		}
		if strings.TrimSpace(agentID) == "" {
			return domain.AgentRun{}, fmt.Errorf("%w: conversation agent id is required", domain.ErrInvalidArgument)
		}
		run.AgentID = domain.ID(strings.TrimSpace(agentID))
	} else {
		run.AgentID = domain.ID(strings.TrimSpace(string(run.AgentID)))
	}

	if !run.ConversationID.Empty() {
		var conversationAgent string
		if err := r.db.QueryRowContext(ctx,
			"SELECT agent_id FROM conversations WHERE id = ?", string(run.ConversationID)).Scan(&conversationAgent); err != nil {
			return domain.AgentRun{}, wrappedSQLError("resolve run conversation owner", err)
		}
		conversationAgent = strings.TrimSpace(conversationAgent)
		if conversationAgent == "" {
			return domain.AgentRun{}, fmt.Errorf("%w: conversation agent id is required", domain.ErrInvalidArgument)
		}
		if conversationAgent != string(run.AgentID) {
			return domain.AgentRun{}, fmt.Errorf("%w: run agent does not own conversation", domain.ErrConflict)
		}
	}

	if !run.ParentRunID.Empty() {
		var parentAgent string
		if err := r.db.QueryRowContext(ctx,
			"SELECT agent_id FROM agent_runs WHERE id = ?", string(run.ParentRunID)).Scan(&parentAgent); err != nil {
			return domain.AgentRun{}, wrappedSQLError("resolve parent run owner", err)
		}
		parentAgent = strings.TrimSpace(parentAgent)
		if parentAgent == "" {
			return domain.AgentRun{}, fmt.Errorf("%w: parent run agent id is required", domain.ErrInvalidArgument)
		}
		if parentAgent != string(run.AgentID) {
			return domain.AgentRun{}, fmt.Errorf("%w: child run agent does not match parent run", domain.ErrConflict)
		}
	}
	return run, nil
}

func nullableID(value domain.ID) any {
	if value.Empty() {
		return nil
	}
	return string(value)
}

func nullableStringValue(value string) any {
	if value == "" {
		return nil
	}
	return value
}
