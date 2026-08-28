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
		schedule.Timezone, schedule.StartAt.Format(time.RFC3339Nano), schedule.IntervalSeconds,
		schedule.PayloadJSON, string(schedule.Status), boolInt(schedule.Enabled),
		string(schedule.MisfirePolicy), nullableTimeValue(schedule.NextRunAt),
		nullableTimeValue(schedule.LastRunAt), schedule.Retry.MaxAttempts,
		schedule.Retry.InitialBackoffSecond, schedule.Retry.MaxBackoffSecond,
		schedule.Budget.MaxDurationSeconds, schedule.Budget.MaxTokens,
		schedule.Budget.MaxToolCalls, boolInt(schedule.AllowOverlap), schedule.HistoryLimit,
		schedule.Version, schedule.CreatedAt.Format(time.RFC3339Nano), schedule.UpdatedAt.Format(time.RFC3339Nano))
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
	if options.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, options.Limit)
	}
	if options.Offset > 0 {
		if options.Limit == 0 {
			query += " LIMIT -1"
		}
		query += " OFFSET ?"
		args = append(args, options.Offset)
	}
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
		schedule.StartAt.Format(time.RFC3339Nano), schedule.IntervalSeconds, schedule.PayloadJSON,
		string(schedule.Status), boolInt(schedule.Enabled), string(schedule.MisfirePolicy),
		nullableTimeValue(schedule.NextRunAt), nullableTimeValue(schedule.LastRunAt),
		schedule.Retry.MaxAttempts, schedule.Retry.InitialBackoffSecond,
		schedule.Retry.MaxBackoffSecond, schedule.Budget.MaxDurationSeconds,
		schedule.Budget.MaxTokens, schedule.Budget.MaxToolCalls, boolInt(schedule.AllowOverlap),
		schedule.HistoryLimit, schedule.Version, schedule.UpdatedAt.Format(time.RFC3339Nano),
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
		now.UTC().Format(time.RFC3339Nano), string(id), string(domain.ScheduleStatusDeleted))
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
	args := []any{string(domain.ScheduleStatusActive), now.UTC().Format(time.RFC3339Nano)}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
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

func (r *SchedulerRepository) ListRetryableRuns(ctx context.Context, now time.Time, limit int) ([]domain.JobRun, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if now.IsZero() || limit < 0 {
		return nil, fmt.Errorf("%w: retry timestamp or limit is invalid", domain.ErrInvalidArgument)
	}
	query := jobRunSelect + ` AS j WHERE j.state = ? AND (
		(j.trigger = ? AND (j.retry_at IS NULL OR j.retry_at <= ?)) OR
		(j.trigger = ? AND j.retry_at IS NOT NULL AND j.retry_at <= ?)
	) AND EXISTS (
		SELECT 1 FROM schedules AS s WHERE s.id = j.schedule_id AND s.status <> ? AND
			(s.status = ? OR j.trigger = ? OR
			 (j.trigger = ? AND s.kind = ? AND s.status = ?))
	) ORDER BY COALESCE(j.retry_at, j.created_at) ASC, j.created_at ASC, j.id ASC`
	args := []any{string(domain.JobRunQueued), string(domain.JobTriggerManual), now.UTC().Format(time.RFC3339Nano),
		string(domain.JobTriggerScheduled), now.UTC().Format(time.RFC3339Nano), string(domain.ScheduleStatusDeleted),
		string(domain.ScheduleStatusActive), string(domain.JobTriggerManual), string(domain.JobTriggerScheduled),
		string(domain.ScheduleKindOnce), string(domain.ScheduleStatusCompleted)}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list retryable runs", err)
	}
	defer rows.Close()
	result := make([]domain.JobRun, 0)
	for rows.Next() {
		item, err := scanJobRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate retryable runs", err)
	}
	return result, nil
}

func (r *SchedulerRepository) ClaimScheduled(ctx context.Context, claim domain.ScheduledClaim) (domain.ScheduledJob, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.ScheduledJob{}, err
	}
	if err := validateScheduledClaim(claim); err != nil {
		return domain.ScheduledJob{}, err
	}
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ScheduledJob{}, wrappedSQLError("begin scheduled claim", err)
	}
	defer transaction.Rollback()
	if _, err := recoverExpiredLeasesTx(ctx, transaction, claim.Now); err != nil {
		return domain.ScheduledJob{}, err
	}
	schedule, err := scanSchedule(transaction.QueryRowContext(ctx, scheduleSelect+" WHERE id = ?", string(claim.ScheduleID)))
	if err != nil {
		return domain.ScheduledJob{}, err
	}
	if schedule.Version != claim.ExpectedVersion || schedule.Status != domain.ScheduleStatusActive || !schedule.Enabled ||
		schedule.NextRunAt.IsZero() || !schedule.NextRunAt.Equal(claim.ScheduledFor) || claim.ScheduledFor.After(claim.Now) {
		return domain.ScheduledJob{}, domain.ErrConflict
	}
	if !schedule.AllowOverlap {
		busy, err := scheduleHasOpenRun(ctx, transaction, schedule.ID, "")
		if err != nil {
			return domain.ScheduledJob{}, err
		}
		if busy {
			return domain.ScheduledJob{}, domain.ErrConflict
		}
	}
	job, err := newClaimedJob(schedule, domain.JobTriggerScheduled, claim.ScheduledFor, claim.Now, claim.WorkerID, claim.LeaseDuration)
	if err != nil {
		return domain.ScheduledJob{}, err
	}
	if err := insertJobRunTx(ctx, transaction, job.Run); err != nil {
		return domain.ScheduledJob{}, err
	}
	if err := advanceScheduleTx(ctx, transaction, schedule, claim.NextRunAt, claim.ScheduledFor, claim.Now); err != nil {
		return domain.ScheduledJob{}, err
	}
	if err := pruneJobHistoryTx(ctx, transaction, schedule.ID, schedule.HistoryLimit); err != nil {
		return domain.ScheduledJob{}, err
	}
	if err := transaction.Commit(); err != nil {
		return domain.ScheduledJob{}, wrappedSQLError("commit scheduled claim", err)
	}
	return job, nil
}

