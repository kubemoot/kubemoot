// Package timeparse turns a small set of natural-language time
// expressions into absolute time.Time values. It is intentionally strict:
// inputs that don't match a known pattern return an error so the calling
// LLM can re-prompt with a clearer phrasing instead of silently guessing.
//
// Supported forms (case-insensitive, whitespace tolerant):
//
//	"in 30 minutes" / "in 1 minute" / "in 2 hours" / "in 3 days"
//	"in 30m" / "in 2h" / "in 3d"
//	"tomorrow at 9am" / "tomorrow at 14:30"
//	"<weekday> at 9am"                          (next occurrence)
//	"at 9am" / "at 14:30"                       (today if not yet passed, else tomorrow)
//	"<RFC3339 timestamp>"                        (escape hatch for ISO inputs)
package timeparse

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Parse interprets s relative to now. Returns the absolute trigger time
// in UTC, or an error describing why parsing failed.
func Parse(s string, now time.Time) (time.Time, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return time.Time{}, errors.New("empty time expression")
	}

	// RFC 3339 escape hatch — try BEFORE lowercasing because the format
	// requires uppercase 'T' and 'Z'.
	if t, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return t.UTC(), nil
	}

	// All other patterns are case-insensitive.
	s = strings.ToLower(trimmed)

	if t, ok, err := parseInDuration(s, now); ok {
		return t, err
	}
	if t, ok, err := parseTomorrow(s, now); ok {
		return t, err
	}
	if t, ok, err := parseWeekday(s, now); ok {
		return t, err
	}
	if t, ok, err := parseTodayAt(s, now); ok {
		return t, err
	}

	return time.Time{}, fmt.Errorf(
		"unrecognized time expression %q — try 'in 30 minutes', 'tomorrow at 9am', 'Monday at 9am', or an RFC 3339 timestamp",
		s)
}

// ─── "in N <unit>" ──────────────────────────────────────────────────────

var inDurationRE = regexp.MustCompile(
	`^in\s+(\d+)\s*(s|sec|secs|second|seconds|m|min|mins|minute|minutes|h|hr|hrs|hour|hours|d|day|days)$`)

func parseInDuration(s string, now time.Time) (time.Time, bool, error) {
	m := inDurationRE.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false, nil
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return time.Time{}, true, fmt.Errorf("duration must be a positive integer: %q", m[1])
	}
	var d time.Duration
	switch m[2] {
	case "s", "sec", "secs", "second", "seconds":
		d = time.Duration(n) * time.Second
	case "m", "min", "mins", "minute", "minutes":
		d = time.Duration(n) * time.Minute
	case "h", "hr", "hrs", "hour", "hours":
		d = time.Duration(n) * time.Hour
	case "d", "day", "days":
		d = time.Duration(n) * 24 * time.Hour
	}
	return now.Add(d).UTC(), true, nil
}

// ─── "tomorrow at HH" ───────────────────────────────────────────────────

var tomorrowRE = regexp.MustCompile(`^tomorrow(?:\s+at\s+(.+))?$`)

func parseTomorrow(s string, now time.Time) (time.Time, bool, error) {
	m := tomorrowRE.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false, nil
	}
	base := now.AddDate(0, 0, 1)
	if m[1] == "" {
		// "tomorrow" alone → 09:00 by convention; record the choice in the error
		// path? No, we treat it as 09:00 silently — better than rejecting.
		return atClock(base, 9, 0).UTC(), true, nil
	}
	h, mn, err := parseClock(m[1])
	if err != nil {
		return time.Time{}, true, err
	}
	return atClock(base, h, mn).UTC(), true, nil
}

// ─── "<weekday> at HH" ──────────────────────────────────────────────────

var weekdayRE = regexp.MustCompile(
	`^(monday|mon|tuesday|tue|tues|wednesday|wed|thursday|thu|thur|thurs|` +
		`friday|fri|saturday|sat|sunday|sun)(?:\s+at\s+(.+))?$`)

var weekdayMap = map[string]time.Weekday{
	"sunday": time.Sunday, "sun": time.Sunday,
	"monday": time.Monday, "mon": time.Monday,
	"tuesday": time.Tuesday, "tue": time.Tuesday, "tues": time.Tuesday,
	"wednesday": time.Wednesday, "wed": time.Wednesday,
	"thursday": time.Thursday, "thu": time.Thursday, "thur": time.Thursday, "thurs": time.Thursday,
	"friday": time.Friday, "fri": time.Friday,
	"saturday": time.Saturday, "sat": time.Saturday,
}

func parseWeekday(s string, now time.Time) (time.Time, bool, error) {
	m := weekdayRE.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false, nil
	}
	target, ok := weekdayMap[m[1]]
	if !ok {
		return time.Time{}, true, fmt.Errorf("unknown weekday %q", m[1])
	}
	h, mn := 9, 0
	if m[2] != "" {
		var err error
		h, mn, err = parseClock(m[2])
		if err != nil {
			return time.Time{}, true, err
		}
	}
	// Days until the next occurrence (1..7, strictly future).
	delta := int(target - now.Weekday())
	if delta <= 0 {
		delta += 7
	}
	day := now.AddDate(0, 0, delta)
	return atClock(day, h, mn).UTC(), true, nil
}

// ─── "at HH" (today or tomorrow if passed) ──────────────────────────────

var todayAtRE = regexp.MustCompile(`^at\s+(.+)$`)

func parseTodayAt(s string, now time.Time) (time.Time, bool, error) {
	m := todayAtRE.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false, nil
	}
	h, mn, err := parseClock(m[1])
	if err != nil {
		return time.Time{}, true, err
	}
	t := atClock(now, h, mn)
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t.UTC(), true, nil
}

// ─── clock helpers ──────────────────────────────────────────────────────

// parseClock interprets "9am", "9:30am", "14:30", "9", "9pm".
var clockRE = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?\s*(am|pm)?$`)

func parseClock(s string) (int, int, error) {
	m := clockRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, 0, fmt.Errorf("unrecognized clock time %q (try 9am, 14:30, or 9:30pm)", s)
	}
	h, _ := strconv.Atoi(m[1])
	mn := 0
	if m[2] != "" {
		mn, _ = strconv.Atoi(m[2])
	}
	h = to24Hour(h, m[3])
	if h < 0 || h > 23 || mn < 0 || mn > 59 {
		return 0, 0, fmt.Errorf("clock time out of range: %d:%02d", h, mn)
	}
	return h, mn, nil
}

// to24Hour converts hour h with an optional "am" or "pm" suffix to a 24-hour
// hour: 12am is 0, 1pm through 11pm add 12, and no suffix leaves h as written.
func to24Hour(h int, meridiem string) int {
	switch {
	case meridiem == "am" && h == 12:
		return 0
	case meridiem == "pm" && h < 12:
		return h + 12
	}
	return h
}

func atClock(day time.Time, h, mn int) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), h, mn, 0, 0, day.Location())
}
