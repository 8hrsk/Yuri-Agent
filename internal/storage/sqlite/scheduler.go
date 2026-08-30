package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// SchedulerRepository is the SQLite implementation of the durable scheduler
// port. Claim, lease, retry, and misfire operations use compare-and-set
// updates inside transactions so two workers cannot execute the same queued
// invocation at the same time.
type SchedulerRepository struct {
	db *sql.DB
}

func NewSchedulerRepository(database *sql.DB) *SchedulerRepository {
	return &SchedulerRepository{db: database}
}

var _ domain.SchedulerRepository = (*SchedulerRepository)(nil)

const scheduleSelect = `SELECT id, name, kind, expression, timezone, start_at,
       interval_seconds, payload_json, status, enabled, misfire_policy,
       next_run_at, last_run_at, retry_max_attempts,
       retry_initial_backoff_seconds, retry_max_backoff_seconds,
       max_duration_seconds, max_tokens, max_tool_calls, allow_overlap,
       history_limit, version, created_at, updated_at FROM schedules`

const jobRunSelect = `SELECT id, schedule_id, state, trigger, attempt,
       execution_key, lease_owner, lease_token, lease_until, scheduled_for,
       retry_at, started_at, finished_at, error, result_ref, version,
       created_at, updated_at FROM job_runs`

func (r *SchedulerRepository) CreateSchedule(ctx context.Context, schedule domain.Schedule) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	schedule = normalizeSchedule(schedule)
	if err := validateScheduleForStorage(schedule); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO schedules(
			id, name, kind, expression, timezone, start_at, interval_seconds,
			payload_json, status, enabled, misfire_policy, next_run_at,
			last_run_at, retry_max_attempts, retry_initial_backoff_seconds,
			retry_max_backoff_seconds, max_duration_seconds, max_tokens,
			max_tool_calls, allow_overlap, history_limit, version, created_at,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(schedule.ID), schedule.Name, string(schedule.Kind), schedule.Expression,
		schedule.Timezone, formatTime(schedule.StartAt), schedule.IntervalSeconds,
		schedule.PayloadJSON, string(schedule.Status), boolInt(schedule.Enabled),
		string(schedule.MisfirePolicy), nullableTimeValue(schedule.NextRunAt),
		nullableTimeValue(schedule.LastRunAt), schedule.Retry.MaxAttempts,
		schedule.Retry.InitialBackoffSecond, schedule.Retry.MaxBackoffSecond,
		schedule.Budget.MaxDurationSeconds, schedule.Budget.MaxTokens,
		schedule.Budget.MaxToolCalls, boolInt(schedule.AllowOverlap), schedule.HistoryLimit,
		schedule.Version, formatTime(schedule.CreatedAt), formatTime(schedule.UpdatedAt))
	return wrappedSQLError("create schedule", err)
}

func (r *SchedulerRepository) GetSchedule(ctx context.Context, id domain.ID) (domain.Schedule, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.Schedule{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.Schedule{}, err
	}
	if id.Empty() {
		return domain.Schedule{}, fmt.Errorf("%w: schedule id is required", domain.ErrInvalidArgument)
	}
	return scanSchedule(r.db.QueryRowContext(ctx, scheduleSelect+" WHERE id = ?", string(id)))
}

func (r *SchedulerRepository) ListSchedules(ctx context.Context, options domain.ScheduleListOptions) ([]domain.Schedule, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if options.Limit < 0 || options.Offset < 0 {
		return nil, fmt.Errorf("%w: schedule pagination cannot be negative", domain.ErrInvalidArgument)
	}
	statuses := []string{string(domain.ScheduleStatusActive)}
	if options.IncludePaused {
		statuses = append(statuses, string(domain.ScheduleStatusPaused))
	}
	if options.IncludeCompleted {
		statuses = append(statuses, string(domain.ScheduleStatusCompleted))
	}
	if options.IncludeDeleted {
		statuses = append(statuses, string(domain.ScheduleStatusDeleted))
	}
	placeholders := make([]string, len(statuses))
	args := make([]any, 0, len(statuses)+2)
	for i, status := range statuses {
		placeholders[i] = "?"
		args = append(args, status)
	}
	query := scheduleSelect + " WHERE status IN (" + strings.Join(placeholders, ",") + ") ORDER BY created_at ASC, id ASC"
	query, args = appendWindow(query, args, options.Limit, options.Offset)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list schedules", err)
	}
	defer rows.Close()
	result := make([]domain.Schedule, 0)
	for rows.Next() {
		item, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate schedules", err)
	}
	return result, nil
}

