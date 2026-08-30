package scheduler

import (
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// referenceCronScan is the exhaustive per-minute scan the search used to
// perform. It is the oracle for the skipping search: it is obviously correct
// (it inspects every wall-clock minute in order) and far too slow to ship.
func referenceCronScan(spec cronSpec, location *time.Location, after time.Time) (time.Time, bool) {
	candidate := after.In(location).Truncate(time.Minute).Add(time.Minute)
	for index := 0; index < cronSearchMinutes; index++ {
		if spec.matchesTime(candidate.In(location)) {
			return candidate.UTC(), true
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, false
}

// TestCronSearchMatchesExhaustiveScan is the correctness proof for M-32: the
// day/month skipping search must return exactly what the per-minute scan
// returned, including across DST transitions in both hemispheres, in a
// half-hour-offset zone, and in a zone whose fall-back lands on a date
// boundary.
func TestCronSearchMatchesExhaustiveScan(t *testing.T) {
	zones := []string{
		"UTC",
		"Europe/Moscow",
		"America/New_York",
		"America/Santiago",
		"America/Sao_Paulo",
		"Australia/Lord_Howe",
		"Australia/Sydney",
		"Asia/Kolkata",
		"Pacific/Apia",
	}
	// Anchors sit next to northern and southern DST transitions so the chained
	// walk crosses spring-forward and fall-back windows minute by minute.
	anchors := []string{"2025-03-08T00:00:00Z", "2025-10-31T00:00:00Z"}
	expressions := []struct {
		expression string
		chain      int
	}{
		{"*/7 * * * *", 120},
		{"0 3 * * *", 24},
		{"30 2 * * *", 24},
		{"0 0 * * SUN", 6},
		{"15 9 * * MON-FRI", 12},
		{"*/20 8-10 * * *", 40},
		{"59 23 * * *", 12},
		{"0 1-3 * * *", 40},
		{"23 0,12 * * 1,5", 8},
	}
	for _, zone := range zones {
		location := mustLocation(t, zone)
		for _, anchor := range anchors {
			start, err := time.Parse(time.RFC3339, anchor)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range expressions {
				spec, err := parseCron(item.expression)
				if err != nil {
					t.Fatalf("parse %q: %v", item.expression, err)
				}
				cursor := start
				for step := 0; step < item.chain; step++ {
					want, ok := referenceCronScan(spec, location, cursor)
					got, _, err := nextCronOccurrenceSteps(item.expression, zone, cursor)
					if !ok {
						if err == nil {
							t.Fatalf("%s %s: search found %s where the scan found nothing", zone, item.expression, got)
						}
						break
					}
					if err != nil {
						t.Fatalf("%s %s after %s: search failed with %v, scan found %s", zone, item.expression, cursor, err, want)
					}
					if !got.Equal(want) {
						t.Fatalf("%s %s after %s: search = %s, exhaustive scan = %s",
							zone, item.expression, cursor.In(location), got.In(location), want.In(location))
					}
					cursor = got
				}
			}
		}
	}
}

// TestCronSearchMatchesExhaustiveScanForRareExpressions covers the expensive
// expressions separately, with short chains, because the oracle has to walk
// millions of minutes for each answer.
func TestCronSearchMatchesExhaustiveScanForRareExpressions(t *testing.T) {
	cases := []struct {
		expression string
		zone       string
		after      string
		chain      int
	}{
		{"0 0 1 * *", "UTC", "2025-03-08T00:00:00Z", 6},
		{"0 0 1 * *", "America/New_York", "2025-10-31T00:00:00Z", 6},
		{"0 0 31 * *", "America/New_York", "2025-03-08T00:00:00Z", 5},
		{"0 0 29 2 *", "UTC", "2024-03-01T00:00:00Z", 1},
		{"0 0 29 2 *", "America/New_York", "2026-08-29T00:00:00Z", 1},
	}
	for _, item := range cases {
		location := mustLocation(t, item.zone)
		spec, err := parseCron(item.expression)
		if err != nil {
			t.Fatalf("parse %q: %v", item.expression, err)
		}
		cursor, err := time.Parse(time.RFC3339, item.after)
		if err != nil {
			t.Fatal(err)
		}
		for step := 0; step < item.chain; step++ {
			want, ok := referenceCronScan(spec, location, cursor)
			if !ok {
				t.Fatalf("%s %s: oracle found no occurrence", item.zone, item.expression)
			}
			got, _, err := nextCronOccurrenceSteps(item.expression, item.zone, cursor)
			if err != nil {
				t.Fatalf("%s %s after %s: %v", item.zone, item.expression, cursor, err)
			}
			if !got.Equal(want) {
				t.Fatalf("%s %s after %s: search = %s, exhaustive scan = %s",
					item.zone, item.expression, cursor, got, want)
			}
			cursor = got
		}
	}
}

// TestCronSearchDoesNotScanEveryMinute is the M-32 regression guard. The
// counts are iteration counts, not durations, so the test cannot pass merely
// because the machine was fast. Before the fix "0 0 29 2 *" cost 2_102_400
// iterations from this reference instant.
func TestCronSearchDoesNotScanEveryMinute(t *testing.T) {
	cases := []struct {
		expression string
		zone       string
		after      string
		maxSteps   int
	}{
		{"0 0 29 2 *", "UTC", "2024-03-01T00:00:00Z", 4000},
		{"0 0 29 2 *", "America/New_York", "2024-03-01T00:00:00Z", 4000},
		{"0 0 1 1 *", "UTC", "2026-01-02T00:00:00Z", 2000},
		{"0 0 31 12 *", "Europe/Moscow", "2026-01-01T00:00:00Z", 2000},
		{"15 9 * * MON-FRI", "Europe/Moscow", "2026-08-28T10:00:00Z", 2000},
	}
	for _, item := range cases {
		after, err := time.Parse(time.RFC3339, item.after)
		if err != nil {
			t.Fatal(err)
		}
		got, steps, err := nextCronOccurrenceSteps(item.expression, item.zone, after)
		if err != nil {
			t.Fatalf("%s in %s: %v", item.expression, item.zone, err)
		}
		if steps > item.maxSteps {
			t.Fatalf("%s in %s took %d iterations (limit %d), result %s",
				item.expression, item.zone, steps, item.maxSteps, got)
		}
	}
}

// TestCronRejectsUnreachableDayOfMonth pins the parse-time half of M-32.
// "0 0 30 2 *" parses field by field but can never fire, and used to be
// discovered only by scanning the full ten-year window (5_270_400 iterations)
// inside a synchronous CreateSchedule call.
func TestCronRejectsUnreachableDayOfMonth(t *testing.T) {
	for _, expression := range []string{"0 0 30 2 *", "0 0 31 2 *", "0 0 31 4 *", "0 0 31 4,6,9,11 *"} {
		spec, err := parseCron(expression)
		if err == nil {
			t.Fatalf("parseCron(%q) unexpectedly succeeded as %#v", expression, spec)
		}
		if !strings.Contains(err.Error(), "day of month") {
			t.Fatalf("parseCron(%q) error = %v, want day-of-month context", expression, err)
		}
	}
	// These stay valid. February 29 occurs in every ten-year window, March 30
	// exists, and a restricted day-of-week is ORed with day-of-month under
	// POSIX semantics, so an impossible day-of-month is still satisfiable.
	for _, expression := range []string{"0 0 29 2 *", "0 0 30 2,3 *", "0 0 30 2 MON", "0 0 31 * *"} {
		if _, err := parseCron(expression); err != nil {
			t.Fatalf("parseCron(%q) = %v, want success", expression, err)
		}
	}
}

// TestCronUnreachableExpressionIsRejectedBeforeSchedulingWork checks that the
// rejection reaches schedule validation, which is where a user-facing
// CreateSchedule call used to burn the full window.
func TestCronUnreachableExpressionIsRejectedBeforeSchedulingWork(t *testing.T) {
	schedule := Schedule{
		ID: "unreachable", Name: "Unreachable", Kind: "cron", Expression: "0 0 30 2 *",
		Timezone: "UTC", PayloadJSON: "{}", Status: "active", Enabled: true,
		MisfirePolicy: "run_once", HistoryLimit: 10, Version: 1,
		Retry:     domain.RetryPolicy{MaxAttempts: 2, InitialBackoffSecond: 1, MaxBackoffSecond: 2},
		StartAt:   time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC),
		CreatedAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC),
		NextRunAt: time.Date(2026, 8, 28, 13, 0, 0, 0, time.UTC),
	}
	err := ValidateSchedule(schedule)
	if err == nil || !strings.Contains(err.Error(), "day of month") {
		t.Fatalf("ValidateSchedule = %v, want an unreachable day-of-month rejection", err)
	}
}
