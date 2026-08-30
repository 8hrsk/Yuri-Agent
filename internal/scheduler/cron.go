package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronSpec is a deliberately small, dependency-free implementation of the
// standard five-field cron grammar. It supports lists, ranges, steps, '*',
// and the conventional English month/day names. Seconds, @macros, and
// non-standard Quartz '?' syntax are rejected instead of being guessed.
type cronSpec struct {
	minute fieldSet
	hour   fieldSet
	dom    fieldSet
	month  fieldSet
	dow    fieldSet
}

type fieldSet struct {
	values map[int]struct{}
	any    bool
}

var monthNames = map[string]int{
	"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6,
	"JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
}

var weekdayNames = map[string]int{
	"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6,
}

func parseCron(expression string) (cronSpec, error) {
	parts := strings.Fields(strings.TrimSpace(expression))
	if len(parts) != 5 {
		return cronSpec{}, fmt.Errorf("cron expression must contain exactly five fields")
	}
	minute, err := parseCronField(parts[0], 0, 59, nil, false)
	if err != nil {
		return cronSpec{}, fmt.Errorf("minute: %w", err)
	}
	hour, err := parseCronField(parts[1], 0, 23, nil, false)
	if err != nil {
		return cronSpec{}, fmt.Errorf("hour: %w", err)
	}
	dom, err := parseCronField(parts[2], 1, 31, nil, false)
	if err != nil {
		return cronSpec{}, fmt.Errorf("day of month: %w", err)
	}
	month, err := parseCronField(parts[3], 1, 12, monthNames, false)
	if err != nil {
		return cronSpec{}, fmt.Errorf("month: %w", err)
	}
	dow, err := parseCronField(parts[4], 0, 7, weekdayNames, true)
	if err != nil {
		return cronSpec{}, fmt.Errorf("day of week: %w", err)
	}
	spec := cronSpec{minute: minute, hour: hour, dom: dom, month: month, dow: dow}
	if err := spec.checkDayReachable(); err != nil {
		return cronSpec{}, err
	}
	return spec, nil
}

// maxDaysInMonth is the largest day number a month can carry, counting the
// leap day for February. Every ten-year search window contains a leap year, so
// a February 29 expression is reachable and must not be rejected.
func maxDaysInMonth(month int) int {
	switch month {
	case 2:
		return 29
	case 4, 6, 9, 11:
		return 30
	default:
		return 31
	}
}

// checkDayReachable rejects day/month combinations that can never occur, such
// as "0 0 30 2 *". They parse field by field but have no occurrence at all, so
// without this check the caller only learns about them from the ten-year
// search bound, after a full scan. The check only applies when day-of-week is
// an unrestricted '*': with both day fields restricted POSIX ORs them, so an
// impossible day-of-month is still satisfiable through the weekday.
func (spec cronSpec) checkDayReachable() error {
	if !spec.dow.any {
		return nil
	}
	for month := range spec.month.values {
		limit := maxDaysInMonth(month)
		for day := range spec.dom.values {
			if day <= limit {
				return nil
			}
		}
	}
	return fmt.Errorf("day of month: selected days never occur in the selected months")
}

func parseCronField(raw string, minimum, maximum int, names map[string]int, sundaySeven bool) (fieldSet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "?" {
		return fieldSet{}, fmt.Errorf("empty or unsupported field")
	}
	result := fieldSet{values: make(map[int]struct{})}
	items := strings.Split(raw, ",")
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			return fieldSet{}, fmt.Errorf("empty list item")
		}
		base := item
		step := 1
		if strings.Count(item, "/") > 1 {
			return fieldSet{}, fmt.Errorf("multiple step separators in %q", item)
		}
		if slash := strings.IndexByte(item, '/'); slash >= 0 {
			base = item[:slash]
			parsed, err := strconv.Atoi(item[slash+1:])
			if err != nil || parsed <= 0 {
				return fieldSet{}, fmt.Errorf("invalid step in %q", item)
			}
			step = parsed
		}
		if base == "*" {
			// In POSIX cron, DOM/DOW are wildcard only when the field is
			// syntactically '*'. A stepped wildcard such as '*/2' is a
			// restricted field and must participate in the DOM/DOW OR rule.
			if item == "*" && len(items) == 1 {
				result.any = true
			}
			for value := minimum; value <= maximum; value += step {
				result.values[normalizeCronValue(value, minimum, maximum, sundaySeven)] = struct{}{}
			}
			continue
		}
		if strings.Contains(base, "-") {
			if strings.Count(base, "-") != 1 {
				return fieldSet{}, fmt.Errorf("invalid range %q", base)
			}
			bounds := strings.SplitN(base, "-", 2)
			from, err := parseCronValue(bounds[0], names)
			if err != nil {
				return fieldSet{}, err
			}
			to, err := parseCronValue(bounds[1], names)
			if err != nil {
				return fieldSet{}, err
			}
			if from > to {
				return fieldSet{}, fmt.Errorf("range start exceeds end in %q", base)
			}
			if from < minimum || to > maximum {
				return fieldSet{}, fmt.Errorf("range %q is outside %d-%d", base, minimum, maximum)
			}
			for value := from; value <= to; value += step {
				result.values[normalizeCronValue(value, minimum, maximum, sundaySeven)] = struct{}{}
			}
			continue
		}
		if strings.Contains(base, "*") {
			return fieldSet{}, fmt.Errorf("invalid wildcard %q", base)
		}
		value, err := parseCronValue(base, names)
		if err != nil {
			return fieldSet{}, err
		}
		if value < minimum || value > maximum {
			return fieldSet{}, fmt.Errorf("value %q is outside %d-%d", base, minimum, maximum)
		}
		if strings.Contains(item, "/") {
			// A single value followed by a step is not part of the portable
			// five-field grammar (e.g. 5/10), so reject it explicitly.
			return fieldSet{}, fmt.Errorf("step requires '*' or a range in %q", item)
		}
		result.values[normalizeCronValue(value, minimum, maximum, sundaySeven)] = struct{}{}
	}
	if len(result.values) == 0 {
		return fieldSet{}, fmt.Errorf("field selects no values")
	}
	return result, nil
}

