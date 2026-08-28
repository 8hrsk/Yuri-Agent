// Package scheduler contains the durable schedule coordinator. It deliberately
// knows nothing about Wails, notifications, or the agent runtime: an Executor
// supplied by the application translates an opaque schedule payload into work.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

type Schedule = domain.Schedule
type JobRun = domain.JobRun
type ScheduledJob = domain.ScheduledJob
type Repository = domain.SchedulerRepository

var (
	ErrExecutorRequired = errors.New("scheduler: executor is required")
	ErrAlreadyStarted   = errors.New("scheduler: worker is already started")
	ErrNotStarted       = errors.New("scheduler: worker is not started")
	ErrStopTimeout      = errors.New("scheduler: worker stop timed out")
)

// Executor performs one claimed invocation. The claim contains an immutable
// schedule snapshot and a stable Run.ExecutionKey so external side effects can
// be made idempotent across retry/lease recovery.
type Executor interface {
	Execute(context.Context, ScheduledJob) error
}

// ExecuteFunc adapts an ordinary function to Executor.
type ExecuteFunc func(context.Context, ScheduledJob) error

func (f ExecuteFunc) Execute(ctx context.Context, job ScheduledJob) error {
	if f == nil {
		return ErrExecutorRequired
	}
	return f(ctx, job)
}

type Options struct {
	Clock             domain.Clock
	WorkerID          string
	LeaseDuration     time.Duration
	PollInterval      time.Duration
	MaxClaimsPerCycle int
	// StopTimeout bounds how long Stop waits for an executor and the current
	// worker cycle to return. A timed-out worker remains in the stopping state,
	// so a new worker cannot overlap it; a later Stop can be used to join it.
	StopTimeout time.Duration
}

const (
	defaultLeaseDuration = 2 * time.Minute
	defaultPollInterval  = 15 * time.Second
	defaultMaxClaims     = 16
	defaultStopTimeout   = 5 * time.Second
)

// RunDueResult summarizes one polling cycle. Executor failures are persisted
// to the job row and counted here; they do not abort unrelated jobs.
type RunDueResult struct {
	Recovered int
	Claimed   int
	Completed int
	Failed    int
	Skipped   int
}

// ValidateSchedule validates timezone, payload, schedule shape, and (for cron
// schedules) the complete five-field expression before a schedule is written.
func ValidateSchedule(schedule Schedule) error { return validateSchedule(schedule) }

// NextOccurrence returns the first occurrence strictly after after. A
// one-shot schedule returns a zero time once its occurrence has passed.
func NextOccurrence(schedule Schedule, after time.Time) (time.Time, error) {
	return nextOccurrence(schedule, after)
}

type Scheduler struct {
	repository  Repository
	executor    Executor
	clock       domain.Clock
	workerID    string
	lease       time.Duration
	poll        time.Duration
	maxClaims   int
	stopTimeout time.Duration
	startMu     sync.Mutex
	workerDone  chan struct{}
	workerStop  context.CancelFunc
	workerState workerState
	generation  uint64
	activeMu    sync.Mutex
	activeRuns  map[domain.ID]context.CancelFunc
}

// workerState is protected by startMu. A separate stopping state is needed so
// Start cannot launch a second worker while Stop is still joining the first
// one, even if the first worker is blocked inside an executor.
type workerState uint8

const (
	workerStopped workerState = iota
	workerRunning
	workerStopping
)

// Worker is an architectural alias used by callers that refer to the
// polling component as a worker rather than a scheduler.
type Worker = Scheduler

