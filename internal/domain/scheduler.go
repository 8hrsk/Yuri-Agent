package domain

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ScheduleKind describes how a durable schedule calculates its occurrences.
// Cron expressions use the standard five-field format (minute, hour, day of
// month, month, day of week); parsing is owned by the scheduler package so the
// domain remains independent from a particular cron implementation.
type ScheduleKind string

const (
	ScheduleKindOnce     ScheduleKind = "once"
	ScheduleKindInterval ScheduleKind = "interval"
	ScheduleKindCron     ScheduleKind = "cron"
)

func (k ScheduleKind) Valid() bool {
	switch k {
	case ScheduleKindOnce, ScheduleKindInterval, ScheduleKindCron:
		return true
	default:
		return false
	}
}

// ScheduleStatus is deliberately separate from Enabled. A completed one-shot
// schedule is retained for history and can still be invoked manually, while
// a deleted schedule is never executable again.
type ScheduleStatus string

const (
	ScheduleStatusActive    ScheduleStatus = "active"
	ScheduleStatusPaused    ScheduleStatus = "paused"
	ScheduleStatusCompleted ScheduleStatus = "completed"
	ScheduleStatusDeleted   ScheduleStatus = "deleted"
)

func (s ScheduleStatus) Valid() bool {
	switch s {
	case ScheduleStatusActive, ScheduleStatusPaused, ScheduleStatusCompleted, ScheduleStatusDeleted:
		return true
	default:
		return false
	}
}

// MisfirePolicy determines what happens when the process wakes after an
// occurrence has already passed. Skip records a durable skipped run and moves
// to the next occurrence. RunOnce executes one occurrence and discards the
// rest of the backlog.
type MisfirePolicy string

const (
	MisfireSkip    MisfirePolicy = "skip"
	MisfireRunOnce MisfirePolicy = "run_once"
)

func (p MisfirePolicy) Valid() bool {
	switch p {
	case MisfireSkip, MisfireRunOnce:
		return true
	default:
		return false
	}
}

// JobRunState is the durable lifecycle of one scheduled invocation.
type JobRunState string

const (
	JobRunQueued    JobRunState = "queued"
	JobRunRunning   JobRunState = "running"
	JobRunSucceeded JobRunState = "succeeded"
	JobRunFailed    JobRunState = "failed"
	JobRunCancelled JobRunState = "cancelled"
	JobRunSkipped   JobRunState = "skipped"
)

func (s JobRunState) Valid() bool {
	switch s {
	case JobRunQueued, JobRunRunning, JobRunSucceeded, JobRunFailed, JobRunCancelled, JobRunSkipped:
		return true
	default:
		return false
	}
}

func (s JobRunState) Terminal() bool {
	switch s {
	case JobRunSucceeded, JobRunFailed, JobRunCancelled, JobRunSkipped:
		return true
	default:
		return false
	}
}

// JobTrigger explains why a job entered the worker queue. Keeping this on the
// durable row lets pause/resume and manual execution have unambiguous behavior
// after a restart.
type JobTrigger string

const (
	JobTriggerScheduled JobTrigger = "scheduled"
	JobTriggerManual    JobTrigger = "manual"
)

func (t JobTrigger) Valid() bool {
	switch t {
	case JobTriggerScheduled, JobTriggerManual:
		return true
	default:
		return false
	}
}

// RetryPolicy bounds automatic retries for one scheduled invocation. Attempts
// are one-based once a worker claims a job. MaxAttempts=1 disables retries.
type RetryPolicy struct {
	MaxAttempts          int `json:"max_attempts"`
	InitialBackoffSecond int `json:"initial_backoff_seconds"`
	MaxBackoffSecond     int `json:"max_backoff_seconds"`
}

func (p RetryPolicy) Valid() bool {
	return p.MaxAttempts >= 1 && p.MaxAttempts <= 100 &&
		p.InitialBackoffSecond >= 0 && p.InitialBackoffSecond <= 7*24*60*60 &&
		p.MaxBackoffSecond >= p.InitialBackoffSecond && p.MaxBackoffSecond <= 30*24*60*60
}

// JobBudget is passed unchanged to an Executor. The worker owns timeouts and
// the downstream agent runtime owns token/tool accounting.
type JobBudget struct {
	MaxDurationSeconds int   `json:"max_duration_seconds"`
	MaxTokens          int64 `json:"max_tokens"`
	MaxToolCalls       int   `json:"max_tool_calls"`
}

