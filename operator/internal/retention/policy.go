/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package retention

import (
	"errors"
	"fmt"
	"time"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// MaxWindow is the longest finite window a crew may choose for conversations and other
// data. The shared conversation bucket carries a backstop expiry of this length, so no
// crew's window is cut short by it. Memory has no such limit: it may be never.
const MaxWindow = 5 * 365 * 24 * time.Hour

// MaxWindowText is MaxWindow as the install writes it to the bucket.
const MaxWindowText = "5y"

// Policy is a crew's retention with every default applied and every value parsed.
type Policy struct {
	Schedule Schedule
	// Conversations is how long a conversation is kept after its last turn.
	Conversations time.Duration
	// Memory is how long a fact is kept after it was last written or recalled.
	Memory Duration
	// Other is how long a message of the crew is kept; zero leaves the streams' limits.
	Other time.Duration
	// Warnings name a duration finer than the schedule can enforce.
	Warnings []string
}

// Resolve turns a Crew's declared retention into a Policy. A crew that declares
// nothing, or whose defaults the API server did not fill, keeps everything (a zero
// Policy with a nil Schedule) rather than removing data on a guess. Every invalid value
// is reported together.
func Resolve(crew *kubemootv1alpha1.Crew) (Policy, error) {
	r := crew.Spec.Retention
	if r == nil || (r.Schedule == "" && r.PurgeUnused == "") {
		return Policy{}, nil
	}
	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	var p Policy
	var err error
	if p.Schedule, err = ParseSchedule(orDefault(r.Schedule, DefaultSchedule)); err != nil {
		collect(err)
	}
	unused, err := parseField("retention.purgeUnused", string(r.PurgeUnused))
	collect(err)

	p.Conversations, err = conversationWindow(r, unused)
	collect(err)
	p.Memory, err = memoryPurge(crew, r, unused)
	collect(err)
	p.Other, err = otherWindow(r)
	collect(err)

	if len(errs) > 0 {
		return Policy{}, errors.Join(errs...)
	}
	p.Warnings = finerThanSchedule(p)
	return p, nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func parseField(field, text string) (Duration, error) {
	if text == "" {
		return Duration{}, nil
	}
	d, err := ParseDuration(text)
	if err != nil {
		return Duration{}, fmt.Errorf("%s: %w", field, err)
	}
	return d, nil
}

// finiteWindow parses a window that may not be never and may not exceed MaxWindow.
func finiteWindow(field, text string) (time.Duration, error) {
	d, err := parseField(field, text)
	if err != nil || !d.IsSet() {
		return 0, err
	}
	if d.IsNever() {
		return 0, fmt.Errorf("%s: never is not accepted; use a duration up to %s", field, MaxWindowText)
	}
	if d.Value() > MaxWindow {
		return 0, fmt.Errorf("%s: %s is longer than the %s the shared stores keep", field, d, MaxWindowText)
	}
	return d.Value(), nil
}

// conversationWindow is the shorter of the conversation window and the crew-wide
// purgeUnused, which a conversation's last turn is the activity of.
func conversationWindow(r *kubemootv1alpha1.CrewRetention, unused Duration) (time.Duration, error) {
	var text string
	if r.Conversations != nil {
		text = string(r.Conversations.Window)
	}
	w, err := finiteWindow("retention.conversations.window", text)
	if err != nil {
		return 0, err
	}
	if !unused.IsNever() && unused.IsSet() && (w == 0 || unused.Value() < w) {
		w = unused.Value()
	}
	return w, nil
}

// memoryPurge is the crew's memory.purgeUnused, else the crew-wide purgeUnused. The
// deprecated memory.ttlDays overrides both when set.
func memoryPurge(crew *kubemootv1alpha1.Crew, r *kubemootv1alpha1.CrewRetention, unused Duration) (Duration, error) {
	if m := crew.Spec.Memory; m != nil && m.TTLDays > 0 {
		return Of(time.Duration(m.TTLDays) * 24 * time.Hour), nil
	}
	if r.Memory != nil && r.Memory.PurgeUnused != "" {
		return parseField("retention.memory.purgeUnused", string(r.Memory.PurgeUnused))
	}
	return unused, nil
}

func otherWindow(r *kubemootv1alpha1.CrewRetention) (time.Duration, error) {
	if r.Other == nil {
		return 0, nil
	}
	return finiteWindow("retention.other.window", string(r.Other.Window))
}

// finerThanSchedule names each duration shorter than the longest wait between runs.
func finerThanSchedule(p Policy) []string {
	gap := p.Schedule.LongestGap()
	var out []string
	check := func(name string, d time.Duration) {
		if d > 0 && d < gap {
			out = append(out, fmt.Sprintf("%s %s is finer than the schedule %q (up to %s between runs): data is removed at the next run after it expires", name, Of(d), p.Schedule, Of(gap)))
		}
	}
	check("conversations window", p.Conversations)
	if !p.Memory.IsNever() {
		check("memory purgeUnused", p.Memory.Value())
	}
	check("other window", p.Other)
	return out
}

// Enforced reports whether the policy removes anything: a crew whose defaults were not
// filled has none.
func (p Policy) Enforced() bool { return p.Schedule.cron != nil }
