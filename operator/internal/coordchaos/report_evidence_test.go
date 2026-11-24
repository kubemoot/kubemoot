/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package coordchaos

import (
	"strings"
	"testing"
	"time"
)

// The evidence section explains a run: a control run lists its notes, a trial that failed
// to run lists its error, and a clean protected run adds nothing.
func TestEvidenceListsControlNotesAndTrialErrorsOnly(t *testing.T) {
	r := Report{Cases: []CaseResult{
		{Case: controlCase, Trials: []Trial{{Number: 1, Notes: []string{"the fault landed late"}}}},
		{Case: protectedCase, Trials: []Trial{{Number: 2, Notes: []string{"ignored outside a control"}, Error: "pod gone"}}},
		{Case: protectedCase, Trials: []Trial{{Number: 3, Violations: []Violation{
			{Check: vAnswer, Detail: "2 synthesis messages", Evidence: []string{"m1", "m2"}},
			{Check: vAnswer, Detail: "no evidence kept"},
		}}}},
	}}
	got := r.evidence()
	for _, want := range []string{
		"## Evidence", "- kill@x (control) trial 1: the fault landed late", "- kill@x trial 2: error: pod gone",
		"- kill@x trial 3: one_answer_on_bus: 2 synthesis messages\n  evidence: m1, m2\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("evidence lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "no evidence kept\n  evidence") {
		t.Errorf("a violation without evidence printed an evidence line:\n%s", got)
	}
	if strings.Contains(got, "ignored outside a control") {
		t.Errorf("notes of a protected case were listed:\n%s", got)
	}
	clean := Report{Cases: []CaseResult{{Case: protectedCase, Trials: []Trial{{Number: 1}}}}}
	if clean.evidence() != "" {
		t.Errorf("a clean run produced evidence: %q", clean.evidence())
	}
}

// The table shows the trial where the fault landed; with none landed, the last trial tried;
// with no trials, an empty one.
func TestLandedTrialPrefersTheLandedThenTheLast(t *testing.T) {
	trials := []Trial{{Number: 1}, {Number: 2, Landed: true}, {Number: 3}}
	if got := landedTrial(trials); got.Number != 2 {
		t.Errorf("landed trial = %d, want 2", got.Number)
	}
	if got := landedTrial(trials[:1]); got.Number != 1 {
		t.Errorf("with none landed = %d, want the last tried", got.Number)
	}
	if got := landedTrial(nil); got.Number != 0 || got.Landed {
		t.Errorf("with no trials = %+v, want empty", got)
	}
}

// A timing before the fault cannot be negative.
func TestPositiveClampsANegativeDuration(t *testing.T) {
	if positive(-time.Second) != 0 || positive(1500*time.Millisecond) != 1.5 {
		t.Errorf("positive = %v, %v", positive(-time.Second), positive(1500*time.Millisecond))
	}
}

func TestThreadFirstFindsTheEarliestEventOfAType(t *testing.T) {
	th := Thread{Events: []Event{{Type: "a", Seq: 1}, {Type: "b", Seq: 2}, {Type: "b", Seq: 3}}}
	if ev, ok := th.First("b"); !ok || ev.Seq != 2 {
		t.Errorf("First(b) = %+v, %v, want seq 2", ev, ok)
	}
	if _, ok := th.First("c"); ok {
		t.Error("First(c) found an event the log does not hold")
	}
}

func TestDeathQuietUsesTheConfiguredWindowElseTheDefault(t *testing.T) {
	r := &Runner{}
	if r.deathQuiet() != defaultDeathQuiet {
		t.Errorf("default = %v", r.deathQuiet())
	}
	r.Cfg.DeathQuiet = 5 * time.Second
	if r.deathQuiet() != 5*time.Second {
		t.Errorf("configured = %v", r.deathQuiet())
	}
}