// SaveSchedule uses Schedule.Version as an optimistic concurrency token. The
// caller supplies the next version, just like the other versioned repositories
// in this package.
func (r *SchedulerRepository) SaveSchedule(ctx context.Context, schedule domain.Schedule) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	schedule = normalizeSchedule(schedule)
	if schedule.Version < 2 {
		return fmt.Errorf("%w: schedule version must be greater than one when saving", domain.ErrInvalidArgument)
	}
	if err := validateScheduleForStorage(schedule); err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE schedules SET
			name = ?, kind = ?, expression = ?, timezone = ?, start_at = ?,
			interval_seconds = ?, payload_json = ?, status = ?, enabled = ?,
			misfire_policy = ?, next_run_at = ?, last_run_at = ?,
			retry_max_attempts = ?, retry_initial_backoff_seconds = ?,
			retry_max_backoff_seconds = ?, max_duration_seconds = ?,
			max_tokens = ?, max_tool_calls = ?, allow_overlap = ?,
			history_limit = ?, version = ?, updated_at = ?
		WHERE id = ? AND version = ?`,
		schedule.Name, string(schedule.Kind), schedule.Expression, schedule.Timezone,
		formatTime(schedule.StartAt), schedule.IntervalSeconds, schedule.PayloadJSON,
		string(schedule.Status), boolInt(schedule.Enabled), string(schedule.MisfirePolicy),
		nullableTimeValue(schedule.NextRunAt), nullableTimeValue(schedule.LastRunAt),
		schedule.Retry.MaxAttempts, schedule.Retry.InitialBackoffSecond,
		schedule.Retry.MaxBackoffSecond, schedule.Budget.MaxDurationSeconds,
		schedule.Budget.MaxTokens, schedule.Budget.MaxToolCalls, boolInt(schedule.AllowOverlap),
		schedule.HistoryLimit, schedule.Version, formatTime(schedule.UpdatedAt),
		string(schedule.ID), schedule.Version-1)
	if err != nil {
		return wrappedSQLError("save schedule", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrappedSQLError("count saved schedule", err)
	}
	if count == 1 {
		return nil
	}
	if _, err := r.GetSchedule(ctx, schedule.ID); err != nil {
		return err
	}
	return domain.ErrConflict
}

func (r *SchedulerRepository) DeleteSchedule(ctx context.Context, id domain.ID, now time.Time) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if id.Empty() || now.IsZero() {
		return fmt.Errorf("%w: schedule id and timestamp are required", domain.ErrInvalidArgument)
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE schedules SET status = ?, enabled = 0, next_run_at = NULL,
			version = version + 1, updated_at = ?
		WHERE id = ? AND status <> ?`, string(domain.ScheduleStatusDeleted),
		formatTime(now), string(id), string(domain.ScheduleStatusDeleted))
	if err != nil {
		return wrappedSQLError("delete schedule", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrappedSQLError("count deleted schedule", err)
	}
	if count == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *SchedulerRepository) ListDueSchedules(ctx context.Context, now time.Time, limit int) ([]domain.Schedule, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if now.IsZero() || limit < 0 {
		return nil, fmt.Errorf("%w: due schedule timestamp or limit is invalid", domain.ErrInvalidArgument)
	}
	query := scheduleSelect + ` WHERE enabled = 1 AND status = ? AND next_run_at IS NOT NULL AND next_run_at <= ?
		ORDER BY next_run_at ASC, id ASC`
	args := []any{string(domain.ScheduleStatusActive), formatTime(now)}
	query, args = appendWindow(query, args, limit, 0)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list due schedules", err)
	}
	defer rows.Close()
	result := make([]domain.Schedule, 0)
	for rows.Next() {
		item, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate due schedules", err)
	}
	return result, nil
}
