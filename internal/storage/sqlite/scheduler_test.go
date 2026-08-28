package sqlite

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func schedulerTestSchedule(now time.Time) domain.Schedule {
	return domain.Schedule{
		ID: "schedule-1", Name: "Test schedule", Kind: domain.ScheduleKindInterval,
		Timezone: "UTC", StartAt: now.Add(-time.Minute), IntervalSeconds: 3600,
		PayloadJSON: `{"task":"test"}`, Status: domain.ScheduleStatusActive, Enabled: true,
		MisfirePolicy: domain.MisfireRunOnce, NextRunAt: now.Add(-time.Second),
		Retry:        domain.RetryPolicy{MaxAttempts: 3, InitialBackoffSecond: 2, MaxBackoffSecond: 8},
		Budget:       domain.JobBudget{MaxDurationSeconds: 60, MaxTokens: 1000, MaxToolCalls: 4},
		HistoryLimit: 100, CreatedAt: now.Add(-time.Hour), UpdatedAt: now, Version: 1,
	}
}

func TestSchedulerRepositoryCRUDAndOptimisticVersion(t *testing.T) {
	database, ctx := testDatabase(t)
	repository := NewSchedulerRepository(database)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	schedule := schedulerTestSchedule(now)
	if err := repository.CreateSchedule(ctx, schedule); err != nil {
		t.Fatal(err)
	}
	found, err := repository.GetSchedule(ctx, schedule.ID)
	if err != nil || found.ID != schedule.ID || found.IntervalSeconds != 3600 {
		t.Fatalf("get schedule = %#v, %v", found, err)
	}
	found.Name = "updated"
	found.Version++
	found.UpdatedAt = now.Add(time.Second)
	if err := repository.SaveSchedule(ctx, found); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveSchedule(ctx, found); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale save = %v, want conflict", err)
	}
	if err := repository.DeleteSchedule(ctx, schedule.ID, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	deleted, err := repository.GetSchedule(ctx, schedule.ID)
	if err != nil || deleted.Status != domain.ScheduleStatusDeleted || deleted.Enabled || !deleted.NextRunAt.IsZero() {
		t.Fatalf("deleted schedule = %#v, %v", deleted, err)
	}
}

