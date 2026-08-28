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
	return cronSpec{minute: minute, hour: hour, dom: dom, month: month, dow: dow}, nil
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

func (spec cronSpec) matchesTime(value time.Time) bool {
	month := int(value.Month())
	dom := value.Day()
	dow := int(value.Weekday())
	if !spec.minute.matches(value.Minute()) || !spec.hour.matches(value.Hour()) || !spec.month.matches(month) {
		return false
	}
	// POSIX cron uses OR semantics when both day-of-month and day-of-week
	// are restricted; if either is '*', the restricted field alone applies.
	domMatches := spec.dom.matches(dom)
	dowMatches := spec.dow.matches(dow)
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

// nextCronOccurrence returns the first matching wall-clock minute strictly
// after after. Iterating absolute minutes preserves both occurrences during a
// fall-back DST transition and naturally skips nonexistent spring-forward
// minutes. The ten-year bound turns malformed/unreachable combinations into a
// deterministic error instead of an infinite worker loop.
func nextCronOccurrence(expression, timezone string, after time.Time) (time.Time, error) {
	spec, err := parseCron(expression)
	if err != nil {
		return time.Time{}, err
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("load timezone %q: %w", timezone, err)
	}
	if after.IsZero() {
		return time.Time{}, fmt.Errorf("cron reference time is required")
	}
	candidate := after.In(location).Truncate(time.Minute).Add(time.Minute)
	const maxMinutes = 10 * 366 * 24 * 60
	for index := 0; index < maxMinutes; index++ {
		if spec.matchesTime(candidate.In(location)) {
			return candidate.UTC(), nil
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("cron expression has no occurrence within ten years")
}
