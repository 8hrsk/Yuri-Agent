package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

// --- M-5 regression harness -------------------------------------------------
//
// prunePprobe instruments the SQLite driver so the pruning tests can observe
// two things the repository API alone cannot show: how many DELETE statements
// a prune issues, and what happens when row iteration fails half-way through.

var errPruneProbeIteration = errors.New("injected job history iteration failure")

type pruneProbe struct {
	deletes atomic.Int64
	// failRowsAfter, when positive, makes every SELECT over job_runs return
	// that many rows and then fail, the way a truncated or interrupted read
	// does in production.
	failRowsAfter atomic.Int64
	injecting     atomic.Bool
	// failQuery narrows injection to prepared statements whose text contains
	// this substring. An unset value means "FROM job_runs", the table the
	// pruning tests care about.
	failQuery atomic.Value
}

// injectAfter arms the probe to cut every matching SELECT short after rows
// rows, for the duration of run.
func (p *pruneProbe) injectAfter(rows int64, querySubstring string, run func()) {
	p.failRowsAfter.Store(rows)
	p.failQuery.Store(querySubstring)
	p.injecting.Store(true)
	defer p.injecting.Store(false)
	run()
}

var activePruneProbe atomic.Pointer[pruneProbe]

type pruneProbeDriver struct{ inner driver.Driver }

func (d *pruneProbeDriver) Open(name string) (driver.Conn, error) {
	connection, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &pruneProbeConn{inner: connection}, nil
}

type pruneProbeConn struct{ inner driver.Conn }

func (c *pruneProbeConn) Prepare(query string) (driver.Stmt, error) {
	statement, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &pruneProbeStmt{inner: statement, query: query}, nil
}

func (c *pruneProbeConn) Close() error { return c.inner.Close() }

func (c *pruneProbeConn) Begin() (driver.Tx, error) { return c.inner.Begin() } //nolint:staticcheck // driver.Conn requires it

type pruneProbeStmt struct {
	inner driver.Stmt
	query string
}

func (s *pruneProbeStmt) Close() error  { return s.inner.Close() }
func (s *pruneProbeStmt) NumInput() int { return s.inner.NumInput() }

func (s *pruneProbeStmt) Exec(args []driver.Value) (driver.Result, error) { //nolint:staticcheck // driver.Stmt requires it
	if probe := activePruneProbe.Load(); probe != nil && strings.Contains(s.query, "DELETE FROM job_runs") {
		probe.deletes.Add(1)
	}
	return s.inner.Exec(args)
}

func (s *pruneProbeStmt) Query(args []driver.Value) (driver.Rows, error) { //nolint:staticcheck // driver.Stmt requires it
	rows, err := s.inner.Query(args)
	if err != nil {
		return nil, err
	}
	probe := activePruneProbe.Load()
	if probe == nil || !probe.injecting.Load() {
		return rows, nil
	}
	match, _ := probe.failQuery.Load().(string)
	if match == "" {
		match = "FROM job_runs"
	}
	if !strings.Contains(s.query, match) {
		return rows, nil
	}
	return &pruneProbeRows{inner: rows, remaining: probe.failRowsAfter.Load()}, nil
}

type pruneProbeRows struct {
	inner     driver.Rows
	remaining int64
}

func (r *pruneProbeRows) Columns() []string { return r.inner.Columns() }
func (r *pruneProbeRows) Close() error      { return r.inner.Close() }

func (r *pruneProbeRows) Next(dest []driver.Value) error {
	if r.remaining <= 0 {
		return errPruneProbeIteration
	}
	r.remaining--
	return r.inner.Next(dest)
}

var pruneProbeDriverOnce sync.Once