func (r *SchedulerRepository) RecordMisfire(ctx context.Context, record domain.MisfireRecord) (domain.JobRun, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.JobRun{}, err
	}
	if err := validateMisfireRecord(record); err != nil {
		return domain.JobRun{}, err
	}
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.JobRun{}, wrappedSQLError("begin misfire record", err)
	}
	defer transaction.Rollback()
	schedule, err := scanSchedule(transaction.QueryRowContext(ctx, scheduleSelect+" WHERE id = ?", string(record.ScheduleID)))
	if err != nil {
		return domain.JobRun{}, err
	}
	if schedule.Version != record.ExpectedVersion || schedule.Status != domain.ScheduleStatusActive ||
		schedule.NextRunAt.IsZero() || !schedule.NextRunAt.Equal(record.ScheduledFor) || record.ScheduledFor.After(record.Now) {
		return domain.JobRun{}, domain.ErrConflict
	}
	if !schedule.AllowOverlap {
		busy, err := scheduleHasOpenRun(ctx, transaction, schedule.ID, "")
		if err != nil {
			return domain.JobRun{}, err
		}
		if busy {
			return domain.JobRun{}, domain.ErrConflict
		}
	}
	jobID, err := domain.NewID("job")
	if err != nil {
		return domain.JobRun{}, err
	}
	run := domain.JobRun{
		ID: jobID, ScheduleID: schedule.ID, State: domain.JobRunSkipped,
		Trigger: domain.JobTriggerScheduled, Attempt: 0,
		ExecutionKey: executionKey(schedule.ID, record.ScheduledFor), ScheduledFor: record.ScheduledFor.UTC(),
		Error: strings.TrimSpace(record.Reason), CreatedAt: record.Now.UTC(), UpdatedAt: record.Now.UTC(),
		FinishedAt: record.Now.UTC(), Version: 1,
	}
	if run.Error == "" {
		run.Error = "misfire skipped"
	}
	if err := insertJobRunTx(ctx, transaction, run); err != nil {
		return domain.JobRun{}, err
	}
	if err := advanceScheduleTx(ctx, transaction, schedule, record.NextRunAt, record.ScheduledFor, record.Now); err != nil {
		return domain.JobRun{}, err
	}
	if err := pruneJobHistoryTx(ctx, transaction, schedule.ID, schedule.HistoryLimit); err != nil {
		return domain.JobRun{}, err
	}
	if err := transaction.Commit(); err != nil {
		return domain.JobRun{}, wrappedSQLError("commit misfire record", err)
	}
	return run, nil
}

func (r *SchedulerRepository) EnqueueManualRun(ctx context.Context, request domain.ManualRunRequest) (domain.JobRun, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.JobRun{}, err
	}
	if err := validateManualRunRequest(request); err != nil {
		return domain.JobRun{}, err
	}
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.JobRun{}, wrappedSQLError("begin manual run", err)
	}
	defer transaction.Rollback()
	if _, err := recoverExpiredLeasesTx(ctx, transaction, request.Now); err != nil {
		return domain.JobRun{}, err
	}
	schedule, err := scanSchedule(transaction.QueryRowContext(ctx, scheduleSelect+" WHERE id = ?", string(request.ScheduleID)))
	if err != nil {
		return domain.JobRun{}, err
	}
	if schedule.Status == domain.ScheduleStatusDeleted {
		return domain.JobRun{}, domain.ErrNotFound
	}
	if !schedule.AllowOverlap {
		busy, err := scheduleHasOpenRun(ctx, transaction, schedule.ID, "")
		if err != nil {
			return domain.JobRun{}, err
		}
		if busy {
			return domain.JobRun{}, domain.ErrConflict
		}
	}
	jobID, err := domain.NewID("job")
	if err != nil {
		return domain.JobRun{}, err
	}
	executionID, err := domain.NewID("execution")
	if err != nil {
		return domain.JobRun{}, err
	}
	run := domain.JobRun{
		ID: jobID, ScheduleID: schedule.ID, State: domain.JobRunQueued,
		Trigger: domain.JobTriggerManual, Attempt: 0, ExecutionKey: string(executionID),
		ScheduledFor: request.Now.UTC(), CreatedAt: request.Now.UTC(), UpdatedAt: request.Now.UTC(), Version: 1,
	}
	if err := insertJobRunTx(ctx, transaction, run); err != nil {
		return domain.JobRun{}, err
	}
	if err := pruneJobHistoryTx(ctx, transaction, schedule.ID, schedule.HistoryLimit); err != nil {
		return domain.JobRun{}, err
	}
	if err := transaction.Commit(); err != nil {
		return domain.JobRun{}, wrappedSQLError("commit manual run", err)
	}
	return run, nil
}

