package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// M-3, end to end. A cron schedule's next_run_at always lands on a whole
// second, while the clock the worker polls with carries sub-second precision.
// Under the old variable-width timestamp encoding "…T12:00:00Z" compared as
// GREATER than "…T12:00:00.000000001Z", so RunDue found nothing for the entire
// remainder of the second in which the schedule became due.
//
// The clock is injected and stepped explicitly; nothing here observes elapsed
// wall-clock time.
func TestRunDueFiresAtEveryPointOfTheDueSecond(t *testing.T) {
	due := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	offsets := []time.Duration{
		0,
		1 * time.Nanosecond,
		1 * time.Microsecond,
		250 * time.Millisecond,
		500 * time.Millisecond,
		999999999 * time.Nanosecond,
	}
	for _, offset := range offsets {
		t.Run(offset.String(), func(t *testing.T) {
			clock := &advancingClock{now: due.Add(-time.Hour)}
			executed := 0
			worker, _ := newTestScheduler(t, clock, ExecuteFunc(func(context.Context, ScheduledJob) error {
				executed++
				return nil
			}))
			schedule := serviceSchedule(due, domain.ScheduleKindCron, domain.MisfireRunOnce)
			schedule.Expression = "0 12 * * *"
			schedule.IntervalSeconds = 0
			schedule.StartAt = due.Add(-24 * time.Hour)
			schedule.NextRunAt = due
			if err := worker.CreateSchedule(context.Background(), schedule); err != nil {
				t.Fatal(err)
			}
			stored, err := worker.GetSchedule(context.Background(), schedule.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !stored.NextRunAt.Equal(due) {
				t.Fatalf("next_run_at = %s, want %s", stored.NextRunAt.Format(time.RFC3339Nano), due.Format(time.RFC3339Nano))
			}
			// One nanosecond before the boundary nothing may fire.
			clock.Set(due.Add(-time.Nanosecond))
			early, err := worker.RunDue(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if early.Claimed != 0 {
				t.Fatalf("claimed %d before the due instant, want 0", early.Claimed)
			}
			clock.Set(due.Add(offset))
			result, err := worker.RunDue(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if result.Claimed != 1 || result.Completed != 1 || executed != 1 {
				t.Fatalf("at due+%v: result = %#v, executed = %d; want one claimed and completed run",
					offset, result, executed)
			}
		})
	}
}

// The same encoding governs lease recovery: a lease whose lease_until lands on
// a whole second stayed unexpired for the rest of that second.
func TestRecoverLeasesSeesWholeSecondExpiryImmediately(t *testing.T) {
	start := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clock := &advancingClock{now: start}
	blocked := make(chan struct{})
	worker, repository := newTestScheduler(t, clock, ExecuteFunc(func(ctx context.Context, _ ScheduledJob) error {
		<-blocked
		return nil
	}))
	t.Cleanup(func() { close(blocked) })
	schedule := serviceSchedule(start, domain.ScheduleKindOnce, domain.MisfireRunOnce)
	schedule.IntervalSeconds = 0
	schedule.StartAt = start.Add(-time.Second)
	schedule.NextRunAt = start.Add(-time.Second)
	if err := worker.CreateSchedule(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	// Claim directly so the run stays running with a lease that expires on a
	// whole second; LeaseDuration is one minute in the test worker.
	claim, err := repository.ClaimScheduled(context.Background(), domain.ScheduledClaim{
		ScheduleID: schedule.ID, ExpectedVersion: 1, ScheduledFor: schedule.NextRunAt,
		WorkerID: "other-worker", LeaseDuration: time.Minute,
		Now: start, NextRunAt: time.Time{},
	})
	if err != nil {
		t.Fatal(err)
	}
	expiry := claim.Run.LeaseUntil
	if expiry.Nanosecond() != 0 {
		t.Fatalf("lease_until = %s, expected a whole second for this test", expiry.Format(time.RFC3339Nano))
	}
	// Just before expiry the lease is still held.
	recovered, err := repository.RecoverExpiredLeases(context.Background(), expiry.Add(-time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 0 {
		t.Fatalf("recovered %d leases before expiry, want 0", recovered)
	}
	// One nanosecond past expiry it must be recoverable, not a second later.
	recovered, err = repository.RecoverExpiredLeases(context.Background(), expiry.Add(time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("recovered %d leases one nanosecond past expiry, want 1", recovered)
	}
}
