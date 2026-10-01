/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package fitnessscript

import "testing"

// Event and signal names the tests share.
const (
	sigAgree    = "agree"
	evDone      = "done"
	evFinding   = "finding"
	evConnected = "connected"
	evSignal    = "signal"
	termSolid   = "solid"
)

// Evaluate keeps assertion order and checks each one against the same state.
func TestEvaluateKeepsOrder(t *testing.T) {
	ft := ParseFitnessTest(`DEFINE CONST QUESTION AS "q"
ASSERT(synthesis CONTAINS "cilium")
ASSERT(synthesis does NOT CONTAIN "harbor")
ASSERT(at least 2 specialist contributes with signal=agree)`)
	state := RunState{
		PostOK:    true,
		Events:    []SignalEvent{{Type: evFinding, Signal: sigAgree}, {Type: evDone}},
		Synthesis: "cilium and harbor",
	}
	got := Evaluate(ft.Assertions, state)
	want := []bool{true, false, false}
	if len(got) != len(want) {
		t.Fatalf("got %d results, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Passed != want[i] || got[i].Raw != ft.Assertions[i].Raw {
			t.Errorf("result[%d] = %+v, want passed=%v raw=%q", i, got[i], want[i], ft.Assertions[i].Raw)
		}
	}
}

func TestEvaluateEmpty(t *testing.T) {
	if got := Evaluate(nil, RunState{}); len(got) != 0 {
		t.Errorf("no assertions should give no results, got %v", got)
	}
}

func TestHasDoneAndCountAgrees(t *testing.T) {
	events := make([]SignalEvent, 0, 5)
	events = append(events,
		SignalEvent{Type: evFinding, Signal: sigAgree},
		SignalEvent{Type: evFinding, Signal: "concern"},
		SignalEvent{Type: "phase", Signal: sigAgree},
		SignalEvent{Type: evFinding, Signal: sigAgree},
	)
	if HasDone(events) {
		t.Error("no done event: HasDone must be false")
	}
	if n := CountAgreeSignals(events); n != 2 {
		t.Errorf("CountAgreeSignals = %d, want 2 (finding events only)", n)
	}
	if !HasDone(append(events, SignalEvent{Type: evDone})) {
		t.Error("HasDone must see the done event")
	}
}
