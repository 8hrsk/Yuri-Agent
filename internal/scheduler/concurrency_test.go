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

// dueOneShot builds a one-shot schedule that is already due at now.
func dueOneShot(now time.Time, id domain.ID) Schedule {
	schedule := serviceSchedule(now, domain.ScheduleKindOnce, domain.MisfireRunOnce)
	schedule.ID = id
	schedule.Name = string(id)
	schedule.StartAt = now
	schedule.NextRunAt = now
	schedule.IntervalSeconds = 0
	return schedule
}

// TestSchedulerRunDueOverlapsIndependentJobs pins M-33: a single long-running
// job must not hold back every other due schedule. Both executors have to be
// inside Execute at the same time, so the assertion is on an observed event
// (two entries before either release) and never on elapsed wall-clock time.
func TestSchedulerRunDueOverlapsIndependentJobs(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clock := &advancingClock{now: now}
	entered := make(chan domain.ID, 4)
	release := make(chan struct{})
	worker, _ := newTestScheduler(t, clock, ExecuteFunc(func(ctx context.Context, job ScheduledJob) error {
		entered <- job.Schedule.ID
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	for _, id := range []domain.ID{"overlap-a", "overlap-b"} {
		if err := worker.CreateSchedule(context.Background(), dueOneShot(now, id)); err != nil {
			t.Fatal(err)
		}
	}

	cycle := make(chan RunDueResult, 1)
	cycleErr := make(chan error, 1)
	go func() {
		result, err := worker.RunDue(context.Background())
		cycleErr <- err
		cycle <- result
	}()

	seen := map[domain.ID]bool{}
	for len(seen) < 2 {
		select {
		case id := <-entered:
			seen[id] = true
		case <-time.After(10 * time.Second):
			close(release)
			<-cycleErr
			<-cycle
			t.Fatalf("only %d of 2 due jobs entered the executor while the first was still running", len(seen))
		}
	}
	close(release)
	if err := <-cycleErr; err != nil {
		t.Fatalf("run due = %v", err)
	}
	result := <-cycle
	if result.Claimed != 2 || result.Completed != 2 || result.Failed != 0 {
		t.Fatalf("run due result = %#v, want 2 claimed and 2 completed", result)
	}
}

// TestSchedulerRunDueBoundsConcurrency proves the overlap is a bounded pool
// rather than an unbounded goroutine fan-out: with a limit of two, the third
// due job must not start until one of the first two returns.
func TestSchedulerRunDueBoundsConcurrency(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clock := &advancingClock{now: now}
	database, err := sqlite.Open(context.Background(), t.TempDir()+"/yuri.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repository := sqlite.NewSchedulerRepository(database)

	var mu sync.Mutex
	inFlight, peak := 0, 0
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	worker, err := New(repository, ExecuteFunc(func(ctx context.Context, job ScheduledJob) error {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
		entered <- struct{}{}
		defer func() {
			mu.Lock()
			inFlight--
			mu.Unlock()
		}()
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}), Options{
		Clock: clock, WorkerID: "worker-pool", LeaseDuration: time.Minute,
		PollInterval: time.Hour, MaxClaimsPerCycle: 8, MaxConcurrentRuns: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.ID{"pool-a", "pool-b", "pool-c"} {
		if err := worker.CreateSchedule(context.Background(), dueOneShot(now, id)); err != nil {
			t.Fatal(err)
		}
	}

	cycle := make(chan RunDueResult, 1)
	go func() {
		result, _ := worker.RunDue(context.Background())
		cycle <- result
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			close(release)
			<-cycle
			t.Fatal("pool did not start the first two jobs")
		}
	}
	// The third job must still be queued behind the pool limit.
	select {
	case <-entered:
		close(release)
		<-cycle
		t.Fatal("a third job started while the concurrency limit was two")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	result := <-cycle
	if result.Claimed != 3 || result.Completed != 3 {
		t.Fatalf("bounded pool result = %#v, want 3 claimed and 3 completed", result)
	}
	mu.Lock()
	observed := peak
	mu.Unlock()
	if observed != 2 {
		t.Fatalf("peak concurrency = %d, want 2", observed)
	}
}

// TestSchedulerAppliesDefaultRunDeadline pins the second half of M-33: a job
// whose schedule carries no MaxDurationSeconds budget must still receive a
// bounded context instead of being able to pin the worker forever. The
// assertion waits for the cancellation event, so a slow machine cannot make it
// flake; only a missing deadline can.
func TestSchedulerAppliesDefaultRunDeadline(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clock := &advancingClock{now: now}
	database, err := sqlite.Open(context.Background(), t.TempDir()+"/yuri.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repository := sqlite.NewSchedulerRepository(database)

	observed := make(chan error, 1)
	worker, err := New(repository, ExecuteFunc(func(ctx context.Context, job ScheduledJob) error {
		<-ctx.Done()
		observed <- ctx.Err()
		return ctx.Err()
	}), Options{
		Clock: clock, WorkerID: "worker-deadline", LeaseDuration: time.Minute,
		PollInterval: time.Hour, MaxClaimsPerCycle: 8, MaxRunDuration: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	schedule := dueOneShot(now, "no-budget")
	schedule.Budget = domain.JobBudget{}
	if err := worker.CreateSchedule(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = worker.RunDue(context.Background())
	}()
	select {
	case err := <-observed:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("execution context error = %v, want deadline exceeded", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a job without MaxDurationSeconds never received a deadline")
	}
	<-done
}
