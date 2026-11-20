/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package retention

import (
	"strings"
	"testing"
	"time"
)

const day = 24 * time.Hour

func TestParseDurationValid(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"5s":         5 * time.Second,
		"15m":        15 * time.Minute,
		"12h":        12 * time.Hour,
		text90d:      90 * day,
		"2w":         14 * day,
		"1y":         365 * day,
		text1y6w:     365*day + 42*day,
		"1w2d3h4m5s": 7*day + 2*day + 3*time.Hour + 4*time.Minute + 5*time.Second,
		text36h:      36 * time.Hour,
		"100y":       100 * 365 * day,
	} {
		got, err := ParseDuration(in)
		if err != nil {
			t.Errorf("%q: unexpected error %v", in, err)
			continue
		}
		if got.IsNever() || got.Value() != want || !got.IsSet() {
			t.Errorf("%q: got %v (never=%v), want %v", in, got.Value(), got.IsNever(), want)
		}
	}
}

func TestParseDurationNever(t *testing.T) {
	d, err := ParseDuration(Never)
	if err != nil || !d.IsNever() || !d.IsSet() || d.Value() != 0 || d.String() != Never {
		t.Fatalf("never: got %+v %v", d, err)
	}
}

func TestParseDurationInvalid(t *testing.T) {
	for in, why := range map[string]string{
		"":                     "empty",
		"0d":                   "zero",
		"0":                    "no unit",
		"30":                   "no unit",
		"d":                    "no number",
		"-1d":                  "negative",
		"+1d":                  "sign",
		"1.5d":                 "fraction",
		"07d":                  "leading zero",
		"1x":                   "unknown unit",
		"1ms":                  "milliseconds are not a unit",
		"30D":                  "unit case",
		"Never":                "keyword case",
		"NEVER":                "keyword case",
		"30 d":                 whySpace,
		" 30d":                 whySpace,
		"30d ":                 whySpace,
		"1d1y":                 "out of order",
		"1d1d":                 "repeated",
		"1h1y":                 "out of order",
		"1y6w1y":               "repeated",
		"never1d":              "keyword with a number",
		"1dnever":              "keyword with a number",
		"99999999999y":         "too long",
		"9223372036854775807s": "too long (seconds overflow)",
		"1y\n":                 "newline",
	} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("%q (%s): expected an error", in, why)
		}
	}
}

func TestParseDurationErrorNamesTheValue(t *testing.T) {
	_, err := ParseDuration("1d1y")
	if err == nil || !strings.Contains(err.Error(), `"1d1y"`) {
		t.Fatalf("the error should quote the value: %v", err)
	}
}

func TestDurationString(t *testing.T) {
	for in, want := range map[string]string{text1y6w: text1y6w, text36h: "1d12h", text90d: "12w6d", "5s": "5s", Never: Never} {
		d, err := ParseDuration(in)
		if err != nil {
			t.Fatal(err)
		}
		if d.String() != want {
			t.Errorf("%q: String() = %q, want %q", in, d.String(), want)
		}
		if back, err := ParseDuration(d.String()); err != nil || back != d {
			t.Errorf("%q does not round trip: %v %v", in, back, err)
		}
	}
	if (Duration{}).IsSet() || (Duration{}).String() != "0s" {
		t.Error("the zero Duration is unset")
	}
}

func TestParseScheduleValid(t *testing.T) {
	for in, gap := range map[string]time.Duration{
		"@hourly":       time.Hour,
		DefaultSchedule: day,
		"@midnight":     day,
		"@weekly":       7 * day,
		"0 3 * * *":     day,
		"*/15 * * * *":  15 * time.Minute,
		"0 3,4 * * *":   23 * time.Hour,
	} {
		s, err := ParseSchedule(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got := s.LongestGap(); got != gap {
			t.Errorf("%q: longest gap %v, want %v", in, got, gap)
		}
		if s.String() != in {
			t.Errorf("String() = %q", s.String())
		}
	}
	if s, _ := ParseSchedule("@monthly"); s.LongestGap() != 31*day {
		t.Errorf("@monthly longest gap %v", s.LongestGap())
	}
}

func TestParseScheduleInvalid(t *testing.T) {
	for _, in := range []string{"", "daily", "@every 1h", "@every", "* * * *", "0 0 3 * * *", "61 * * * *", "@sometimes", "0 3 * * * *"} {
		if _, err := ParseSchedule(in); err == nil {
			t.Errorf("%q: expected an error", in)
		}
	}
}

func TestScheduleNext(t *testing.T) {
	s, _ := ParseSchedule(DefaultSchedule)
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	if got, want := s.Next(at), time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
}

func TestDurationDays(t *testing.T) {
	for in, want := range map[string]int32{text90d: 90, text36h: 2, "1h": 1, "1y": 365, Never: 0} {
		d, _ := ParseDuration(in)
		if got := d.Days(); got != want {
			t.Errorf("%s: %d days, want %d", in, got, want)
		}
	}
	if (Duration{}).Days() != 0 {
		t.Error("an unset Duration has no days")
	}
}
