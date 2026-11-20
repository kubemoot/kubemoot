/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package retention

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// DefaultSchedule is the cleanup schedule a policy takes when none is declared.
const DefaultSchedule = "@daily"

var scheduleParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Schedule is when the cleanup runs.
type Schedule struct {
	text string
	cron cron.Schedule
}

// ParseSchedule reads a cron expression of five fields, or @hourly, @daily, @midnight,
// @weekly, @monthly, @yearly, @annually. @every and a seconds field are not accepted.
func ParseSchedule(s string) (Schedule, error) {
	if strings.HasPrefix(s, "@every") {
		return Schedule{}, fmt.Errorf("schedule %q: use a cron expression or @hourly, @daily, @weekly, @monthly, @yearly", s)
	}
	sched, err := scheduleParser.Parse(s)
	if err != nil {
		return Schedule{}, fmt.Errorf("schedule %q: %w", s, err)
	}
	return Schedule{text: s, cron: sched}, nil
}

// String is the schedule as written.
func (s Schedule) String() string { return s.text }

// Next is the first run after t.
func (s Schedule) Next(t time.Time) time.Time { return s.cron.Next(t) }

// gapSamples is how many consecutive runs LongestGap looks at.
const gapSamples = 400

// LongestGap is the longest wait between two runs, found over the runs that follow a
// fixed date. Data can outlast its expiry by this long.
func (s Schedule) LongestGap() time.Duration {
	t := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	var longest time.Duration
	prev := s.cron.Next(t)
	for i := 0; i < gapSamples; i++ {
		next := s.cron.Next(prev)
		if next.IsZero() {
			break
		}
		if gap := next.Sub(prev); gap > longest {
			longest = gap
		}
		prev = next
	}
	return longest
}