func parseCronValue(raw string, names map[string]int) (int, error) {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	if names != nil {
		if value, ok := names[raw]; ok {
			return value, nil
		}
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", raw)
	}
	return value, nil
}

func normalizeCronValue(value, minimum, maximum int, sundaySeven bool) int {
	if sundaySeven && maximum == 7 && value == 7 {
		return 0
	}
	return value
}

func (f fieldSet) matches(value int) bool {
	_, ok := f.values[value]
	return ok
}

// matchesDay reports whether the calendar day of value is selected. POSIX cron
// uses OR semantics when both day-of-month and day-of-week are restricted; if
// either is '*', the restricted field alone applies.
func (spec cronSpec) matchesDay(value time.Time) bool {
	domMatches := spec.dom.matches(value.Day())
	dowMatches := spec.dow.matches(int(value.Weekday()))
	switch {
	case spec.dom.any && spec.dow.any:
		return true
	case spec.dom.any:
		return dowMatches
	case spec.dow.any:
		return domMatches
	default:
		return domMatches || dowMatches
	}
}

func (spec cronSpec) matchesTime(value time.Time) bool {
	if !spec.minute.matches(value.Minute()) || !spec.hour.matches(value.Hour()) || !spec.month.matches(int(value.Month())) {
		return false
	}
	return spec.matchesDay(value)
}

// cronSearchMinutes is the ten-year search window, expressed as the number of
// one-minute candidates it contains. It turns malformed or unreachable
// combinations into a deterministic error instead of an infinite worker loop.
const cronSearchMinutes = 10 * 366 * 24 * 60

// nextCronOccurrence returns the first matching wall-clock minute strictly
// after after.
func nextCronOccurrence(expression, timezone string, after time.Time) (time.Time, error) {
	occurrence, _, err := nextCronOccurrenceSteps(expression, timezone, after)
	return occurrence, err
}

// nextCronOccurrenceSteps is nextCronOccurrence plus the number of loop
// iterations the search needed. Tests assert on that count so a regression
// back to a per-minute scan of the whole window is caught deterministically
// rather than by timing the wall clock.
//
// The search still walks the absolute timeline one wall-clock minute at a
// time inside a selected day, which is what preserves both occurrences of a
// fall-back DST transition and skips nonexistent spring-forward minutes.
// What it no longer does is visit every minute of a day or month that cannot
// contain a match: those are skipped in a single step to the first instant of
// the next day or month. A scan of "0 0 29 2 *" costs 2.1 million iterations
// without the skips and under three thousand with them.
func nextCronOccurrenceSteps(expression, timezone string, after time.Time) (time.Time, int, error) {
	spec, err := parseCron(expression)
	if err != nil {
		return time.Time{}, 0, err
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("load timezone %q: %w", timezone, err)
	}
	if after.IsZero() {
		return time.Time{}, 0, fmt.Errorf("cron reference time is required")
	}
	candidate := after.In(location).Truncate(time.Minute).Add(time.Minute)
	// The bound is the same instant the per-minute scan used to stop at, so
	// the reachable set is identical to the previous implementation's.
	limit := candidate.Add(cronSearchMinutes * time.Minute)
	steps := 0
	for candidate.Before(limit) {
		steps++
		local := candidate.In(location)
		if !spec.month.matches(int(local.Month())) {
			// A jump that failed to advance would loop forever, so fall back
			// to the one-minute step, which always advances.
			if jumped := startOfNextLocalMonth(local, location); jumped.After(candidate) {
				candidate = jumped
			} else {
				candidate = candidate.Add(time.Minute)
			}
			continue
		}
		if !spec.matchesDay(local) {
			if jumped := startOfNextLocalDay(local, location); jumped.After(candidate) {
				candidate = jumped
			} else {
				candidate = candidate.Add(time.Minute)
			}
			continue
		}
		if spec.hour.matches(local.Hour()) && spec.minute.matches(local.Minute()) {
			return candidate.UTC(), steps, nil
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, steps, fmt.Errorf("cron expression has no occurrence within ten years")
}

// startOfNextLocalDay returns the first instant of the calendar day after
// local's, in location. time.Date normalizes a midnight that does not exist
// (a spring-forward transition landing on midnight) forward to the first
// instant that does, and resolves an ambiguous midnight to its earlier
// occurrence, so the result is never later than the first real minute of that
// day and the skip cannot step over a match.
func startOfNextLocalDay(local time.Time, location *time.Location) time.Time {
	return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, location)
}

// startOfNextLocalMonth is the month-level equivalent of startOfNextLocalDay.
func startOfNextLocalMonth(local time.Time, location *time.Location) time.Time {
	return time.Date(local.Year(), local.Month()+1, 1, 0, 0, 0, 0, location)
}
