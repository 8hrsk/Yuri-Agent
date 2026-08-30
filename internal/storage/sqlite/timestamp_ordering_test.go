package sqlite

import (
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// M-3: timestamps are stored as TEXT and compared lexicographically by SQLite.
// A variable-width fractional part makes the byte order disagree with the time
// order, so a schedule whose next_run_at lands on a whole second stays invisible
// to `next_run_at <= ?` for the remainder of that second.
func TestListDueSchedulesSeesWholeSecondBoundaryImmediately(t *testing.T) {
	database, ctx := testDatabase(t)
	repository := NewSchedulerRepository(database)
	due := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	schedule := schedulerTestSchedule(due)
	schedule.NextRunAt = due
	if err := repository.CreateSchedule(ctx, schedule); err != nil {
		t.Fatal(err)
	}
	offsets := []time.Duration{
		0,
		1 * time.Nanosecond,
		1 * time.Microsecond,
		1 * time.Millisecond,
		100 * time.Millisecond,
		500 * time.Millisecond,
		999 * time.Millisecond,
		999999999 * time.Nanosecond,
		time.Second,
	}
	for _, offset := range offsets {
		now := due.Add(offset)
		found, err := repository.ListDueSchedules(ctx, now, 10)
		if err != nil {
			t.Fatalf("offset %v: %v", offset, err)
		}
		if len(found) != 1 {
			t.Errorf("offset %v (now=%s): due schedules = %d, want 1", offset, now.Format(time.RFC3339Nano), len(found))
		}
	}
}

// M-3: the same encoding breaks `ORDER BY created_at` whenever one timestamp's
// fractional part is a byte-prefix of another's.
func TestScheduleListingOrdersPrefixFractions(t *testing.T) {
	database, ctx := testDatabase(t)
	repository := NewSchedulerRepository(database)
	base := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	// Chronological order; every pair is a lexicographic trap under RFC3339Nano:
	// "" < ".5" < ".55" in time, but 'Z' > '.' and ".5Z" > ".55Z" as bytes.
	stamps := []time.Time{
		base,
		base.Add(500 * time.Millisecond),
		base.Add(550 * time.Millisecond),
		base.Add(555 * time.Millisecond),
		base.Add(time.Second),
	}
	want := make([]domain.ID, 0, len(stamps))
	for index, stamp := range stamps {
		schedule := schedulerTestSchedule(base)
		// IDs run in reverse so a correct result cannot come from the id tiebreak.
		schedule.ID = domain.ID(string(rune('e'-index)) + "-schedule")
		schedule.CreatedAt = stamp
		if err := repository.CreateSchedule(ctx, schedule); err != nil {
			t.Fatal(err)
		}
		want = append(want, schedule.ID)
	}
	listed, err := repository.ListSchedules(ctx, domain.ScheduleListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != len(want) {
		t.Fatalf("listed %d schedules, want %d", len(listed), len(want))
	}
	got := make([]domain.ID, 0, len(listed))
	for _, item := range listed {
		got = append(got, item.ID)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("ORDER BY created_at ASC = %v, want %v", got, want)
		}
	}
}
