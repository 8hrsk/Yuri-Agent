package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

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
	args := []any{string(domain.JobRunQueued), string(domain.JobTriggerManual), formatTime(now),
		string(domain.JobTriggerScheduled), formatTime(now), string(domain.ScheduleStatusDeleted),
		string(domain.ScheduleStatusActive), string(domain.JobTriggerManual), string(domain.JobTriggerScheduled),
		string(domain.ScheduleKindOnce), string(domain.ScheduleStatusCompleted)}
	query, args = appendWindow(query, args, limit, 0)
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
		string(domain.JobRunSucceeded), request.ResultRef, formatTime(request.Now),
		formatTime(request.Now), string(request.RunID), string(domain.JobRunRunning),
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
		formatTime(run.UpdatedAt), run.Version, string(run.ID), string(domain.JobRunRunning),
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
		string(domain.JobRunCancelled), formatTime(request.Now),
		formatTime(request.Now), string(request.RunID), string(domain.JobRunQueued),
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
	query, args = appendWindow(query, args, options.Limit, options.Offset)
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