func New(repository Repository, executor Executor, options Options) (*Scheduler, error) {
	if repository == nil {
		return nil, fmt.Errorf("%w: repository is required", domain.ErrInvalidArgument)
	}
	if executor == nil {
		return nil, ErrExecutorRequired
	}
	if options.Clock == nil {
		options.Clock = domain.SystemClock{}
	}
	if strings.TrimSpace(options.WorkerID) == "" {
		generated, err := domain.NewID("worker")
		if err != nil {
			return nil, fmt.Errorf("generate scheduler worker id: %w", err)
		}
		options.WorkerID = string(generated)
	}
	if options.LeaseDuration == 0 {
		options.LeaseDuration = defaultLeaseDuration
	}
	if options.PollInterval == 0 {
		options.PollInterval = defaultPollInterval
	}
	if options.MaxClaimsPerCycle == 0 {
		options.MaxClaimsPerCycle = defaultMaxClaims
	}
	if options.StopTimeout == 0 {
		options.StopTimeout = defaultStopTimeout
	}
	if options.LeaseDuration <= 0 || options.LeaseDuration > 30*24*time.Hour ||
		options.PollInterval <= 0 || options.MaxClaimsPerCycle < 1 || options.MaxClaimsPerCycle > 10000 ||
		options.StopTimeout <= 0 || options.StopTimeout > 30*24*time.Hour {
		return nil, fmt.Errorf("%w: invalid scheduler worker options", domain.ErrInvalidArgument)
	}
	return &Scheduler{
		repository: repository, executor: executor, clock: options.Clock,
		workerID: options.WorkerID, lease: options.LeaseDuration,
		poll: options.PollInterval, maxClaims: options.MaxClaimsPerCycle,
		activeRuns:  make(map[domain.ID]context.CancelFunc),
		stopTimeout: options.StopTimeout,
	}, nil
}

// NewWorker is the explicit worker-named constructor; it is equivalent to
// New and exists so application wiring can use either vocabulary.
func NewWorker(repository Repository, executor Executor, options Options) (*Worker, error) {
	return New(repository, executor, options)
}

// CreateSchedule validates the complete schedule, including cron syntax, and
// then persists it. Callers should provide Version=1 and an already calculated
// NextRunAt; NewSchedule helpers below make that setup convenient.
func (s *Scheduler) CreateSchedule(ctx context.Context, schedule Schedule) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	normalized, err := normalizeNewSchedule(schedule, s.clock.Now())
	if err != nil {
		return err
	}
	return s.repository.CreateSchedule(ctx, normalized)
}

func (s *Scheduler) GetSchedule(ctx context.Context, id domain.ID) (Schedule, error) {
	if err := contextError(ctx); err != nil {
		return Schedule{}, err
	}
	return s.repository.GetSchedule(ctx, id)
}

func (s *Scheduler) ListSchedules(ctx context.Context, options domain.ScheduleListOptions) ([]Schedule, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return s.repository.ListSchedules(ctx, options)
}

func (s *Scheduler) GetJobRun(ctx context.Context, id domain.ID) (JobRun, error) {
	if err := contextError(ctx); err != nil {
		return JobRun{}, err
	}
	return s.repository.GetJobRun(ctx, id)
}

func (s *Scheduler) ListJobRuns(ctx context.Context, scheduleID domain.ID, options domain.JobRunListOptions) ([]JobRun, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return s.repository.ListJobRuns(ctx, scheduleID, options)
}

// UpdateSchedule applies a user-visible schedule edit with optimistic
// concurrency. The caller may pass either the version read from GetSchedule
// (the method increments it) or the already incremented next version. ID and
// CreatedAt are immutable and always taken from the authoritative row.
func (s *Scheduler) UpdateSchedule(ctx context.Context, edited Schedule) (Schedule, error) {
	if err := contextError(ctx); err != nil {
		return Schedule{}, err
	}
	if edited.ID.Empty() {
		return Schedule{}, fmt.Errorf("%w: schedule id is required", domain.ErrInvalidArgument)
	}
	current, err := s.repository.GetSchedule(ctx, edited.ID)
	if err != nil {
		return Schedule{}, err
	}
	if edited.Version != 0 && edited.Version != current.Version && edited.Version != current.Version+1 {
		return Schedule{}, domain.ErrConflict
	}
	if !edited.CreatedAt.IsZero() && !edited.CreatedAt.Equal(current.CreatedAt) {
		return Schedule{}, fmt.Errorf("%w: schedule created_at is immutable", domain.ErrInvalidArgument)
	}
	edited.ID = current.ID
	edited.CreatedAt = current.CreatedAt
	edited.Version = current.Version + 1
	edited.UpdatedAt = s.clock.Now().UTC()
	if edited.NextRunAt.IsZero() && edited.Status == domain.ScheduleStatusActive {
		edited.NextRunAt, err = initialOccurrence(edited, edited.UpdatedAt)
		if err != nil {
			return Schedule{}, err
		}
	}
	if err := validateSchedule(edited); err != nil {
		return Schedule{}, err
	}
	if err := s.repository.SaveSchedule(ctx, edited); err != nil {
		return Schedule{}, err
	}
	return edited, nil
}

// Pause changes only schedule state. Already claimed jobs are allowed to
// finish; queued scheduled retries wait until Resume.
func (s *Scheduler) Pause(ctx context.Context, id domain.ID) (Schedule, error) {
	return s.setScheduleState(ctx, id, domain.ScheduleStatusPaused, false)
}

