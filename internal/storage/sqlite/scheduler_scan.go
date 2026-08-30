package sqlite

import (
	"database/sql"
	"errors"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

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
		if errors.Is(err, sql.ErrNoRows) {
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
		if errors.Is(err, sql.ErrNoRows) {
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