func (r *SchedulerRepository) ClaimRetry(ctx context.Context, request domain.RetryClaim) (domain.ScheduledJob, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.ScheduledJob{}, err
	}
	if err := validateRetryClaim(request); err != nil {
		return domain.ScheduledJob{}, err
	}
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ScheduledJob{}, wrappedSQLError("begin retry claim", err)
	}
	defer transaction.Rollback()
	if _, err := recoverExpiredLeasesTx(ctx, transaction, request.Now); err != nil {
		return domain.ScheduledJob{}, err
	}
	run, err := scanJobRun(transaction.QueryRowContext(ctx, jobRunSelect+" WHERE id = ?", string(request.RunID)))
	if err != nil {
		return domain.ScheduledJob{}, err
	}
	schedule, err := scanSchedule(transaction.QueryRowContext(ctx, scheduleSelect+" WHERE id = ?", string(run.ScheduleID)))
	if err != nil {
		return domain.ScheduledJob{}, err
	}
	ready := run.State == domain.JobRunQueued && run.Attempt < schedule.Retry.MaxAttempts &&
		(run.Trigger == domain.JobTriggerManual || (!run.RetryAt.IsZero() && !run.RetryAt.After(request.Now)))
	scheduledRetryAllowed := schedule.Status == domain.ScheduleStatusActive ||
		(schedule.Status == domain.ScheduleStatusCompleted && schedule.Kind == domain.ScheduleKindOnce && run.Trigger == domain.JobTriggerScheduled)
	if !ready || schedule.Status == domain.ScheduleStatusDeleted ||
		(!scheduledRetryAllowed && run.Trigger != domain.JobTriggerManual) {
		return domain.ScheduledJob{}, domain.ErrConflict
	}
	if !schedule.AllowOverlap {
		busy, err := scheduleHasOpenRun(ctx, transaction, schedule.ID, string(run.ID))
		if err != nil {
			return domain.ScheduledJob{}, err
		}
		if busy {
			return domain.ScheduledJob{}, domain.ErrConflict
		}
	}
	leaseToken, err := domain.NewID("lease")
	if err != nil {
		return domain.ScheduledJob{}, err
	}
	run.State = domain.JobRunRunning
	run.Attempt++
	run.LeaseOwner = request.WorkerID
	run.LeaseToken = string(leaseToken)
	run.LeaseUntil = request.Now.UTC().Add(request.LeaseDuration)
	run.RetryAt = time.Time{}
	run.StartedAt = request.Now.UTC()
	run.FinishedAt = time.Time{}
	run.Error = ""
	run.UpdatedAt = request.Now.UTC()
	run.Version++
	result, err := transaction.ExecContext(ctx, `
		UPDATE job_runs SET state = ?, attempt = ?, lease_owner = ?, lease_token = ?,
			lease_until = ?, retry_at = NULL, started_at = ?, finished_at = NULL,
			error = '', version = ?, updated_at = ?
		WHERE id = ? AND state = ? AND version = ?`,
		string(run.State), run.Attempt, run.LeaseOwner, run.LeaseToken,
		run.LeaseUntil.Format(time.RFC3339Nano), run.StartedAt.Format(time.RFC3339Nano),
		run.Version, run.UpdatedAt.Format(time.RFC3339Nano), string(run.ID),
		string(domain.JobRunQueued), run.Version-1)
	if err != nil {
		return domain.ScheduledJob{}, wrappedSQLError("claim retry run", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return domain.ScheduledJob{}, wrappedSQLError("count claimed retry run", err)
	} else if count != 1 {
		return domain.ScheduledJob{}, domain.ErrConflict
	}
	if err := transaction.Commit(); err != nil {
		return domain.ScheduledJob{}, wrappedSQLError("commit retry claim", err)
	}
	return domain.ScheduledJob{Schedule: schedule, Run: run}, nil
}