func (s *Scheduler) Resume(ctx context.Context, id domain.ID) (Schedule, error) {
	if err := contextError(ctx); err != nil {
		return Schedule{}, err
	}
	schedule, err := s.repository.GetSchedule(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	if schedule.Status == domain.ScheduleStatusDeleted || schedule.Status == domain.ScheduleStatusCompleted {
		return Schedule{}, fmt.Errorf("%w: schedule %s cannot be resumed", domain.ErrInvalidTransition, id)
	}
	schedule.Status = domain.ScheduleStatusActive
	schedule.Enabled = true
	if schedule.NextRunAt.IsZero() {
		if schedule.Kind == domain.ScheduleKindOnce {
			// A paused one-shot may be resumed after its wall-clock time. Keep
			// the original occurrence due so RunDue can apply its explicit
			// misfire policy instead of producing an invalid active schedule.
			schedule.NextRunAt = schedule.StartAt.UTC()
		} else {
			next, err := nextOccurrence(schedule, s.clock.Now().UTC().Add(-time.Nanosecond))
			if err != nil {
				return Schedule{}, err
			}
			schedule.NextRunAt = next
		}
	}
	schedule.Version++
	schedule.UpdatedAt = s.clock.Now().UTC()
	if err := s.repository.SaveSchedule(ctx, schedule); err != nil {
		return Schedule{}, err
	}
	return schedule, nil
}

func (s *Scheduler) Delete(ctx context.Context, id domain.ID) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	return s.repository.DeleteSchedule(ctx, id, s.clock.Now().UTC())
}

// ManualRun creates a durable manual invocation. It is intentionally separate
// from RunDue so a UI can display the queued state and a later worker can claim
// it after a restart.
func (s *Scheduler) ManualRun(ctx context.Context, id domain.ID) (JobRun, error) {
	if err := contextError(ctx); err != nil {
		return JobRun{}, err
	}
	return s.repository.EnqueueManualRun(ctx, domain.ManualRunRequest{
		ScheduleID: id, Now: s.clock.Now().UTC(), WorkerID: s.workerID, LeaseDuration: s.lease,
	})
}

func (s *Scheduler) CancelRun(ctx context.Context, id domain.ID) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	s.activeMu.Lock()
	activeCancel := s.activeRuns[id]
	s.activeMu.Unlock()
	if activeCancel != nil {
		activeCancel()
		return nil
	}
	return s.repository.CancelRun(ctx, domain.CancelRunRequest{RunID: id, Now: s.clock.Now().UTC()})
}

// StopRun is a descriptive alias used by UI/application callers.
func (s *Scheduler) StopRun(ctx context.Context, id domain.ID) error {
	return s.CancelRun(ctx, id)
}