func TestSchedulerRepositoryClaimAndNoOverlap(t *testing.T) {
	database, ctx := testDatabase(t)
	repository := NewSchedulerRepository(database)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	schedule := schedulerTestSchedule(now)
	if err := repository.CreateSchedule(ctx, schedule); err != nil {
		t.Fatal(err)
	}
	claim, err := repository.ClaimScheduled(ctx, domain.ScheduledClaim{
		ScheduleID: schedule.ID, ExpectedVersion: 1, ScheduledFor: schedule.NextRunAt,
		NextRunAt: now.Add(time.Hour - time.Second), Now: now, WorkerID: "worker-a", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Run.State != domain.JobRunRunning || claim.Run.Attempt != 1 || claim.Run.LeaseOwner != "worker-a" {
		t.Fatalf("claim = %#v", claim.Run)
	}
	updated, err := repository.GetSchedule(ctx, schedule.ID)
	if err != nil || updated.Version != 2 || !updated.NextRunAt.Equal(now.Add(time.Hour-time.Second)) {
		t.Fatalf("advanced schedule = %#v, %v", updated, err)
	}
	if _, err := repository.EnqueueManualRun(ctx, domain.ManualRunRequest{ScheduleID: schedule.ID, Now: now, WorkerID: "worker-b", LeaseDuration: time.Minute}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("manual overlap error = %v, want conflict", err)
	}
	if err := repository.CompleteRun(ctx, domain.CompleteRunRequest{RunID: claim.Run.ID, WorkerID: "worker-a", LeaseToken: claim.Run.LeaseToken, Now: now.Add(time.Second), ResultRef: "result:1"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteRun(ctx, domain.CompleteRunRequest{RunID: claim.Run.ID, WorkerID: "worker-a", LeaseToken: claim.Run.LeaseToken, Now: now.Add(2 * time.Second)}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale completion = %v, want conflict", err)
	}
}

func TestSchedulerRepositoryConcurrentClaimUsesCompareAndSet(t *testing.T) {
	database, ctx := testDatabase(t)
	repository := NewSchedulerRepository(database)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	schedule := schedulerTestSchedule(now)
	schedule.ID = "concurrent-schedule"
	if err := repository.CreateSchedule(ctx, schedule); err != nil {
		t.Fatal(err)
	}
	claims := make(chan error, 2)
	var group sync.WaitGroup
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func(worker string) {
			defer group.Done()
			_, err := repository.ClaimScheduled(context.Background(), domain.ScheduledClaim{
				ScheduleID: schedule.ID, ExpectedVersion: 1, ScheduledFor: schedule.NextRunAt,
				NextRunAt: now.Add(time.Hour), Now: now, WorkerID: worker, LeaseDuration: time.Minute,
			})
			claims <- err
		}(string(rune('a' + i)))
	}
	group.Wait()
	close(claims)
	var success, conflicts int
	for err := range claims {
		if err == nil {
			success++
		} else if errors.Is(err, domain.ErrConflict) {
			conflicts++
		} else {
			t.Fatalf("concurrent claim error = %v", err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("concurrent claim outcomes: success=%d conflicts=%d", success, conflicts)
	}
}

func TestSchedulerRepositoryRetryBackoffAndBoundedHistory(t *testing.T) {
	database, ctx := testDatabase(t)
	repository := NewSchedulerRepository(database)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	schedule := schedulerTestSchedule(now)
	schedule.ID = "retry-schedule"
	schedule.HistoryLimit = 2
	schedule.Retry = domain.RetryPolicy{MaxAttempts: 2, InitialBackoffSecond: 5, MaxBackoffSecond: 5}
	if err := repository.CreateSchedule(ctx, schedule); err != nil {
		t.Fatal(err)
	}
	claim, err := repository.ClaimScheduled(ctx, domain.ScheduledClaim{
		ScheduleID: schedule.ID, ExpectedVersion: 1, ScheduledFor: schedule.NextRunAt,
		NextRunAt: now.Add(time.Hour), Now: now, WorkerID: "worker-a", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.FailRun(ctx, domain.FailRunRequest{RunID: claim.Run.ID, WorkerID: "worker-a", LeaseToken: claim.Run.LeaseToken, Now: now, Reason: "temporary"}); err != nil {
		t.Fatal(err)
	}
	retry, err := repository.GetJobRun(ctx, claim.Run.ID)
	if err != nil || retry.State != domain.JobRunQueued || !retry.RetryAt.Equal(now.Add(5*time.Second)) || retry.Attempt != 1 {
		t.Fatalf("retry row = %#v, %v", retry, err)
	}
	reclaimed, err := repository.ClaimRetry(ctx, domain.RetryClaim{RunID: retry.ID, Now: now.Add(5 * time.Second), WorkerID: "worker-a", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.Run.Attempt != 2 {
		t.Fatalf("reclaimed attempt = %d", reclaimed.Run.Attempt)
	}
	if err := repository.FailRun(ctx, domain.FailRunRequest{RunID: retry.ID, WorkerID: "worker-a", LeaseToken: reclaimed.Run.LeaseToken, Now: now.Add(5 * time.Second), Reason: "permanent"}); err != nil {
		t.Fatal(err)
	}
	if final, err := repository.GetJobRun(ctx, retry.ID); err != nil || final.State != domain.JobRunFailed {
		t.Fatalf("final retry row = %#v, %v", final, err)
	}
	// Manual terminal runs are pruned once the configured history bound is
	// exceeded; the currently running/queued row is never pruned.
	for i := 0; i < 4; i++ {
		manual, err := repository.EnqueueManualRun(ctx, domain.ManualRunRequest{ScheduleID: schedule.ID, Now: now.Add(time.Duration(i+1) * time.Minute), WorkerID: "worker-a", LeaseDuration: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		job, err := repository.ClaimRetry(ctx, domain.RetryClaim{RunID: manual.ID, Now: now.Add(time.Duration(i+1) * time.Minute), WorkerID: "worker-a", LeaseDuration: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.CompleteRun(ctx, domain.CompleteRunRequest{RunID: manual.ID, WorkerID: "worker-a", LeaseToken: job.Run.LeaseToken, Now: now.Add(time.Duration(i+1)*time.Minute + time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := repository.ListJobRuns(ctx, schedule.ID, domain.JobRunListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) > schedule.HistoryLimit {
		t.Fatalf("job history length = %d, want <= %d", len(runs), schedule.HistoryLimit)
	}
	allRuns, err := repository.ListJobRuns(ctx, "", domain.JobRunListOptions{Limit: 100})
	if err != nil || len(allRuns) < len(runs) {
		t.Fatalf("global job history = %d, %v", len(allRuns), err)
	}
}

func TestSchedulerRepositoryMisfireAndLeaseRecovery(t *testing.T) {
	database, ctx := testDatabase(t)
	repository := NewSchedulerRepository(database)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	schedule := schedulerTestSchedule(now)
	schedule.ID = "misfire-schedule"
	if err := repository.CreateSchedule(ctx, schedule); err != nil {
		t.Fatal(err)
	}
	skipped, err := repository.RecordMisfire(ctx, domain.MisfireRecord{
		ScheduleID: schedule.ID, ExpectedVersion: 1, ScheduledFor: schedule.NextRunAt,
		NextRunAt: now.Add(time.Hour), Now: now, Reason: "offline",
	})
	if err != nil || skipped.State != domain.JobRunSkipped {
		t.Fatalf("misfire = %#v, %v", skipped, err)
	}
	paused, err := repository.GetSchedule(ctx, schedule.ID)
	if err != nil || paused.Version != 2 || !paused.NextRunAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("after misfire schedule = %#v, %v", paused, err)
	}
	paused.Status = domain.ScheduleStatusActive
	paused.Enabled = true
	paused.NextRunAt = now.Add(-2 * time.Second)
	paused.Version++
	paused.UpdatedAt = now.Add(time.Second)
	if err := repository.SaveSchedule(ctx, paused); err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.ClaimScheduled(ctx, domain.ScheduledClaim{
		ScheduleID: schedule.ID, ExpectedVersion: 3, ScheduledFor: paused.NextRunAt,
		NextRunAt: now.Add(2 * time.Hour), Now: now, WorkerID: "worker-a", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := repository.RecoverExpiredLeases(ctx, now.Add(2*time.Minute))
	if err != nil || recovered != 1 {
		t.Fatalf("recovered = %d, %v", recovered, err)
	}
	retry, err := repository.GetJobRun(ctx, claimed.Run.ID)
	if err != nil || retry.State != domain.JobRunQueued || retry.LeaseToken != "" || retry.RetryAt.IsZero() {
		t.Fatalf("recovered run = %#v, %v", retry, err)
	}
	reclaimed, err := repository.ClaimRetry(ctx, domain.RetryClaim{RunID: retry.ID, Now: now.Add(2 * time.Minute), WorkerID: "worker-b", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.Run.ExecutionKey != claimed.Run.ExecutionKey || reclaimed.Run.Attempt != 2 {
		t.Fatalf("recovered claim = %#v, original = %#v", reclaimed.Run, claimed.Run)
	}
	if err := repository.CompleteRun(ctx, domain.CompleteRunRequest{RunID: claimed.Run.ID, WorkerID: "worker-a", LeaseToken: claimed.Run.LeaseToken, Now: now.Add(2 * time.Minute)}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale worker completion = %v, want conflict", err)
	}
}

func TestSchedulerRepositoryHonoursContextCancellation(t *testing.T) {
	database, _ := testDatabase(t)
	repository := NewSchedulerRepository(database)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repository.ListSchedules(ctx, domain.ScheduleListOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled list = %v, want context canceled", err)
	}
}