func (r *SchedulerRepository) RenewLease(ctx context.Context, request domain.RenewLeaseRequest) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := validateRenewLeaseRequest(request); err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE job_runs SET lease_until = ?, updated_at = ?, version = version + 1
		WHERE id = ? AND state = ? AND lease_owner = ? AND lease_token = ?
			AND lease_until IS NOT NULL AND lease_until > ?`,
		request.LeaseUntil.UTC().Format(time.RFC3339Nano), request.Now.UTC().Format(time.RFC3339Nano),
		string(request.RunID), string(domain.JobRunRunning), request.WorkerID, request.LeaseToken,
		request.Now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return wrappedSQLError("renew job lease", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrappedSQLError("count renewed job lease", err)
	}
	if count != 1 {
		return domain.ErrConflict
	}
	return nil
}

func (r *SchedulerRepository) CompleteRun(ctx context.Context, request domain.CompleteRunRequest) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := validateCompletionRequest(request); err != nil {
		return err
	}
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin complete job run", err)
	}
	defer transaction.Rollback()
	run, err := scanJobRun(transaction.QueryRowContext(ctx, jobRunSelect+" WHERE id = ?", string(request.RunID)))
	if err != nil {
		return err
	}
	if run.State != domain.JobRunRunning || run.LeaseOwner != request.WorkerID || run.LeaseToken != request.LeaseToken {
		return domain.ErrConflict
	}
	result, err := transaction.ExecContext(ctx, `
		UPDATE job_runs SET state = ?, result_ref = ?, lease_owner = '', lease_token = '',
			lease_until = NULL, finished_at = ?, updated_at = ?, version = version + 1
		WHERE id = ? AND state = ? AND lease_owner = ? AND lease_token = ?`,
		string(domain.JobRunSucceeded), request.ResultRef, request.Now.UTC().Format(time.RFC3339Nano),
		request.Now.UTC().Format(time.RFC3339Nano), string(request.RunID), string(domain.JobRunRunning),
		request.WorkerID, request.LeaseToken)
	if err != nil {
		return wrappedSQLError("complete job run", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrappedSQLError("count completed job run", err)
	}
	if count != 1 {
		return domain.ErrConflict
	}
	var historyLimit int
	if err := transaction.QueryRowContext(ctx, "SELECT history_limit FROM schedules WHERE id = ?", string(run.ScheduleID)).Scan(&historyLimit); err != nil {
		return wrappedSQLError("read schedule history limit", err)
	}
	if err := pruneJobHistoryTx(ctx, transaction, run.ScheduleID, historyLimit); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return wrappedSQLError("commit completed job run", err)
	}
	return nil
}

func (r *SchedulerRepository) FailRun(ctx context.Context, request domain.FailRunRequest) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := validateFailureRequest(request); err != nil {
		return err
	}
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin fail job run", err)
	}
	defer transaction.Rollback()
	run, err := scanJobRun(transaction.QueryRowContext(ctx, jobRunSelect+" WHERE id = ?", string(request.RunID)))
	if err != nil {
		return err
	}
	schedule, err := scanSchedule(transaction.QueryRowContext(ctx, scheduleSelect+" WHERE id = ?", string(run.ScheduleID)))
	if err != nil {
		return err
	}
	if run.State != domain.JobRunRunning || run.LeaseOwner != request.WorkerID || run.LeaseToken != request.LeaseToken {
		return domain.ErrConflict
	}
	reason := strings.TrimSpace(request.Reason)
	if run.Attempt < schedule.Retry.MaxAttempts && schedule.Status != domain.ScheduleStatusDeleted {
		run.State = domain.JobRunQueued
		run.RetryAt = request.Now.UTC().Add(retryBackoff(schedule.Retry, run.Attempt))
		run.FinishedAt = time.Time{}
	} else {
		run.State = domain.JobRunFailed
		run.RetryAt = time.Time{}
		run.FinishedAt = request.Now.UTC()
	}
	run.Error = reason
	run.LeaseOwner = ""
	run.LeaseToken = ""
	run.LeaseUntil = time.Time{}
	run.UpdatedAt = request.Now.UTC()
	run.Version++
	result, err := transaction.ExecContext(ctx, `
		UPDATE job_runs SET state = ?, lease_owner = '', lease_token = '', lease_until = NULL,
			retry_at = ?, finished_at = ?, error = ?, updated_at = ?, version = ?
		WHERE id = ? AND state = ? AND lease_owner = ? AND lease_token = ? AND version = ?`,
		string(run.State), nullableTimeValue(run.RetryAt), nullableTimeValue(run.FinishedAt), run.Error,
		run.UpdatedAt.Format(time.RFC3339Nano), run.Version, string(run.ID), string(domain.JobRunRunning),
		request.WorkerID, request.LeaseToken, run.Version-1)
	if err != nil {
		return wrappedSQLError("fail job run", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return wrappedSQLError("count failed job run", err)
	} else if count != 1 {
		return domain.ErrConflict
	}
	if err := pruneJobHistoryTx(ctx, transaction, schedule.ID, schedule.HistoryLimit); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return wrappedSQLError("commit failed job run", err)
	}
	return nil
}

func (r *SchedulerRepository) CancelRun(ctx context.Context, request domain.CancelRunRequest) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := validateCancelRequest(request); err != nil {
		return err
	}
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin cancel job run", err)
	}
	defer transaction.Rollback()
	run, err := scanJobRun(transaction.QueryRowContext(ctx, jobRunSelect+" WHERE id = ?", string(request.RunID)))
	if err != nil {
		return err
	}
	if run.State != domain.JobRunQueued && run.State != domain.JobRunRunning {
		return domain.ErrConflict
	}
	if run.State == domain.JobRunRunning && (run.LeaseOwner != request.WorkerID || run.LeaseToken != request.LeaseToken) {
		return domain.ErrConflict
	}
	result, err := transaction.ExecContext(ctx, `
		UPDATE job_runs SET state = ?, lease_owner = '', lease_token = '', lease_until = NULL,
			retry_at = NULL, finished_at = ?, updated_at = ?, version = version + 1
		WHERE id = ? AND state IN (?, ?) AND version = ?`,
		string(domain.JobRunCancelled), request.Now.UTC().Format(time.RFC3339Nano),
		request.Now.UTC().Format(time.RFC3339Nano), string(request.RunID), string(domain.JobRunQueued),
		string(domain.JobRunRunning), run.Version)
	if err != nil {
		return wrappedSQLError("cancel job run", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrappedSQLError("count cancelled job run", err)
	}
	if count != 1 {
		return domain.ErrConflict
	}
	var historyLimit int
	if err := transaction.QueryRowContext(ctx, "SELECT history_limit FROM schedules WHERE id = ?", string(run.ScheduleID)).Scan(&historyLimit); err != nil {
		return wrappedSQLError("read schedule history limit", err)
	}
	if err := pruneJobHistoryTx(ctx, transaction, run.ScheduleID, historyLimit); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return wrappedSQLError("commit cancelled job run", err)
	}
	return nil
}

func (r *SchedulerRepository) RecoverExpiredLeases(ctx context.Context, now time.Time) (int, error) {
	if err := requireDatabase(r.db); err != nil {
		return 0, err
	}
	if err := contextErr(ctx); err != nil {
		return 0, err
	}
	if now.IsZero() {
		return 0, fmt.Errorf("%w: recovery timestamp is required", domain.ErrInvalidArgument)
	}
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, wrappedSQLError("begin lease recovery", err)
	}
	defer transaction.Rollback()
	count, err := recoverExpiredLeasesTx(ctx, transaction, now.UTC())
	if err != nil {
		return 0, err
	}
	if err := transaction.Commit(); err != nil {
		return 0, wrappedSQLError("commit lease recovery", err)
	}
	return count, nil
}

func (r *SchedulerRepository) GetJobRun(ctx context.Context, id domain.ID) (domain.JobRun, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.JobRun{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.JobRun{}, err
	}
	if id.Empty() {
		return domain.JobRun{}, fmt.Errorf("%w: job run id is required", domain.ErrInvalidArgument)
	}
	return scanJobRun(r.db.QueryRowContext(ctx, jobRunSelect+" WHERE id = ?", string(id)))
}

func (r *SchedulerRepository) ListJobRuns(ctx context.Context, scheduleID domain.ID, options domain.JobRunListOptions) ([]domain.JobRun, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if options.Limit < 0 || options.Offset < 0 {
		return nil, fmt.Errorf("%w: job run pagination cannot be negative", domain.ErrInvalidArgument)
	}
	query := jobRunSelect
	args := make([]any, 0, 3)
	if !scheduleID.Empty() {
		query += " WHERE schedule_id = ?"
		args = append(args, string(scheduleID))
	}
	query += " ORDER BY created_at DESC, id DESC"
	if options.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, options.Limit)
	}
	if options.Offset > 0 {
		if options.Limit == 0 {
			query += " LIMIT -1"
		}
		query += " OFFSET ?"
		args = append(args, options.Offset)
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list job runs", err)
	}
	defer rows.Close()
	result := make([]domain.JobRun, 0)
	for rows.Next() {
		item, err := scanJobRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate job runs", err)
	}
	return result, nil
}

func validateScheduleForStorage(schedule domain.Schedule) error {
	if err := schedule.ValidateBasic(); err != nil {
		return err
	}
	if _, err := time.LoadLocation(schedule.Timezone); err != nil {
		return fmt.Errorf("%w: invalid schedule timezone: %v", domain.ErrInvalidArgument, err)
	}
	if err := validJSON(schedule.PayloadJSON, "payload_json"); err != nil {
		return err
	}
	switch schedule.Kind {
	case domain.ScheduleKindOnce:
		if schedule.Expression != "" || schedule.IntervalSeconds != 0 {
			return fmt.Errorf("%w: one-shot schedule has interval or expression", domain.ErrInvalidArgument)
		}
	case domain.ScheduleKindInterval:
		if schedule.Expression != "" || schedule.IntervalSeconds <= 0 {
			return fmt.Errorf("%w: interval schedule requires interval_seconds only", domain.ErrInvalidArgument)
		}
	case domain.ScheduleKindCron:
		if strings.TrimSpace(schedule.Expression) == "" || schedule.IntervalSeconds != 0 {
			return fmt.Errorf("%w: cron schedule requires expression only", domain.ErrInvalidArgument)
		}
	}
	return nil
}

func normalizeSchedule(schedule domain.Schedule) domain.Schedule {
	schedule.Name = strings.TrimSpace(schedule.Name)
	schedule.Timezone = strings.TrimSpace(schedule.Timezone)
	schedule.Expression = strings.TrimSpace(schedule.Expression)
	schedule.PayloadJSON = strings.TrimSpace(schedule.PayloadJSON)
	if schedule.PayloadJSON == "" {
		schedule.PayloadJSON = "{}"
	}
	schedule.StartAt = schedule.StartAt.UTC()
	schedule.NextRunAt = schedule.NextRunAt.UTC()
	schedule.LastRunAt = schedule.LastRunAt.UTC()
	schedule.CreatedAt = schedule.CreatedAt.UTC()
	schedule.UpdatedAt = schedule.UpdatedAt.UTC()
	return schedule
}

func validateScheduledClaim(claim domain.ScheduledClaim) error {
	if claim.ScheduleID.Empty() || claim.ExpectedVersion == 0 || claim.ScheduledFor.IsZero() || claim.Now.IsZero() ||
		strings.TrimSpace(claim.WorkerID) == "" || claim.LeaseDuration <= 0 || claim.LeaseDuration > 30*24*time.Hour {
		return fmt.Errorf("%w: invalid scheduled claim", domain.ErrInvalidArgument)
	}
	if claim.ScheduledFor.After(claim.Now) {
		return fmt.Errorf("%w: scheduled occurrence is in the future", domain.ErrInvalidArgument)
	}
	return nil
}

func validateMisfireRecord(record domain.MisfireRecord) error {
	if record.ScheduleID.Empty() || record.ExpectedVersion == 0 || record.ScheduledFor.IsZero() || record.Now.IsZero() ||
		record.ScheduledFor.After(record.Now) {
		return fmt.Errorf("%w: invalid misfire record", domain.ErrInvalidArgument)
	}
	return nil
}

func validateManualRunRequest(request domain.ManualRunRequest) error {
	if request.ScheduleID.Empty() || request.Now.IsZero() {
		return fmt.Errorf("%w: invalid manual run request", domain.ErrInvalidArgument)
	}
	return nil
}

func validateRetryClaim(request domain.RetryClaim) error {
	if request.RunID.Empty() || request.Now.IsZero() || strings.TrimSpace(request.WorkerID) == "" ||
		request.LeaseDuration <= 0 || request.LeaseDuration > 30*24*time.Hour {
		return fmt.Errorf("%w: invalid retry claim", domain.ErrInvalidArgument)
	}
	return nil
}

func validateRenewLeaseRequest(request domain.RenewLeaseRequest) error {
	if request.RunID.Empty() || strings.TrimSpace(request.WorkerID) == "" || strings.TrimSpace(request.LeaseToken) == "" ||
		request.Now.IsZero() || request.LeaseUntil.IsZero() || !request.LeaseUntil.After(request.Now) {
		return fmt.Errorf("%w: invalid lease renewal", domain.ErrInvalidArgument)
	}
	return nil
}

func validateCompletionRequest(request domain.CompleteRunRequest) error {
	if request.RunID.Empty() || strings.TrimSpace(request.WorkerID) == "" || strings.TrimSpace(request.LeaseToken) == "" || request.Now.IsZero() {
		return fmt.Errorf("%w: invalid completion request", domain.ErrInvalidArgument)
	}
	return nil
}

func validateFailureRequest(request domain.FailRunRequest) error {
	if request.RunID.Empty() || strings.TrimSpace(request.WorkerID) == "" || strings.TrimSpace(request.LeaseToken) == "" ||
		request.Now.IsZero() || strings.TrimSpace(request.Reason) == "" {
		return fmt.Errorf("%w: invalid failure request", domain.ErrInvalidArgument)
	}
	return nil
}

func validateCancelRequest(request domain.CancelRunRequest) error {
	if request.RunID.Empty() || request.Now.IsZero() {
		return fmt.Errorf("%w: invalid cancellation request", domain.ErrInvalidArgument)
	}
	return nil
}

func newClaimedJob(schedule domain.Schedule, trigger domain.JobTrigger, scheduledFor, now time.Time, workerID string, leaseDuration time.Duration) (domain.ScheduledJob, error) {
	jobID, err := domain.NewID("job")
	if err != nil {
		return domain.ScheduledJob{}, err
	}
	leaseToken, err := domain.NewID("lease")
	if err != nil {
		return domain.ScheduledJob{}, err
	}
	return domain.ScheduledJob{
		Schedule: schedule,
		Run: domain.JobRun{
			ID: jobID, ScheduleID: schedule.ID, State: domain.JobRunRunning, Trigger: trigger, Attempt: 1,
			ExecutionKey: executionKey(schedule.ID, scheduledFor), LeaseOwner: workerID,
			LeaseToken: string(leaseToken), LeaseUntil: now.UTC().Add(leaseDuration), ScheduledFor: scheduledFor.UTC(),
			StartedAt: now.UTC(), CreatedAt: now.UTC(), UpdatedAt: now.UTC(), Version: 1,
		},
	}, nil
}

func executionKey(scheduleID domain.ID, scheduledFor time.Time) string {
	return string(scheduleID) + ":" + scheduledFor.UTC().Format(time.RFC3339Nano)
}

func insertJobRunTx(ctx context.Context, transaction *sql.Tx, run domain.JobRun) error {
	if !run.Valid() {
		return fmt.Errorf("%w: invalid job run", domain.ErrInvalidArgument)
	}
	_, err := transaction.ExecContext(ctx, `
		INSERT INTO job_runs(
			id, schedule_id, state, trigger, attempt, execution_key, lease_owner,
			lease_token, lease_until, scheduled_for, retry_at, started_at,
			finished_at, error, result_ref, version, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(run.ID), string(run.ScheduleID), string(run.State), string(run.Trigger), run.Attempt,
		run.ExecutionKey, run.LeaseOwner, run.LeaseToken, nullableTimeValue(run.LeaseUntil),
		run.ScheduledFor.UTC().Format(time.RFC3339Nano), nullableTimeValue(run.RetryAt),
		nullableTimeValue(run.StartedAt), nullableTimeValue(run.FinishedAt), run.Error, run.ResultRef,
		run.Version, run.CreatedAt.UTC().Format(time.RFC3339Nano), run.UpdatedAt.UTC().Format(time.RFC3339Nano))
	return wrappedSQLError("insert job run", err)
}