// pruneProbeDatabase opens a fully migrated database on the instrumented
// driver and installs probe as the active one for the duration of the test.
func pruneProbeDatabase(t *testing.T, probe *pruneProbe) (*sql.DB, context.Context) {
	t.Helper()
	pruneProbeDriverOnce.Do(func() {
		seed, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		inner := seed.Driver()
		_ = seed.Close()
		sql.Register("sqlite-prune-probe", &pruneProbeDriver{inner: inner})
	})
	path := filepath.Join(t.TempDir(), "prune.sqlite3")
	database, err := sql.Open("sqlite-prune-probe", sqliteFileDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if err := Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	activePruneProbe.Store(probe)
	t.Cleanup(func() { activePruneProbe.Store(nil) })
	return database, ctx
}

// seedTerminalJobRuns writes count succeeded runs plus one still-queued run,
// oldest first, and returns the terminal ids newest first.
func seedTerminalJobRuns(t *testing.T, database *sql.DB, ctx context.Context, scheduleID domain.ID, count int) []string {
	t.Helper()
	base := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	newestFirst := make([]string, 0, count)
	for index := 0; index < count; index++ {
		at := base.Add(time.Duration(index) * time.Minute).Format(time.RFC3339Nano)
		id := fmt.Sprintf("run-%03d", index)
		if _, err := database.ExecContext(ctx, `
			INSERT INTO job_runs(id, schedule_id, state, trigger, attempt, execution_key,
				scheduled_for, finished_at, version, created_at, updated_at)
			VALUES (?, ?, ?, ?, 1, ?, ?, ?, 1, ?, ?)`,
			id, string(scheduleID), string(domain.JobRunSucceeded), string(domain.JobTriggerScheduled),
			id+":key", at, at, at, at); err != nil {
			t.Fatal(err)
		}
		newestFirst = append([]string{id}, newestFirst...)
	}
	open := base.Add(time.Duration(count) * time.Minute).Format(time.RFC3339Nano)
	if _, err := database.ExecContext(ctx, `
		INSERT INTO job_runs(id, schedule_id, state, trigger, attempt, execution_key,
			scheduled_for, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?, 1, ?, ?)`,
		"run-open", string(scheduleID), string(domain.JobRunQueued), string(domain.JobTriggerScheduled),
		"run-open:key", open, open, open); err != nil {
		t.Fatal(err)
	}
	return newestFirst
}

func remainingJobRunIDs(t *testing.T, database *sql.DB, ctx context.Context, scheduleID domain.ID) []string {
	t.Helper()
	rows, err := database.QueryContext(ctx,
		`SELECT id FROM job_runs WHERE schedule_id = ? ORDER BY created_at DESC, id DESC`, string(scheduleID))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func seedPruneSchedule(t *testing.T, database *sql.DB, ctx context.Context, historyLimit int) domain.Schedule {
	t.Helper()
	now := time.Date(2026, 8, 29, 7, 0, 0, 0, time.UTC)
	schedule := schedulerTestSchedule(now)
	schedule.HistoryLimit = historyLimit
	if err := NewSchedulerRepository(database).CreateSchedule(ctx, schedule); err != nil {
		t.Fatal(err)
	}
	return schedule
}

// TestPruneJobHistoryDoesNotSwallowIterationErrors is the M-5 regression test.
// The old implementation read every terminal id into a Go slice and checked
// only rows.Close() afterwards, so an iteration cut short by an error looked
// like a short history: prune returned nil and quietly deleted nothing. The
// prune must either report the failure or complete correctly - never claim
// success while leaving the history over its limit.
func TestPruneJobHistoryDoesNotSwallowIterationErrors(t *testing.T) {
	probe := &pruneProbe{}
	probe.failRowsAfter.Store(1)
	database, ctx := pruneProbeDatabase(t, probe)
	schedule := seedPruneSchedule(t, database, ctx, 2)
	seedTerminalJobRuns(t, database, ctx, schedule.ID, 6)

	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	probe.injecting.Store(true)
	pruneErr := pruneJobHistoryTx(ctx, transaction, schedule.ID, schedule.HistoryLimit)
	probe.injecting.Store(false)
	if pruneErr != nil {
		// Surfacing the failure is a correct outcome: the caller's transaction
		// rolls back and nothing is silently under-deleted.
		_ = transaction.Rollback()
		return
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("prune reported success but its transaction failed: %v", err)
	}
	remaining := remainingJobRunIDs(t, database, ctx, schedule.ID)
	// One queued run is never terminal, so it always survives.
	if len(remaining) != schedule.HistoryLimit+1 {
		t.Fatalf("prune reported success but left %d runs (%v), want %d",
			len(remaining), remaining, schedule.HistoryLimit+1)
	}
}

// TestPruneJobHistoryDeletesInOneStatement covers the other half of M-5: the
// prune ran on every claim, completion, failure and cancellation and issued
// one DELETE per excess row.
func TestPruneJobHistoryDeletesInOneStatement(t *testing.T) {
	probe := &pruneProbe{}
	database, ctx := pruneProbeDatabase(t, probe)
	schedule := seedPruneSchedule(t, database, ctx, 2)
	seedTerminalJobRuns(t, database, ctx, schedule.ID, 12)

	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	probe.deletes.Store(0)
	if err := pruneJobHistoryTx(ctx, transaction, schedule.ID, schedule.HistoryLimit); err != nil {
		t.Fatal(err)
	}
	deletes := probe.deletes.Load()
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if deletes != 1 {
		t.Fatalf("prune issued %d DELETE statements, want 1", deletes)
	}
	if remaining := remainingJobRunIDs(t, database, ctx, schedule.ID); len(remaining) != 3 {
		t.Fatalf("remaining runs = %v, want the 2 newest terminal runs plus the queued one", remaining)
	}
}

// TestPruneJobHistoryRetainsExactlyTheNewestRuns pins the retention contract
// itself so the rewrite cannot change which rows survive.
func TestPruneJobHistoryRetainsExactlyTheNewestRuns(t *testing.T) {
	database, ctx := testDatabase(t)
	schedule := seedPruneSchedule(t, database, ctx, 3)
	newestFirst := seedTerminalJobRuns(t, database, ctx, schedule.ID, 9)

	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := pruneJobHistoryTx(ctx, transaction, schedule.ID, schedule.HistoryLimit); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	want := append(append([]string{}, "run-open"), newestFirst[:schedule.HistoryLimit]...)
	got := remainingJobRunIDs(t, database, ctx, schedule.ID)
	if len(got) != len(want) {
		t.Fatalf("remaining runs = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("remaining runs = %v, want %v", got, want)
		}
	}

	// A second prune of an already-trimmed history is a no-op, and a
	// non-positive limit is still rejected.
	second, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := pruneJobHistoryTx(ctx, second, schedule.ID, schedule.HistoryLimit); err != nil {
		t.Fatal(err)
	}
	if err := second.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := remainingJobRunIDs(t, database, ctx, schedule.ID); len(got) != len(want) {
		t.Fatalf("second prune changed the history: %v", got)
	}
	third, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = third.Rollback() }()
	if err := pruneJobHistoryTx(ctx, third, schedule.ID, 0); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("prune with a zero limit = %v, want invalid argument", err)
	}
}

