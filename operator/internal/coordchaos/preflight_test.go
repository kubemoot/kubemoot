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
)

func podWith(env map[string]string) Pod { return Pod{Name: "p", Env: env} }

func TestFlagsFromEnvAndEffective(t *testing.T) {
	on := podWith(map[string]string{envEventLog: flagTrue, envResume: flagTrue})
	if got, err := EffectiveFlags([]Pod{on, on}); err != nil || got != (Flags{EventLog: true, Resume: true}) {
		t.Fatalf("got %+v %v", got, err)
	}
	off := podWith(map[string]string{envEventLog: "false"})
	if _, err := EffectiveFlags([]Pod{on, off}); err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Fatalf("mixed pods accepted: %v", err)
	}
	if _, err := EffectiveFlags(nil); err == nil {
		t.Fatal("no pods accepted")
	}
}

func TestCheckFlagsRefusesAnUnprotectedCoordinator(t *testing.T) {
	need := Flags{EventLog: true, Resume: true, Lease: true}
	_, err := CheckFlags(ModeProtected, need, Flags{EventLog: true}, nil)
	if err == nil || !strings.Contains(err.Error(), envResume) || !strings.Contains(err.Error(), envLease) {
		t.Fatalf("got %v", err)
	}
	if _, err := CheckFlags(ModeProtected, need, Flags{}, nil); err == nil {
		t.Fatal("flags off accepted in protected mode")
	}
	if _, err := CheckFlags(ModeProtected, need, need, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCheckFlagsControlNeedsFlagsOff(t *testing.T) {
	if _, err := CheckFlags(ModeControl, Flags{}, Flags{EventLog: true}, nil); err == nil {
		t.Fatal("control accepted a protected coordinator")
	}
	if _, err := CheckFlags(ModeControl, Flags{}, Flags{}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCheckFlagsNotesAnOverride(t *testing.T) {
	cfg := Flags{EventLog: true, Resume: true}
	notes, err := CheckFlags(ModeProtected, Flags{EventLog: true}, Flags{EventLog: true}, &cfg)
	if err != nil || len(notes) != 1 || !strings.Contains(notes[0], "Agent's own env wins") {
		t.Fatalf("got %v %v", notes, err)
	}
}

const sampleMetrics = `# HELP kubemoot_coordinator_resumes_total x
# TYPE kubemoot_coordinator_resumes_total counter
kubemoot_coordinator_resumes_total{action="sweep",application="agent"} 2.0
kubemoot_coordinator_resumes_total{action="restart_synthesis"} 1.0
kubemoot_coordinator_log_checks_total{result="match"} 4.0
kubemoot_coordinator_log_checks_total{result="mismatch"} 1.0
kubemoot_coordinator_log_fenced_total 3.0
kubemoot_coordinator_log_append_failures_total 0.0
kubemoot_coordinator_threads_unlogged_total 1.0
jvm_memory_used_bytes{area="heap"} 12345
kubemoot_coordinator_other_total 9
`

func TestParseCounters(t *testing.T) {
	got := ParseCounters(sampleMetrics)
	want := Counters{
		"resumes.sweep": 2, "resumes.restart_synthesis": 1, "checks.match": 4, kMismatch: 1,
		kFenced: 3, "append_failures": 0, "threads_unlogged": 1,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %v want %v", k, got[k], v)
		}
	}
}

func TestDeltaCountsNewPodsFromZeroAndIgnoresGonePods(t *testing.T) {
	before := map[string]Counters{"old": {kFenced: 1}, "gone": {kFenced: 9}}
	after := map[string]Counters{"old": {kFenced: 3, kMismatch: 1}, "new": {kFenced: 2}}
	got := Delta(before, after)
	if got[kFenced] != 4 || got[kMismatch] != 1 {
		t.Fatalf("got %v", got)
	}
}