func (b JobBudget) Valid() bool {
	return b.MaxDurationSeconds >= 0 && b.MaxDurationSeconds <= 7*24*60*60 &&
		b.MaxTokens >= 0 && b.MaxTokens <= 1<<62 && b.MaxToolCalls >= 0 && b.MaxToolCalls <= 100000
}

// Schedule is the authoritative durable definition of a background task.
// PayloadJSON is intentionally opaque to the scheduler; the agent/application
// layer decides how to interpret it and the scheduler only guarantees durable
// delivery, retry, and execution bounds.
type Schedule struct {
	ID              ID             `json:"id"`
	Name            string         `json:"name"`
	Kind            ScheduleKind   `json:"kind"`
	Expression      string         `json:"expression,omitempty"`
	Timezone        string         `json:"timezone"`
	StartAt         time.Time      `json:"start_at"`
	IntervalSeconds int64          `json:"interval_seconds,omitempty"`
	PayloadJSON     string         `json:"payload_json"`
	Status          ScheduleStatus `json:"status"`
	Enabled         bool           `json:"enabled"`
	MisfirePolicy   MisfirePolicy  `json:"misfire_policy"`
	NextRunAt       time.Time      `json:"next_run_at,omitempty"`
	LastRunAt       time.Time      `json:"last_run_at,omitempty"`
	Retry           RetryPolicy    `json:"retry"`
	Budget          JobBudget      `json:"budget"`
	AllowOverlap    bool           `json:"allow_overlap"`
	HistoryLimit    int            `json:"history_limit"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	Version         uint64         `json:"version"`
}

func (s Schedule) Valid() bool {
	return !s.ID.Empty() && strings.TrimSpace(s.Name) != "" && s.Kind.Valid() &&
		strings.TrimSpace(s.Timezone) != "" && !s.StartAt.IsZero() &&
		!s.CreatedAt.IsZero() && !s.UpdatedAt.IsZero() && s.Status.Valid() &&
		s.MisfirePolicy.Valid() && s.Retry.Valid() && s.Budget.Valid() &&
		s.HistoryLimit >= 1 && s.HistoryLimit <= 100000 && s.Version >= 1 &&
		s.Enabled == (s.Status == ScheduleStatusActive) &&
		((s.Kind == ScheduleKindInterval && s.IntervalSeconds > 0) ||
			s.Kind != ScheduleKindInterval) &&
		((s.Status == ScheduleStatusActive && !s.NextRunAt.IsZero()) ||
			s.Status != ScheduleStatusActive)
}

// JobRun is one durable invocation. ExecutionKey is stable across lease
// recovery and retries so an executor can make side effects idempotent.
type JobRun struct {
	ID           ID          `json:"id"`
	ScheduleID   ID          `json:"schedule_id"`
	State        JobRunState `json:"state"`
	Trigger      JobTrigger  `json:"trigger"`
	Attempt      int         `json:"attempt"`
	ExecutionKey string      `json:"execution_key"`
	LeaseOwner   string      `json:"lease_owner,omitempty"`
	LeaseToken   string      `json:"lease_token,omitempty"`
	LeaseUntil   time.Time   `json:"lease_until,omitempty"`
	ScheduledFor time.Time   `json:"scheduled_for"`
	RetryAt      time.Time   `json:"retry_at,omitempty"`
	StartedAt    time.Time   `json:"started_at,omitempty"`
	FinishedAt   time.Time   `json:"finished_at,omitempty"`
	Error        string      `json:"error,omitempty"`
	ResultRef    string      `json:"result_ref,omitempty"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
	Version      uint64      `json:"version"`
}

func (r JobRun) Valid() bool {
	return !r.ID.Empty() && !r.ScheduleID.Empty() && r.State.Valid() && r.Trigger.Valid() &&
		r.Attempt >= 0 && r.Attempt <= 100 && strings.TrimSpace(r.ExecutionKey) != "" &&
		!r.ScheduledFor.IsZero() && !r.CreatedAt.IsZero() && !r.UpdatedAt.IsZero() && r.Version >= 1
}

// ScheduledJob is the immutable snapshot handed to an executor. The snapshot
// prevents a concurrent schedule edit from changing an already claimed job.
type ScheduledJob struct {
	Schedule Schedule `json:"schedule"`
	Run      JobRun   `json:"run"`
}

type ScheduleListOptions struct {
	IncludePaused    bool
	IncludeCompleted bool
	IncludeDeleted   bool
	Limit            int
	Offset           int
}

type JobRunListOptions struct {
	// An empty schedule ID means all schedules; callers can use this for a
	// global activity/history view.
	Limit  int
	Offset int
}