func advanceScheduleTx(ctx context.Context, transaction *sql.Tx, schedule domain.Schedule, nextRunAt, lastRunAt, now time.Time) error {
	status := schedule.Status
	enabled := schedule.Enabled
	var next any
	if nextRunAt.IsZero() {
		status = domain.ScheduleStatusCompleted
		enabled = false
		next = nil
	} else {
		next = nextRunAt.UTC().Format(time.RFC3339Nano)
	}
	result, err := transaction.ExecContext(ctx, `
		UPDATE schedules SET status = ?, enabled = ?, next_run_at = ?, last_run_at = ?,
			version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND status = ? AND enabled = 1 AND next_run_at = ?`,
		string(status), boolInt(enabled), next, nullableTimeValue(lastRunAt), now.UTC().Format(time.RFC3339Nano),
		string(schedule.ID), schedule.Version, string(domain.ScheduleStatusActive), schedule.NextRunAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return wrappedSQLError("advance schedule", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrappedSQLError("count advanced schedule", err)
	}
	if count != 1 {
		return domain.ErrConflict
	}
	return nil
}

func scheduleHasOpenRun(ctx context.Context, transaction *sql.Tx, scheduleID domain.ID, excludeID string) (bool, error) {
	query := `SELECT 1 FROM job_runs WHERE schedule_id = ? AND state IN (?, ?)`
	args := []any{string(scheduleID), string(domain.JobRunQueued), string(domain.JobRunRunning)}
	if excludeID != "" {
		query += " AND id <> ?"
		args = append(args, excludeID)
	}
	query += " LIMIT 1"
	var value int
	err := transaction.QueryRowContext(ctx, query, args...).Scan(&value)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, wrappedSQLError("check open schedule run", err)
	}
	return true, nil
}

