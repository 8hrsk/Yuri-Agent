package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

type advancingClock struct {
	mu  sync.RWMutex
	now time.Time
}

func (c *advancingClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *advancingClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

func newTestScheduler(t *testing.T, clock *advancingClock, execute ExecuteFunc) (*Scheduler, *sqlite.SchedulerRepository) {
	t.Helper()
	database, err := sqlite.Open(context.Background(), t.TempDir()+"/yuri.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repository := sqlite.NewSchedulerRepository(database)
	worker, err := New(repository, execute, Options{Clock: clock, WorkerID: "worker-test", LeaseDuration: time.Minute, PollInterval: time.Hour, MaxClaimsPerCycle: 8})
	if err != nil {
		t.Fatal(err)
	}
	return worker, repository
}

func serviceSchedule(now time.Time, kind domain.ScheduleKind, policy domain.MisfirePolicy) Schedule {
	return Schedule{
		ID: "service-schedule", Name: "Service schedule", Kind: kind, Timezone: "UTC",
		StartAt: now.Add(-time.Minute), IntervalSeconds: 3600, PayloadJSON: `{"kind":"test"}`,
		Status: domain.ScheduleStatusActive, Enabled: true, MisfirePolicy: policy,
		NextRunAt: now.Add(-time.Second), Retry: domain.RetryPolicy{MaxAttempts: 2, InitialBackoffSecond: 1, MaxBackoffSecond: 2},
		Budget:       domain.JobBudget{MaxDurationSeconds: 10, MaxTokens: 1000, MaxToolCalls: 4},
		HistoryLimit: 20, CreatedAt: now.Add(-time.Hour), UpdatedAt: now, Version: 1,
	}
}

func TestSchedulerRunDueExecutesOneShotAndPersistsCompletion(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clock := &advancingClock{now: now}
	var executed []ScheduledJob
	worker, repository := newTestScheduler(t, clock, ExecuteFunc(func(_ context.Context, job ScheduledJob) error {
		executed = append(executed, job)
		return nil
	}))
	schedule := serviceSchedule(now, domain.ScheduleKindOnce, domain.MisfireRunOnce)
	schedule.StartAt = now.Add(-time.Second)
	schedule.NextRunAt = schedule.StartAt
	schedule.IntervalSeconds = 0
	if err := worker.CreateSchedule(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	result, err := worker.RunDue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Claimed != 1 || result.Completed != 1 || len(executed) != 1 {
		t.Fatalf("run result = %#v, executed = %d", result, len(executed))
	}
	stored, err := worker.GetSchedule(context.Background(), schedule.ID)
	if err != nil || stored.Status != domain.ScheduleStatusCompleted || stored.Enabled || !stored.NextRunAt.IsZero() {
		t.Fatalf("stored one-shot = %#v, %v", stored, err)
	}
	runs, err := worker.ListJobRuns(context.Background(), schedule.ID, domain.JobRunListOptions{})
	if err != nil || len(runs) != 1 || runs[0].State != domain.JobRunSucceeded {
		t.Fatalf("job history = %#v, %v", runs, err)
	}
	if _, err := repository.GetJobRun(context.Background(), runs[0].ID); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerRunDueHonoursMisfireSkipAndRunOnce(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clock := &advancingClock{now: now}
	worker, _ := newTestScheduler(t, clock, ExecuteFunc(func(context.Context, ScheduledJob) error { return nil }))
	skip := serviceSchedule(now, domain.ScheduleKindInterval, domain.MisfireSkip)
	skip.ID = "skip-schedule"
	if err := worker.CreateSchedule(context.Background(), skip); err != nil {
		t.Fatal(err)
	}
	runOnce := serviceSchedule(now, domain.ScheduleKindInterval, domain.MisfireRunOnce)
	runOnce.ID = "run-once-schedule"
	if err := worker.CreateSchedule(context.Background(), runOnce); err != nil {
		t.Fatal(err)
	}
	result, err := worker.RunDue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 1 || result.Claimed != 1 || result.Completed != 1 {
		t.Fatalf("misfire result = %#v", result)
	}
	skipRuns, err := worker.ListJobRuns(context.Background(), skip.ID, domain.JobRunListOptions{})
	if err != nil || len(skipRuns) != 1 || skipRuns[0].State != domain.JobRunSkipped {
		t.Fatalf("skip history = %#v, %v", skipRuns, err)
	}
	if next, err := worker.GetSchedule(context.Background(), skip.ID); err != nil || !next.NextRunAt.After(now) {
		t.Fatalf("skip next run = %#v, %v", next, err)
	}
}

func TestSchedulerCanPersistPastOneShotForMisfireHandling(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clock := &advancingClock{now: now}
	worker, _ := newTestScheduler(t, clock, ExecuteFunc(func(context.Context, ScheduledJob) error { return nil }))
	schedule := serviceSchedule(now, domain.ScheduleKindOnce, domain.MisfireSkip)
	schedule.ID = "past-one-shot"
	schedule.StartAt = now.Add(-time.Minute)
	schedule.NextRunAt = time.Time{}
	schedule.IntervalSeconds = 0
	if err := worker.CreateSchedule(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	result, err := worker.RunDue(context.Background())
	if err != nil || result.Skipped != 1 || result.Claimed != 0 {
		t.Fatalf("past one-shot result = %#v, %v", result, err)
	}
}

func TestSchedulerRetriesFailedExecutionWithBoundedAttempts(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clock := &advancingClock{now: now}
	attempts := 0
	worker, _ := newTestScheduler(t, clock, ExecuteFunc(func(context.Context, ScheduledJob) error {
		attempts++
		if attempts == 1 {
			return errors.New("temporary failure")
		}
		return nil
	}))
	schedule := serviceSchedule(now, domain.ScheduleKindInterval, domain.MisfireRunOnce)
	schedule.NextRunAt = now
	if err := worker.CreateSchedule(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	first, err := worker.RunDue(context.Background())
	if err != nil || first.Failed != 1 || attempts != 1 {
		t.Fatalf("first result = %#v, attempts = %d, err = %v", first, attempts, err)
	}
	runs, err := worker.ListJobRuns(context.Background(), schedule.ID, domain.JobRunListOptions{})
	if err != nil || len(runs) != 1 || runs[0].State != domain.JobRunQueued {
		t.Fatalf("after failure = %#v, %v", runs, err)
	}
	clock.Set(now.Add(time.Second))
	second, err := worker.RunDue(context.Background())
	if err != nil || second.Completed != 1 || attempts != 2 {
		t.Fatalf("retry result = %#v, attempts = %d, err = %v", second, attempts, err)
	}
	runs, err = worker.ListJobRuns(context.Background(), schedule.ID, domain.JobRunListOptions{})
	if err != nil || len(runs) != 1 || runs[0].State != domain.JobRunSucceeded || runs[0].Attempt != 2 {
		t.Fatalf("after retry = %#v, %v", runs, err)
	}
}

func TestSchedulerRetriesFailedOneShotAfterScheduleCompletes(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clock := &advancingClock{now: now}
	attempts := 0
	worker, _ := newTestScheduler(t, clock, ExecuteFunc(func(context.Context, ScheduledJob) error {
		attempts++
		if attempts == 1 {
			return errors.New("temporary one-shot failure")
		}
		return nil
	}))
	schedule := serviceSchedule(now, domain.ScheduleKindOnce, domain.MisfireRunOnce)
	schedule.ID = "one-shot-retry"
	schedule.StartAt = now
	schedule.NextRunAt = now
	schedule.IntervalSeconds = 0
	schedule.Retry = domain.RetryPolicy{MaxAttempts: 2, InitialBackoffSecond: 1, MaxBackoffSecond: 1}
	if err := worker.CreateSchedule(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}

	first, err := worker.RunDue(context.Background())
	if err != nil || first.Claimed != 1 || first.Failed != 1 || attempts != 1 {
		t.Fatalf("first one-shot result = %#v, attempts = %d, err = %v", first, attempts, err)
	}
	completed, err := worker.GetSchedule(context.Background(), schedule.ID)
	if err != nil || completed.Status != domain.ScheduleStatusCompleted || completed.Enabled {
		t.Fatalf("one-shot schedule after failure = %#v, %v", completed, err)
	}
	runs, err := worker.ListJobRuns(context.Background(), schedule.ID, domain.JobRunListOptions{})
	if err != nil || len(runs) != 1 || runs[0].State != domain.JobRunQueued {
		t.Fatalf("queued one-shot retry = %#v, %v", runs, err)
	}

	clock.Set(now.Add(time.Second))
	second, err := worker.RunDue(context.Background())
	if err != nil || second.Claimed != 1 || second.Completed != 1 || attempts != 2 {
		t.Fatalf("one-shot retry result = %#v, attempts = %d, err = %v", second, attempts, err)
	}
	runs, err = worker.ListJobRuns(context.Background(), schedule.ID, domain.JobRunListOptions{})
	if err != nil || len(runs) != 1 || runs[0].State != domain.JobRunSucceeded || runs[0].Attempt != 2 {
		t.Fatalf("completed one-shot retry = %#v, %v", runs, err)
	}
}

func TestSchedulerCancelRunStopsActiveExecutor(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clock := &advancingClock{now: now}
	started := make(chan struct{})
	worker, _ := newTestScheduler(t, clock, ExecuteFunc(func(ctx context.Context, _ ScheduledJob) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}))
	schedule := serviceSchedule(now, domain.ScheduleKindOnce, domain.MisfireRunOnce)
	schedule.ID = "cancel-active"
	schedule.StartAt = now
	schedule.NextRunAt = now
	schedule.IntervalSeconds = 0
	if err := worker.CreateSchedule(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, err := worker.RunDue(context.Background())
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("executor did not start")
	}
	runs, err := worker.ListJobRuns(context.Background(), schedule.ID, domain.JobRunListOptions{})
	if err != nil || len(runs) != 1 || runs[0].State != domain.JobRunRunning {
		t.Fatalf("active runs = %#v, %v", runs, err)
	}
	if err := worker.CancelRun(context.Background(), runs[0].ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled executor did not finish")
	}
	run, err := worker.GetJobRun(context.Background(), runs[0].ID)
	if err != nil || run.State != domain.JobRunCancelled {
		t.Fatalf("cancelled run = %#v, %v", run, err)
	}
}

func TestSchedulerPauseResumeUpdateAndManualRun(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clock := &advancingClock{now: now}
	worker, _ := newTestScheduler(t, clock, ExecuteFunc(func(context.Context, ScheduledJob) error { return nil }))
	schedule := serviceSchedule(now, domain.ScheduleKindCron, domain.MisfireRunOnce)
	schedule.ID = "lifecycle-schedule"
	schedule.Expression = "0 * * * *"
	schedule.IntervalSeconds = 0
	schedule.StartAt = now
	schedule.NextRunAt = now.Add(time.Hour)
	if err := worker.CreateSchedule(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	paused, err := worker.Pause(context.Background(), schedule.ID)
	if err != nil || paused.Status != domain.ScheduleStatusPaused || paused.Enabled {
		t.Fatalf("paused = %#v, %v", paused, err)
	}
	manual, err := worker.ManualRun(context.Background(), schedule.ID)
	if err != nil || manual.Trigger != domain.JobTriggerManual || manual.State != domain.JobRunQueued {
		t.Fatalf("manual = %#v, %v", manual, err)
	}
	result, err := worker.RunDue(context.Background())
	if err != nil || result.Completed != 1 {
		t.Fatalf("manual result = %#v, %v", result, err)
	}
	resumed, err := worker.Resume(context.Background(), schedule.ID)
	if err != nil || resumed.Status != domain.ScheduleStatusActive || !resumed.Enabled {
		t.Fatalf("resumed = %#v, %v", resumed, err)
	}
	resumed.Name = "renamed"
	updated, err := worker.UpdateSchedule(context.Background(), resumed)
	if err != nil || updated.Name != "renamed" || updated.Version != resumed.Version+1 {
		t.Fatalf("updated = %#v, %v", updated, err)
	}
	if _, err := worker.ListJobRuns(context.Background(), "", domain.JobRunListOptions{}); err != nil {
		t.Fatalf("global job history = %v", err)
	}
}

func TestSchedulerResumePastPausedOneShotPreservesMisfire(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clock := &advancingClock{now: now}
	worker, _ := newTestScheduler(t, clock, ExecuteFunc(func(context.Context, ScheduledJob) error { return nil }))
	schedule := serviceSchedule(now, domain.ScheduleKindOnce, domain.MisfireRunOnce)
	schedule.ID = "resume-past-once"
	schedule.StartAt = now.Add(-time.Hour)
	schedule.NextRunAt = schedule.StartAt
	schedule.IntervalSeconds = 0
	schedule.Status = domain.ScheduleStatusPaused
	schedule.Enabled = false
	if err := worker.CreateSchedule(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	resumed, err := worker.Resume(context.Background(), schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.NextRunAt.Equal(schedule.StartAt) || resumed.Status != domain.ScheduleStatusActive {
		t.Fatalf("resumed one-shot = %#v", resumed)
	}
	result, err := worker.RunDue(context.Background())
	if err != nil || result.Completed != 1 {
		t.Fatalf("misfire run = %#v, %v", result, err)
	}
}

func TestSchedulerStartRunsImmediatelyAndStopIsIdempotent(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clock := &advancingClock{now: now}
	started := make(chan struct{}, 1)
	worker, _ := newTestScheduler(t, clock, ExecuteFunc(func(context.Context, ScheduledJob) error {
		started <- struct{}{}
		return nil
	}))
	schedule := serviceSchedule(now, domain.ScheduleKindOnce, domain.MisfireRunOnce)
	schedule.StartAt = now
	schedule.NextRunAt = now
	schedule.IntervalSeconds = 0
	if err := worker.CreateSchedule(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(context.Background()); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("second start = %v, want already started", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not execute immediate cycle")
	}
	if err := worker.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := worker.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerStartRejectsOverlapWhileStopping(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clock := &advancingClock{now: now}
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce sync.Once
	worker, _ := newTestScheduler(t, clock, ExecuteFunc(func(context.Context, ScheduledJob) error {
		enterOnce.Do(func() { close(entered) })
		<-release // deliberately ignores cancellation to hold Stop in stopping
		return nil
	}))
	schedule := serviceSchedule(now, domain.ScheduleKindOnce, domain.MisfireRunOnce)
	schedule.ID = "lifecycle-overlap"
	schedule.StartAt = now
	schedule.NextRunAt = now
	schedule.IntervalSeconds = 0
	if err := worker.CreateSchedule(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not enter executor")
	}

	stopDone := make(chan error, 1)
	go func() { stopDone <- worker.Stop() }()
	deadline := time.Now().Add(2 * time.Second)
	rejected := false
	for time.Now().Before(deadline) {
		err := worker.Start(context.Background())
		if errors.Is(err, ErrAlreadyStarted) {
			rejected = true
			break
		}
		if err != nil {
			close(release)
			<-stopDone
			t.Fatalf("overlapping start error = %v", err)
		}
		// Stop cannot have completed while the executor is blocked. A nil
		// result here would indicate that a second worker overlapped it.
		close(release)
		<-stopDone
		t.Fatal("started a worker while the previous worker was stopping")
	}
	if !rejected {
		close(release)
		if err := <-stopDone; err != nil {
			t.Fatalf("stop after rejected starts = %v", err)
		}
		t.Fatal("Start was not rejected during Stop")
	}
	close(release)
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("start after stop = %v", err)
	}
	if err := worker.Stop(); err != nil {
		t.Fatalf("final stop = %v", err)
	}
}

func TestSchedulerStopBoundsUncooperativeExecutor(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clock := &advancingClock{now: now}
	database, err := sqlite.Open(context.Background(), t.TempDir()+"/yuri.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repository := sqlite.NewSchedulerRepository(database)
	entered := make(chan struct{})
	release := make(chan struct{})
	worker, err := New(repository, ExecuteFunc(func(context.Context, ScheduledJob) error {
		close(entered)
		<-release
		return nil
	}), Options{
		Clock: clock, WorkerID: "worker-timeout", LeaseDuration: time.Minute,
		PollInterval: time.Hour, MaxClaimsPerCycle: 8, StopTimeout: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	schedule := serviceSchedule(now, domain.ScheduleKindOnce, domain.MisfireRunOnce)
	schedule.ID = "stop-timeout"
	schedule.StartAt = now
	schedule.NextRunAt = now
	schedule.IntervalSeconds = 0
	if err := worker.CreateSchedule(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not enter uncooperative executor")
	}
	started := time.Now()
	if err := worker.Stop(); !errors.Is(err, ErrStopTimeout) {
		close(release)
		_ = worker.Stop()
		t.Fatalf("uncooperative stop = %v, want timeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		close(release)
		_ = worker.Stop()
		t.Fatalf("stop took %s despite timeout", elapsed)
	}
	close(release)
	if err := worker.Stop(); err != nil {
		t.Fatalf("join after executor release = %v", err)
	}
}
