package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

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
	return string(scheduleID) + ":" + formatTime(scheduledFor)
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
		formatTime(run.ScheduledFor), nullableTimeValue(run.RetryAt),
		nullableTimeValue(run.StartedAt), nullableTimeValue(run.FinishedAt), run.Error, run.ResultRef,
		run.Version, formatTime(run.CreatedAt), formatTime(run.UpdatedAt))
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
		next = formatTime(nextRunAt)
	}
	result, err := transaction.ExecContext(ctx, `
		UPDATE schedules SET status = ?, enabled = ?, next_run_at = ?, last_run_at = ?,
			version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND status = ? AND enabled = 1 AND next_run_at = ?`,
		string(status), boolInt(enabled), next, nullableTimeValue(lastRunAt), formatTime(now),
		string(schedule.ID), schedule.Version, string(domain.ScheduleStatusActive), formatTime(schedule.NextRunAt))
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
	if errors.Is(err, sql.ErrNoRows) {
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
		ORDER BY j.lease_until ASC, j.id ASC`, string(domain.JobRunRunning), formatTime(now))
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
		var retry any = formatTime(now)
		var finished any
		if item.attempt >= item.maxAttempts {
			state = domain.JobRunFailed
			errorMessage = "lease expired; retry limit exhausted"
			retry = nil
			finished = formatTime(now)
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE job_runs SET state = ?, lease_owner = '', lease_token = '', lease_until = NULL,
				retry_at = ?, finished_at = ?, error = ?, updated_at = ?, version = version + 1
			WHERE id = ? AND state = ?`, string(state), retry, finished, errorMessage,
			formatTime(now), string(item.id), string(domain.JobRunRunning)); err != nil {
			return 0, wrappedSQLError("recover expired lease", err)
		}
	}
	return len(expiredRuns), nil
}

// pruneJobHistoryTx keeps the newest `limit` terminal runs of a schedule and
// deletes every older one. It runs inside the write transaction of every
// claim, completion, failure and cancellation, so it must not scale with the
// size of the history.
//
// The whole prune is one bounded statement. The previous version read every
// terminal id of the schedule into a Go slice with no LIMIT, trimmed the tail
// in Go and then issued one DELETE per row; it also checked only rows.Close()
// after the loop, so an iteration cut short by an error looked like a short
// history and silently pruned nothing. Deleting through a subquery removes
// both problems at once: no candidate id is materialised in Go, and there is
// no row iteration left whose error could be swallowed.
func pruneJobHistoryTx(ctx context.Context, transaction *sql.Tx, scheduleID domain.ID, limit int) error {
	if limit < 1 {
		return fmt.Errorf("%w: history limit must be positive", domain.ErrInvalidArgument)
	}
	// SQLite documents "LIMIT -1" as the way to skip rows without bounding the
	// result, which is what appendWindow emits for the same offset-only case.
	if _, err := transaction.ExecContext(ctx, `
		DELETE FROM job_runs WHERE id IN (
			SELECT id FROM job_runs WHERE schedule_id = ? AND state IN (?, ?, ?, ?)
			ORDER BY created_at DESC, id DESC LIMIT -1 OFFSET ?
		)`, string(scheduleID), string(domain.JobRunSucceeded), string(domain.JobRunFailed),
		string(domain.JobRunCancelled), string(domain.JobRunSkipped), limit); err != nil {
		return wrappedSQLError("prune old job history", err)
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
