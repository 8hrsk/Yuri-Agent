package scheduler

import (
	"strings"
	"testing"
	"time"
)

func TestCronNextOccurrenceSupportsListsRangesStepsAndNames(t *testing.T) {
	location, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	after := time.Date(2026, time.August, 28, 10, 0, 0, 0, location)
	got, err := nextCronOccurrence("15 9 * * MON-FRI", "Europe/Moscow", after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, time.August, 31, 9, 15, 0, 0, location)
	if !got.Equal(want) {
		t.Fatalf("next cron = %s, want %s", got, want)
	}

	got, err = nextCronOccurrence("*/20 8-10 * JAN,MAR MON,WED", "Europe/Moscow", time.Date(2026, 1, 1, 10, 1, 0, 0, location))
	if err != nil {
		t.Fatal(err)
	}
	want = time.Date(2026, 1, 5, 8, 0, 0, 0, location)
	if !got.Equal(want) {
		t.Fatalf("named/step cron = %s, want %s", got, want)
	}
}

func TestCronDayOfMonthAndWeekdayUsePOSIXOrSemantics(t *testing.T) {
	// 7 September 2026 is Monday but not an even day. With DOM=*/2 and
	// DOW=MON, POSIX semantics still match because the restricted fields are
	// ORed. This catches accidentally treating */2 as a wildcard.
	got, err := nextCronOccurrence("0 9 */2 * MON", "Europe/Moscow", time.Date(2026, 9, 6, 9, 1, 0, 0, time.FixedZone("MSK", 3*60*60)))
	if err != nil {
		t.Fatal(err)
	}
	if got.In(time.FixedZone("MSK", 3*60*60)).Day() != 7 || got.In(time.FixedZone("MSK", 3*60*60)).Hour() != 9 {
		t.Fatalf("OR semantics returned %s", got)
	}
}

func TestCronSkipsNonexistentSpringForwardMinute(t *testing.T) {
	got, err := nextCronOccurrence("30 2 * * *", "America/New_York", time.Date(2025, 3, 9, 5, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	local := got.In(mustLocation(t, "America/New_York"))
	if local.Year() != 2025 || local.Month() != time.March || local.Day() != 10 || local.Hour() != 2 || local.Minute() != 30 {
		t.Fatalf("spring-forward cron = %s (%s), want 2025-03-10 02:30 local", got, local)
	}
}

func TestCronRejectsNonFiveFieldAndQuartzSyntax(t *testing.T) {
	for _, expression := range []string{"* * * *", "@hourly", "0 0 ? * *", "60 * * * *", "1/2 * * * *"} {
		if _, err := parseCron(expression); err == nil {
			t.Fatalf("parseCron(%q) unexpectedly succeeded", expression)
		}
	}
}

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func TestCronErrorIncludesFieldContext(t *testing.T) {
	_, err := parseCron(strings.TrimSpace("bad * * * *"))
	if err == nil || !strings.Contains(err.Error(), "minute") {
		t.Fatalf("parse error = %v, want minute context", err)
	}
}