// seedListableSchedules writes count active, already-due schedules and returns
// the instant they are all due at.
func seedListableSchedules(t *testing.T, database *sql.DB, ctx context.Context, count int) time.Time {
	t.Helper()
	now := time.Date(2026, 8, 29, 7, 0, 0, 0, time.UTC)
	repository := NewSchedulerRepository(database)
	for index := 0; index < count; index++ {
		schedule := schedulerTestSchedule(now)
		schedule.ID = domain.ID(fmt.Sprintf("schedule-%03d", index))
		if err := repository.CreateSchedule(ctx, schedule); err != nil {
			t.Fatal(err)
		}
	}
	return now
}

// seedRetryableRuns writes count queued, scheduled runs whose retry_at has
// already passed, which is what ListRetryableRuns selects.
func seedRetryableRuns(t *testing.T, database *sql.DB, ctx context.Context, scheduleID domain.ID, now time.Time, count int) {
	t.Helper()
	for index := 0; index < count; index++ {
		at := now.Add(time.Duration(-index-1) * time.Minute).Format(time.RFC3339Nano)
		id := fmt.Sprintf("retry-%03d", index)
		if _, err := database.ExecContext(ctx, `
			INSERT INTO job_runs(id, schedule_id, state, trigger, attempt, execution_key,
				scheduled_for, retry_at, version, created_at, updated_at)
			VALUES (?, ?, ?, ?, 1, ?, ?, ?, 1, ?, ?)`,
			id, string(scheduleID), string(domain.JobRunQueued), string(domain.JobTriggerScheduled),
			id+":key", at, at, at, at); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSchedulerListsSurfaceIterationErrors covers the four rows.Next() loops
// that remain in scheduler.go after the prune rewrite. M-5 was reported only
// against pruneJobHistoryTx, but a missing rows.Err() in any of these would be
// worse than in the prune: a listing whose iteration fails half-way returns a
// short slice with a nil error, so schedules silently vanish from the listing
// and their jobs simply never fire, with no error logged anywhere.
//
// Each case cuts iteration off after a single row and requires the repository
// to report that failure instead of returning a truncated result.
func TestSchedulerListsSurfaceIterationErrors(t *testing.T) {
	const seeded = 5
	cases := []struct {
		name  string
		table string
		seed  func(t *testing.T, database *sql.DB, ctx context.Context) time.Time
		list  func(ctx context.Context, repository *SchedulerRepository, now time.Time) (int, error)
	}{
		{
			name:  "ListSchedules",
			table: "FROM schedules",
			seed: func(t *testing.T, database *sql.DB, ctx context.Context) time.Time {
				return seedListableSchedules(t, database, ctx, seeded)
			},
			list: func(ctx context.Context, repository *SchedulerRepository, _ time.Time) (int, error) {
				result, err := repository.ListSchedules(ctx, domain.ScheduleListOptions{})
				return len(result), err
			},
		},
		{
			name:  "ListDueSchedules",
			table: "FROM schedules",
			seed: func(t *testing.T, database *sql.DB, ctx context.Context) time.Time {
				return seedListableSchedules(t, database, ctx, seeded)
			},
			list: func(ctx context.Context, repository *SchedulerRepository, now time.Time) (int, error) {
				result, err := repository.ListDueSchedules(ctx, now, 0)
				return len(result), err
			},
		},
		{
			name:  "ListJobRuns",
			table: "FROM job_runs",
			seed: func(t *testing.T, database *sql.DB, ctx context.Context) time.Time {
				now := seedListableSchedules(t, database, ctx, 1)
				seedTerminalJobRuns(t, database, ctx, "schedule-000", seeded)
				return now
			},
			list: func(ctx context.Context, repository *SchedulerRepository, _ time.Time) (int, error) {
				result, err := repository.ListJobRuns(ctx, "schedule-000", domain.JobRunListOptions{})
				return len(result), err
			},
		},
		{
			name:  "ListRetryableRuns",
			table: "FROM job_runs",
			seed: func(t *testing.T, database *sql.DB, ctx context.Context) time.Time {
				now := seedListableSchedules(t, database, ctx, 1)
				seedRetryableRuns(t, database, ctx, "schedule-000", now, seeded)
				return now
			},
			list: func(ctx context.Context, repository *SchedulerRepository, now time.Time) (int, error) {
				result, err := repository.ListRetryableRuns(ctx, now, 0)
				return len(result), err
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			probe := &pruneProbe{}
			database, ctx := pruneProbeDatabase(t, probe)
			now := testCase.seed(t, database, ctx)
			repository := NewSchedulerRepository(database)

			// Sanity: without injection the listing really does return more
			// than the one row the injected failure will allow through, so a
			// swallowed error would be observable as a short slice.
			full, err := testCase.list(ctx, repository, now)
			if err != nil {
				t.Fatalf("baseline list: %v", err)
			}
			if full < 2 {
				t.Fatalf("baseline list returned %d rows, need at least 2 for truncation to be visible", full)
			}

			var count int
			var listErr error
			probe.injectAfter(1, testCase.table, func() {
				count, listErr = testCase.list(ctx, repository, now)
			})
			if listErr == nil {
				t.Fatalf("list reported success with %d of %d rows; a mid-iteration failure was swallowed", count, full)
			}
			if !errors.Is(listErr, errPruneProbeIteration) {
				t.Fatalf("list error = %v, want it to wrap %v", listErr, errPruneProbeIteration)
			}
		})
	}
}