// ScheduledClaim is supplied by the scheduler after calculating the next
// occurrence. Repository implementations atomically validate the expected
// schedule version, create the running job, and advance the schedule.
type ScheduledClaim struct {
	ScheduleID      ID
	ExpectedVersion uint64
	ScheduledFor    time.Time
	NextRunAt       time.Time
	Now             time.Time
	WorkerID        string
	LeaseDuration   time.Duration
}

type MisfireRecord struct {
	ScheduleID      ID
	ExpectedVersion uint64
	ScheduledFor    time.Time
	NextRunAt       time.Time
	Now             time.Time
	Reason          string
}

type ManualRunRequest struct {
	ScheduleID    ID
	Now           time.Time
	WorkerID      string
	LeaseDuration time.Duration
}

type RetryClaim struct {
	RunID         ID
	Now           time.Time
	WorkerID      string
	LeaseDuration time.Duration
}

type RenewLeaseRequest struct {
	RunID      ID
	WorkerID   string
	LeaseToken string
	LeaseUntil time.Time
	Now        time.Time
}

type CompleteRunRequest struct {
	RunID      ID
	WorkerID   string
	LeaseToken string
	Now        time.Time
	ResultRef  string
}

type FailRunRequest struct {
	RunID      ID
	WorkerID   string
	LeaseToken string
	Now        time.Time
	Reason     string
}

type CancelRunRequest struct {
	RunID      ID
	WorkerID   string
	LeaseToken string
	Now        time.Time
}

// SchedulerRepository is the storage port used by the durable worker. The
// claim methods are intentionally narrower than generic CRUD: each performs
// all compare-and-set checks and state changes in one SQLite transaction.
type SchedulerRepository interface {
	CreateSchedule(context.Context, Schedule) error
	GetSchedule(context.Context, ID) (Schedule, error)
	ListSchedules(context.Context, ScheduleListOptions) ([]Schedule, error)
	SaveSchedule(context.Context, Schedule) error
	DeleteSchedule(context.Context, ID, time.Time) error

	ListDueSchedules(context.Context, time.Time, int) ([]Schedule, error)
	ListRetryableRuns(context.Context, time.Time, int) ([]JobRun, error)
	ClaimScheduled(context.Context, ScheduledClaim) (ScheduledJob, error)
	RecordMisfire(context.Context, MisfireRecord) (JobRun, error)
	EnqueueManualRun(context.Context, ManualRunRequest) (JobRun, error)
	ClaimRetry(context.Context, RetryClaim) (ScheduledJob, error)
	RenewLease(context.Context, RenewLeaseRequest) error
	CompleteRun(context.Context, CompleteRunRequest) error
	FailRun(context.Context, FailRunRequest) error
	CancelRun(context.Context, CancelRunRequest) error
	RecoverExpiredLeases(context.Context, time.Time) (int, error)
	GetJobRun(context.Context, ID) (JobRun, error)
	ListJobRuns(context.Context, ID, JobRunListOptions) ([]JobRun, error)
}

// ValidateBasic checks fields that do not require a cron parser. The
// scheduler package performs the complete validation before persistence.
func (s Schedule) ValidateBasic() error {
	if s.ID.Empty() || strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("%w: schedule id and name are required", ErrInvalidArgument)
	}
	if !s.Kind.Valid() {
		return fmt.Errorf("%w: invalid schedule kind %q", ErrInvalidArgument, s.Kind)
	}
	if strings.TrimSpace(s.Timezone) == "" {
		return fmt.Errorf("%w: schedule timezone is required", ErrInvalidArgument)
	}
	if s.StartAt.IsZero() || s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: schedule timestamps are required", ErrInvalidArgument)
	}
	if !s.Status.Valid() || !s.MisfirePolicy.Valid() || !s.Retry.Valid() || !s.Budget.Valid() {
		return fmt.Errorf("%w: invalid schedule policy", ErrInvalidArgument)
	}
	if s.Version == 0 || s.HistoryLimit < 1 || s.HistoryLimit > 100000 {
		return fmt.Errorf("%w: invalid schedule version or history limit", ErrInvalidArgument)
	}
	if s.Kind == ScheduleKindInterval && s.IntervalSeconds <= 0 {
		return fmt.Errorf("%w: interval_seconds must be positive", ErrInvalidArgument)
	}
	if s.Status == ScheduleStatusActive && (!s.Enabled || s.NextRunAt.IsZero()) {
		return fmt.Errorf("%w: active schedule must be enabled and have next_run_at", ErrInvalidArgument)
	}
	if s.Status != ScheduleStatusActive && s.Enabled {
		return fmt.Errorf("%w: non-active schedule cannot be enabled", ErrInvalidArgument)
	}
	return nil
}
