/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package retention reads a Crew's retention policy and removes the data it says has
// expired from the shared NATS buckets and streams, by the crew's key prefix.
package retention

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Never is the duration word for "kept until removed another way".
const Never = "never"

// Units of the duration form, largest first. A year is 365 days and a week 7 days.
var units = []struct {
	suffix byte
	size   time.Duration
}{
	{'y', 365 * 24 * time.Hour},
	{'w', 7 * 24 * time.Hour},
	{'d', 24 * time.Hour},
	{'h', time.Hour},
	{'m', time.Minute},
	{'s', time.Second},
}

// Duration is how long data is kept: a length of time, or never.
type Duration struct {
	value time.Duration
	never bool
}

// Forever is the Duration never.
var Forever = Duration{never: true}

// Of returns a finite Duration.
func Of(d time.Duration) Duration { return Duration{value: d} }

// IsNever reports whether the data is kept until removed another way.
func (d Duration) IsNever() bool { return d.never }

// Value is the length of time; zero for never.
func (d Duration) Value() time.Duration { return d.value }

// IsSet reports whether the Duration was parsed (a zero Duration is unset).
func (d Duration) IsSet() bool { return d.never || d.value > 0 }

// ParseDuration reads the form Prometheus uses for its retention: a number and a
// unit (s, m, h, d, w, y), several combined from the largest unit down with each
// unit at most once (1y6w, 90d, 36h), or the word never. It rejects an empty string,
// zero, a negative or fractional number, a leading zero, an unknown unit, units out
// of order or repeated, spaces, other case, and a value too large to represent.
func ParseDuration(s string) (Duration, error) {
	if s == Never {
		return Forever, nil
	}
	if s == "" {
		return Duration{}, errors.New("duration is empty")
	}
	var total time.Duration
	rest := s
	next := 0 // index into units: the next unit may be this one or a smaller one
	for rest != "" {
		n, tail, err := leadingNumber(rest)
		if err != nil {
			return Duration{}, fmt.Errorf("duration %q: %w", s, err)
		}
		if tail == "" {
			return Duration{}, fmt.Errorf("duration %q: %q has no unit (use one of s, m, h, d, w, y)", s, rest)
		}
		idx := unitIndex(tail[0])
		if idx < 0 {
			return Duration{}, fmt.Errorf("duration %q: unknown unit %q (use one of s, m, h, d, w, y)", s, tail[:1])
		}
		if idx < next {
			return Duration{}, fmt.Errorf("duration %q: %q is repeated or out of order (write the largest unit first, each once)", s, tail[:1])
		}
		next = idx + 1
		part, err := scale(n, units[idx].size)
		if err != nil {
			return Duration{}, fmt.Errorf("duration %q: %w", s, err)
		}
		if total > math.MaxInt64-part {
			return Duration{}, fmt.Errorf("duration %q: too long", s)
		}
		total += part
		rest = tail[1:]
	}
	return Duration{value: total}, nil
}

// leadingNumber splits a positive whole number without a leading zero off the front.
func leadingNumber(s string) (int64, string, error) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, "", fmt.Errorf("expected a number before %q", s)
	}
	digits := s[:i]
	if digits[0] == '0' {
		return 0, "", fmt.Errorf("%q must be a positive number without a leading zero", digits)
	}
	var n int64
	for _, c := range digits {
		d := int64(c - '0')
		if n > (math.MaxInt64-d)/10 {
			return 0, "", fmt.Errorf("%q is too large", digits)
		}
		n = n*10 + d
	}
	return n, s[i:], nil
}

func unitIndex(c byte) int {
	for i, u := range units {
		if u.suffix == c {
			return i
		}
	}
	return -1
}

// scale multiplies n units of size, refusing a result that does not fit.
func scale(n int64, size time.Duration) (time.Duration, error) {
	if n > int64(math.MaxInt64/size) {
		return 0, errors.New("too long")
	}
	return time.Duration(n) * size, nil
}

// String writes the Duration back in the largest whole units, or never.
func (d Duration) String() string {
	if d.never {
		return Never
	}
	var b strings.Builder
	rest := d.value
	for _, u := range units {
		if n := rest / u.size; n > 0 {
			fmt.Fprintf(&b, "%d%c", n, u.suffix)
			rest -= n * u.size
		}
	}
	if b.Len() == 0 {
		return "0s"
	}
	return b.String()
}

// Days is the Duration in whole days, rounded up and at least one; 0 for never and for
// an unset Duration.
func (d Duration) Days() int32 {
	if d.never || d.value <= 0 {
		return 0
	}
	const day = 24 * time.Hour
	days := (d.value + day - 1) / day
	return int32(min(days, math.MaxInt32)) //nolint:gosec // clamped to the int32 range
}