func recoverExpiredLeasesTx(ctx context.Context, transaction *sql.Tx, now time.Time) (int, error) {
	rows, err := transaction.QueryContext(ctx, `
		SELECT j.id, j.attempt, s.retry_max_attempts
		FROM job_runs AS j JOIN schedules AS s ON s.id = j.schedule_id
		WHERE j.state = ? AND j.lease_until IS NOT NULL AND j.lease_until <= ?
		ORDER BY j.lease_until ASC, j.id ASC`, string(domain.JobRunRunning), now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, wrappedSQLError("find expired leases", err)
	}
	defer rows.Close()
	type expired struct {
		id                   domain.ID
		attempt, maxAttempts int
	}
	expiredRuns := make([]expired, 0)
	for rows.Next() {
		var item expired
		if err := rows.Scan(&item.id, &item.attempt, &item.maxAttempts); err != nil {
			return 0, wrappedSQLError("scan expired lease", err)
		}
		expiredRuns = append(expiredRuns, item)
	}
	if err := rows.Err(); err != nil {
		return 0, wrappedSQLError("iterate expired leases", err)
	}
	for _, item := range expiredRuns {
		state := domain.JobRunQueued
		errorMessage := "lease expired; recovered for retry"
		var retry any = now.UTC().Format(time.RFC3339Nano)
		var finished any
		if item.attempt >= item.maxAttempts {
			state = domain.JobRunFailed
			errorMessage = "lease expired; retry limit exhausted"
			retry = nil
			finished = now.UTC().Format(time.RFC3339Nano)
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE job_runs SET state = ?, lease_owner = '', lease_token = '', lease_until = NULL,
				retry_at = ?, finished_at = ?, error = ?, updated_at = ?, version = version + 1
			WHERE id = ? AND state = ?`, string(state), retry, finished, errorMessage,
			now.UTC().Format(time.RFC3339Nano), string(item.id), string(domain.JobRunRunning)); err != nil {
			return 0, wrappedSQLError("recover expired lease", err)
		}
	}
	return len(expiredRuns), nil
}

func pruneJobHistoryTx(ctx context.Context, transaction *sql.Tx, scheduleID domain.ID, limit int) error {
	if limit < 1 {
		return fmt.Errorf("%w: history limit must be positive", domain.ErrInvalidArgument)
	}
	rows, err := transaction.QueryContext(ctx, `
		SELECT id FROM job_runs WHERE schedule_id = ? AND state IN (?, ?, ?, ?)
		ORDER BY created_at DESC, id DESC`, string(scheduleID), string(domain.JobRunSucceeded),
		string(domain.JobRunFailed), string(domain.JobRunCancelled), string(domain.JobRunSkipped))
	if err != nil {
		return wrappedSQLError("find old job history", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return wrappedSQLError("scan old job history", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return wrappedSQLError("close old job history", err)
	}
	if len(ids) <= limit {
		return nil
	}
	for _, id := range ids[limit:] {
		if _, err := transaction.ExecContext(ctx, "DELETE FROM job_runs WHERE id = ?", id); err != nil {
			return wrappedSQLError("prune old job history", err)
		}
	}
	return nil
}

func retryBackoff(policy domain.RetryPolicy, attempt int) time.Duration {
	if attempt <= 0 || policy.InitialBackoffSecond <= 0 {
		return 0
	}
	seconds := int64(policy.InitialBackoffSecond)
	for i := 1; i < attempt && seconds < int64(policy.MaxBackoffSecond); i++ {
		if seconds > int64(policy.MaxBackoffSecond)/2 {
			seconds = int64(policy.MaxBackoffSecond)
			break
		}
		seconds *= 2
	}
	if seconds > int64(policy.MaxBackoffSecond) {
		seconds = int64(policy.MaxBackoffSecond)
	}
	return time.Duration(seconds) * time.Second
}

func scanSchedule(row rowScanner) (domain.Schedule, error) {
	var (
		schedule                                domain.Schedule
		id, kind, expression, status, misfire   string
		timezone, startAt, payload, createdAt   string
		updatedAt, nextRunAt, lastRunAt         string
		enabled, allowOverlap                   int
		intervalSeconds                         int64
		retryMax, retryInitial, retryMaxBackoff int
		maxDuration, maxToolCalls, historyLimit int
		maxTokens                               int64
		version                                 uint64
	)
	if err := row.Scan(&id, &schedule.Name, &kind, &expression, &timezone, &startAt,
		&intervalSeconds, &payload, &status, &enabled, &misfire, &nullableString{Value: &nextRunAt},
		&nullableString{Value: &lastRunAt}, &retryMax, &retryInitial, &retryMaxBackoff,
		&maxDuration, &maxTokens, &maxToolCalls, &allowOverlap, &historyLimit, &version,
		&createdAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return domain.Schedule{}, domain.ErrNotFound
		}
		return domain.Schedule{}, wrappedSQLError("scan schedule", err)
	}
	schedule.ID = domain.ID(id)
	schedule.Kind = domain.ScheduleKind(kind)
	schedule.Expression = expression
	schedule.Timezone = timezone
	schedule.IntervalSeconds = intervalSeconds
	schedule.PayloadJSON = payload
	schedule.Status = domain.ScheduleStatus(status)
	schedule.Enabled = enabled != 0
	schedule.MisfirePolicy = domain.MisfirePolicy(misfire)
	schedule.AllowOverlap = allowOverlap != 0
	schedule.HistoryLimit = historyLimit
	schedule.Version = version
	schedule.Retry = domain.RetryPolicy{MaxAttempts: retryMax, InitialBackoffSecond: retryInitial, MaxBackoffSecond: retryMaxBackoff}
	schedule.Budget = domain.JobBudget{MaxDurationSeconds: maxDuration, MaxTokens: maxTokens, MaxToolCalls: maxToolCalls}
	var err error
	if schedule.StartAt, err = scanTime(startAt); err != nil {
		return domain.Schedule{}, err
	}
	if schedule.NextRunAt, err = scanTime(nextRunAt); err != nil {
		return domain.Schedule{}, err
	}
	if schedule.LastRunAt, err = scanTime(lastRunAt); err != nil {
		return domain.Schedule{}, err
	}
	if schedule.CreatedAt, err = scanTime(createdAt); err != nil {
		return domain.Schedule{}, err
	}
	if schedule.UpdatedAt, err = scanTime(updatedAt); err != nil {
		return domain.Schedule{}, err
	}
	return schedule, nil
}

func scanJobRun(row rowScanner) (domain.JobRun, error) {
	var (
		run                                        domain.JobRun
		id, scheduleID, state, trigger, execution  string
		leaseOwner, leaseToken, scheduledFor       string
		errorMessage, resultRef, createdAt         string
		updatedAt                                  string
		leaseUntil, retryAt, startedAt, finishedAt sql.NullString
		version                                    uint64
	)
	if err := row.Scan(&id, &scheduleID, &state, &trigger, &run.Attempt, &execution,
		&leaseOwner, &leaseToken, &leaseUntil, &scheduledFor, &retryAt, &startedAt,
		&finishedAt, &errorMessage, &resultRef, &version, &createdAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return domain.JobRun{}, domain.ErrNotFound
		}
		return domain.JobRun{}, wrappedSQLError("scan job run", err)
	}
	run.ID = domain.ID(id)
	run.ScheduleID = domain.ID(scheduleID)
	run.State = domain.JobRunState(state)
	run.Trigger = domain.JobTrigger(trigger)
	run.ExecutionKey = execution
	run.LeaseOwner = leaseOwner
	run.LeaseToken = leaseToken
	run.Error = errorMessage
	run.ResultRef = resultRef
	run.Version = version
	var err error
	if run.LeaseUntil, err = scanNullableTime(leaseUntil); err != nil {
		return domain.JobRun{}, err
	}
	if run.ScheduledFor, err = scanTime(scheduledFor); err != nil {
		return domain.JobRun{}, err
	}
	if run.RetryAt, err = scanNullableTime(retryAt); err != nil {
		return domain.JobRun{}, err
	}
	if run.StartedAt, err = scanNullableTime(startedAt); err != nil {
		return domain.JobRun{}, err
	}
	if run.FinishedAt, err = scanNullableTime(finishedAt); err != nil {
		return domain.JobRun{}, err
	}
	if run.CreatedAt, err = scanTime(createdAt); err != nil {
		return domain.JobRun{}, err
	}
	if run.UpdatedAt, err = scanTime(updatedAt); err != nil {
		return domain.JobRun{}, err
	}
	return run, nil
}