// RunDue performs one bounded worker cycle. It recovers expired leases, claims
// due retry/manual rows first, then schedules newly due occurrences. A claim
// conflict means another worker won the compare-and-set and is ignored.
func (s *Scheduler) RunDue(ctx context.Context) (RunDueResult, error) {
	if err := contextError(ctx); err != nil {
		return RunDueResult{}, err
	}
	now := s.clock.Now().UTC()
	result := RunDueResult{}
	recovered, err := s.repository.RecoverExpiredLeases(ctx, now)
	if err != nil {
		return result, err
	}
	result.Recovered = recovered
	remaining := s.maxClaims
	if retryable, err := s.repository.ListRetryableRuns(ctx, now, remaining); err != nil {
		return result, err
	} else {
		for _, queued := range retryable {
			if remaining == 0 {
				break
			}
			job, err := s.repository.ClaimRetry(ctx, domain.RetryClaim{
				RunID: queued.ID, Now: now, WorkerID: s.workerID, LeaseDuration: s.lease,
			})
			if errors.Is(err, domain.ErrConflict) {
				continue
			}
			if err != nil {
				return result, err
			}
			remaining--
			result.Claimed++
			if s.executeClaim(ctx, job) {
				result.Completed++
			} else {
				result.Failed++
			}
		}
	}
	if remaining == 0 {
		return result, nil
	}
	due, err := s.repository.ListDueSchedules(ctx, now, remaining)
	if err != nil {
		return result, err
	}
	for _, schedule := range due {
		if remaining == 0 {
			break
		}
		scheduledFor := schedule.NextRunAt
		if scheduledFor.IsZero() {
			continue
		}
		if now.After(scheduledFor) && schedule.MisfirePolicy == domain.MisfireSkip {
			next, err := nextOccurrence(schedule, now)
			if err != nil {
				return result, err
			}
			if _, err := s.repository.RecordMisfire(ctx, domain.MisfireRecord{
				ScheduleID: schedule.ID, ExpectedVersion: schedule.Version,
				ScheduledFor: scheduledFor, NextRunAt: next, Now: now,
				Reason: "occurrence passed while worker was unavailable",
			}); errors.Is(err, domain.ErrConflict) {
				continue
			} else if err != nil {
				return result, err
			}
			result.Skipped++
			continue
		}
		next, err := nextOccurrence(schedule, scheduledFor)
		if err != nil {
			return result, err
		}
		// RunOnce executes one overdue occurrence and discards all older
		// backlog. For an on-time occurrence this is equivalent to the
		// normal next value.
		if now.After(scheduledFor) && schedule.MisfirePolicy == domain.MisfireRunOnce && !next.IsZero() && !next.After(now) {
			next, err = nextOccurrence(schedule, now)
			if err != nil {
				return result, err
			}
		}
		job, err := s.repository.ClaimScheduled(ctx, domain.ScheduledClaim{
			ScheduleID: schedule.ID, ExpectedVersion: schedule.Version,
			ScheduledFor: scheduledFor, NextRunAt: next, Now: now,
			WorkerID: s.workerID, LeaseDuration: s.lease,
		})
		if errors.Is(err, domain.ErrConflict) {
			continue
		}
		if err != nil {
			return result, err
		}
		remaining--
		result.Claimed++
		if s.executeClaim(ctx, job) {
			result.Completed++
		} else {
			result.Failed++
		}
	}
	return result, nil
}

// RunOnce is a worker-oriented alias for one bounded RunDue cycle.
func (s *Scheduler) RunOnce(ctx context.Context) (RunDueResult, error) {
	return s.RunDue(ctx)
}

