package timeparse

import (
	"strings"
	"testing"
	"time"
)

// Fixed "now" used across tests so durations are deterministic.
// 2026-05-16 (Saturday) 14:30:00 UTC.
var fixedNow = time.Date(2026, 5, 16, 14, 30, 0, 0, time.UTC)

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	got, err := Parse(s, fixedNow)
	if err != nil {
		t.Fatalf("Parse(%q) error: %v", s, err)
	}
	return got
}

func TestParse_InDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"in 30 minutes": 30 * time.Minute,
		"in 1 minute":   1 * time.Minute,
		"in 2 hours":    2 * time.Hour,
		"in 3 days":     3 * 24 * time.Hour,
		"in 30m":        30 * time.Minute,
		"in 2h":         2 * time.Hour,
		"in 3d":         3 * 24 * time.Hour,
		"in 45 seconds": 45 * time.Second,
		"In 30 MINUTES": 30 * time.Minute, // case-insensitive
	}
	for input, want := range cases {
		got := mustParse(t, input)
		expected := fixedNow.Add(want).UTC()
		if !got.Equal(expected) {
			t.Errorf("Parse(%q) = %v, want %v", input, got, expected)
		}
	}
}

func TestParse_Tomorrow(t *testing.T) {
	// 2026-05-16 is Saturday → tomorrow = Sunday 2026-05-17
	got := mustParse(t, "tomorrow at 9am")
	want := time.Date(2026, 5, 17, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	got = mustParse(t, "tomorrow at 14:30")
	want = time.Date(2026, 5, 17, 14, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	got = mustParse(t, "tomorrow")
	want = time.Date(2026, 5, 17, 9, 0, 0, 0, time.UTC) // default 9am
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParse_Weekday(t *testing.T) {
	// 2026-05-16 is Saturday. "Monday at 9am" → Mon 2026-05-18 09:00
	got := mustParse(t, "monday at 9am")
	want := time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("monday: got %v, want %v", got, want)
	}
	// "sunday at 9am" → next Sunday is 2026-05-17 (tomorrow)
	got = mustParse(t, "sunday at 9am")
	want = time.Date(2026, 5, 17, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("sunday: got %v, want %v", got, want)
	}
	// Saturday on Saturday → next week's Saturday
	got = mustParse(t, "saturday at 9am")
	want = time.Date(2026, 5, 23, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("saturday: got %v, want %v", got, want)
	}
	// "fri" alias
	got = mustParse(t, "fri at 17:00")
	want = time.Date(2026, 5, 22, 17, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("fri: got %v, want %v", got, want)
	}
}

func TestParse_TodayAt(t *testing.T) {
	// fixedNow = 14:30. "at 4pm" → 16:00 today.
	got := mustParse(t, "at 4pm")
	want := time.Date(2026, 5, 16, 16, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("today 4pm: got %v, want %v", got, want)
	}
	// "at 9am" → already passed; should roll to tomorrow.
	got = mustParse(t, "at 9am")
	want = time.Date(2026, 5, 17, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("9am rollover: got %v, want %v", got, want)
	}
}

func TestParse_RFC3339Passthrough(t *testing.T) {
	got := mustParse(t, "2026-12-25T09:00:00Z")
	want := time.Date(2026, 12, 25, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("RFC3339: got %v, want %v", got, want)
	}
}

func TestParse_RejectsUnknown(t *testing.T) {
	cases := []string{
		"",
		"sometime soon",
		"in a bit",
		"around 9-ish",
		"in -1 minutes",
		"in 0 minutes",
		"at 25:00",
	}
	for _, in := range cases {
		_, err := Parse(in, fixedNow)
		if err == nil {
			t.Errorf("Parse(%q) should have errored", in)
		}
	}
}

func TestParse_ErrorMessageMentionsExamples(t *testing.T) {
	_, err := Parse("when the cows come home", fixedNow)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, hint := range []string{"30 minutes", "tomorrow", "Monday"} {
		if !strings.Contains(strings.ToLower(msg), strings.ToLower(hint)) {
			t.Errorf("error message should hint at %q, got: %s", hint, msg)
		}
	}
}

func TestTo24Hour(t *testing.T) {
	for _, tc := range []struct {
		h        int
		meridiem string
		want     int
	}{
		{12, "am", 0},
		{1, "am", 1},
		{11, "am", 11},
		{12, "pm", 12},
		{1, "pm", 13},
		{11, "pm", 23},
		{14, "", 14},
		{0, "", 0},
		{13, "pm", 13},
	} {
		if got := to24Hour(tc.h, tc.meridiem); got != tc.want {
			t.Errorf("to24Hour(%d, %q) = %d, want %d", tc.h, tc.meridiem, got, tc.want)
		}
	}
}
