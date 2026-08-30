package sqlite

import (
	"context"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

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
		formatTime(run.LeaseUntil), formatTime(run.StartedAt),
		run.Version, formatTime(run.UpdatedAt), string(run.ID),
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
		formatTime(request.LeaseUntil), formatTime(request.Now),
		string(request.RunID), string(domain.JobRunRunning), request.WorkerID, request.LeaseToken,
		formatTime(request.Now))
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