func (s *Scheduler) executeClaim(parent context.Context, job ScheduledJob) bool {
	ctx := parent
	var cancel context.CancelFunc
	if job.Schedule.Budget.MaxDurationSeconds > 0 {
		ctx, cancel = context.WithTimeout(parent, time.Duration(job.Schedule.Budget.MaxDurationSeconds)*time.Second)
	} else {
		ctx, cancel = context.WithCancel(parent)
	}
	defer cancel()
	s.activeMu.Lock()
	s.activeRuns[job.Run.ID] = cancel
	s.activeMu.Unlock()
	defer func() {
		s.activeMu.Lock()
		delete(s.activeRuns, job.Run.ID)
		s.activeMu.Unlock()
	}()

	done := make(chan struct{})
	renewFailed := make(chan error, 1)
	tickDuration := s.lease / 3
	if tickDuration < 100*time.Millisecond {
		tickDuration = 100 * time.Millisecond
	}
	go func() {
		ticker := time.NewTicker(tickDuration)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := s.clock.Now().UTC()
				// Keep every storage call bounded. A blocked SQLite driver or
				// repository must not leave the renewal goroutine (and therefore
				// Stop) waiting forever. The renewal deadline is deliberately
				// shorter than the lease itself.
				renewCtx, renewCancel := context.WithTimeout(ctx, tickDuration)
				err := s.repository.RenewLease(renewCtx, domain.RenewLeaseRequest{
					RunID: job.Run.ID, WorkerID: s.workerID, LeaseToken: job.Run.LeaseToken,
					LeaseUntil: now.Add(s.lease), Now: now,
				})
				renewCancel()
				if err != nil {
					// The executor has already returned and the local context is
					// being cancelled to stop the renewal loop. That is normal and
					// must not turn a successful execution into a lease failure.
					select {
					case <-done:
						return
					default:
					}
					if ctx.Err() != nil {
						return
					}
					select {
					case renewFailed <- err:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()

	executeErr := s.executor.Execute(ctx, job)
	executionContextErr := ctx.Err()
	close(done)
	// Cancel the execution context as soon as the executor returns so an
	// in-flight bounded renewal call is released without waiting for its
	// timeout. The parent context is checked separately below.
	cancel()
	select {
	case err := <-renewFailed:
		if executeErr == nil {
			executeErr = fmt.Errorf("lease renewal failed: %w", err)
		}
	default:
	}
	now := s.clock.Now().UTC()
	if executeErr == nil && executionContextErr != nil {
		executeErr = executionContextErr
	}
	if executeErr == nil {
		if parent.Err() != nil {
			_ = s.repository.CancelRun(context.Background(), domain.CancelRunRequest{
				RunID: job.Run.ID, WorkerID: s.workerID, LeaseToken: job.Run.LeaseToken, Now: now,
			})
			return false
		}
		return s.repository.CompleteRun(context.Background(), domain.CompleteRunRequest{
			RunID: job.Run.ID, WorkerID: s.workerID, LeaseToken: job.Run.LeaseToken,
			Now: now,
		}) == nil
	}
	if parent.Err() != nil {
		_ = s.repository.CancelRun(context.Background(), domain.CancelRunRequest{
			RunID: job.Run.ID, WorkerID: s.workerID, LeaseToken: job.Run.LeaseToken, Now: now,
		})
		return false
	}
	if errors.Is(executeErr, context.Canceled) {
		_ = s.repository.CancelRun(context.Background(), domain.CancelRunRequest{
			RunID: job.Run.ID, WorkerID: s.workerID, LeaseToken: job.Run.LeaseToken, Now: now,
		})
		return false
	}
	_ = s.repository.FailRun(context.Background(), domain.FailRunRequest{
		RunID: job.Run.ID, WorkerID: s.workerID, LeaseToken: job.Run.LeaseToken,
		Now: now, Reason: executeErr.Error(),
	})
	return false
}

// Start launches a polling worker. Calling Start while the worker is running
// or stopping is an error. The explicit stopping state prevents a concurrent
// Start from overlapping the worker that Stop is still joining.
func (s *Scheduler) Start(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if s.workerState != workerStopped {
		return ErrAlreadyStarted
	}
	workerCtx, cancel := context.WithCancel(ctx)
	s.generation++
	generation := s.generation
	done := make(chan struct{})
	s.workerState = workerRunning
	s.workerStop = cancel
	s.workerDone = done
	go func() {
		defer func() {
			close(done)
			s.startMu.Lock()
			// A worker may finish because its parent context was cancelled,
			// or because Stop requested a join. In either case, only the
			// matching generation may release the lifecycle state.
			if s.generation == generation && s.workerDone == done && s.workerState != workerStopped {
				s.workerState = workerStopped
				s.workerStop = nil
				s.workerDone = nil
			}
			s.startMu.Unlock()
		}()
		ticker := time.NewTicker(s.poll)
		defer ticker.Stop()
		_, _ = s.RunDue(workerCtx)
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				_, _ = s.RunDue(workerCtx)
			}
		}
	}()
	return nil
}

func (s *Scheduler) Stop() error {
	s.startMu.Lock()
	if s.workerState == workerStopped {
		s.startMu.Unlock()
		return nil
	}
	cancel := s.workerStop
	done := s.workerDone
	generation := s.generation
	s.workerState = workerStopping
	stopTimeout := s.stopTimeout
	cancel()
	s.startMu.Unlock()

	if stopTimeout <= 0 {
		stopTimeout = defaultStopTimeout
	}
	timer := time.NewTimer(stopTimeout)
	defer timer.Stop()
	select {
	case <-done:
		// Normally the worker's defer has already cleared this state. Keep
		// this CAS-style cleanup for the tiny window between close(done) and
		// that defer acquiring startMu, and for concurrent Stop callers.
		s.startMu.Lock()
		if s.generation == generation && s.workerDone == done && s.workerState == workerStopping {
			s.workerState = workerStopped
			s.workerStop = nil
			s.workerDone = nil
		}
		s.startMu.Unlock()
		return nil
	case <-timer.C:
		// Leave workerState=workerStopping. If the executor eventually
		// returns, the worker defer will transition it to stopped; until then
		// Start is rejected and no overlapping side effects are possible.
		return ErrStopTimeout
	}
}

func (s *Scheduler) setScheduleState(ctx context.Context, id domain.ID, status domain.ScheduleStatus, enabled bool) (Schedule, error) {
	if err := contextError(ctx); err != nil {
		return Schedule{}, err
	}
	schedule, err := s.repository.GetSchedule(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	if schedule.Status == domain.ScheduleStatusDeleted || schedule.Status == domain.ScheduleStatusCompleted {
		return Schedule{}, fmt.Errorf("%w: schedule %s cannot change state", domain.ErrInvalidTransition, id)
	}
	schedule.Status = status
	schedule.Enabled = enabled
	schedule.Version++
	schedule.UpdatedAt = s.clock.Now().UTC()
	if err := s.repository.SaveSchedule(ctx, schedule); err != nil {
		return Schedule{}, err
	}
	return schedule, nil
}

func normalizeNewSchedule(schedule Schedule, now time.Time) (Schedule, error) {
	if schedule.Version == 0 {
		schedule.Version = 1
	}
	if schedule.Status == "" {
		schedule.Status = domain.ScheduleStatusActive
	}
	if !schedule.Enabled && schedule.Status == domain.ScheduleStatusActive {
		schedule.Enabled = true
	}
	if schedule.MisfirePolicy == "" {
		schedule.MisfirePolicy = domain.MisfireRunOnce
	}
	if schedule.Retry.MaxAttempts == 0 {
		schedule.Retry = domain.RetryPolicy{MaxAttempts: 3, InitialBackoffSecond: 5, MaxBackoffSecond: 300}
	}
	if schedule.HistoryLimit == 0 {
		schedule.HistoryLimit = 100
	}
	if schedule.PayloadJSON == "" {
		schedule.PayloadJSON = "{}"
	}
	if schedule.CreatedAt.IsZero() {
		schedule.CreatedAt = now.UTC()
	}
	if schedule.UpdatedAt.IsZero() {
		schedule.UpdatedAt = now.UTC()
	}
	if schedule.StartAt.IsZero() {
		schedule.StartAt = now.UTC()
	}
	if schedule.NextRunAt.IsZero() && schedule.Status == domain.ScheduleStatusActive {
		next, err := initialOccurrence(schedule, now)
		if err != nil {
			return Schedule{}, err
		}
		schedule.NextRunAt = next
	}
	if err := validateSchedule(schedule); err != nil {
		return Schedule{}, err
	}
	return schedule, nil
}

func initialOccurrence(schedule Schedule, now time.Time) (time.Time, error) {
	if schedule.Kind == domain.ScheduleKindOnce {
		return schedule.StartAt.UTC(), nil
	}
	return nextOccurrence(schedule, now.Add(-time.Nanosecond))
}

func validateSchedule(schedule Schedule) error {
	if err := schedule.ValidateBasic(); err != nil {
		return err
	}
	if _, err := time.LoadLocation(schedule.Timezone); err != nil {
		return fmt.Errorf("%w: invalid schedule timezone: %v", domain.ErrInvalidArgument, err)
	}
	if strings.TrimSpace(schedule.PayloadJSON) == "" {
		return fmt.Errorf("%w: payload_json is required", domain.ErrInvalidArgument)
	}
	if !jsonValid(schedule.PayloadJSON) {
		return fmt.Errorf("%w: payload_json must be valid JSON", domain.ErrInvalidArgument)
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
		if schedule.IntervalSeconds != 0 {
			return fmt.Errorf("%w: cron schedule cannot have interval_seconds", domain.ErrInvalidArgument)
		}
		if _, err := parseCron(schedule.Expression); err != nil {
			return fmt.Errorf("%w: invalid cron expression: %v", domain.ErrInvalidArgument, err)
		}
	}
	return nil
}

func nextOccurrence(schedule Schedule, after time.Time) (time.Time, error) {
	if after.IsZero() {
		return time.Time{}, fmt.Errorf("%w: occurrence reference time is required", domain.ErrInvalidArgument)
	}
	switch schedule.Kind {
	case domain.ScheduleKindOnce:
		if schedule.StartAt.After(after) {
			return schedule.StartAt.UTC(), nil
		}
		return time.Time{}, nil
	case domain.ScheduleKindInterval:
		interval := time.Duration(schedule.IntervalSeconds) * time.Second
		if schedule.StartAt.After(after) {
			return schedule.StartAt.UTC(), nil
		}
		elapsed := after.Sub(schedule.StartAt)
		steps := elapsed/interval + 1
		return schedule.StartAt.Add(time.Duration(steps) * interval).UTC(), nil
	case domain.ScheduleKindCron:
		return nextCronOccurrence(schedule.Expression, schedule.Timezone, after)
	default:
		return time.Time{}, fmt.Errorf("%w: unsupported schedule kind %q", domain.ErrInvalidArgument, schedule.Kind)
	}
}

func jsonValid(value string) bool {
	return json.Valid([]byte(value))
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", domain.ErrInvalidArgument)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
