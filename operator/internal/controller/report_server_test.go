/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"encoding/json"
	"testing"
	"time"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	"github.com/nats-io/nats.go"
)

func TestIterationFromTranscript(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
			Scripts: []kubemootv1alpha1.SuiteScript{
				{TestRef: "gpu-utilization"}, // idx 0
				{TestRef: "k8s-pod-status"},  // idx 1
			},
		},
	}

	cases := []struct {
		name     string
		key      string
		json     string
		scenario string
		iter     int32
		phase    kubemootv1alpha1.CrewFitnessPhase
		passed   int
		total    int
	}{
		{
			name:     "all pass → Passed, scenario from Scripts[0]",
			key:      "crew-test/suite/run123/s0-i1.json",
			json:     `{"assertions":[{"raw":"a","passed":true,"message":""},{"raw":"b","passed":true,"message":""}],"durationMs":1500,"startedAt":"2026-06-02T12:00:00Z"}`,
			scenario: "gpu-utilization", iter: 1, phase: kubemootv1alpha1.CrewFitnessPhasePassed, passed: 2, total: 2,
		},
		{
			name:     "one fail → Failed, scenario from Scripts[1]",
			key:      "crew-test/suite/run123/s1-i3.json",
			json:     `{"assertions":[{"raw":"a","passed":true,"message":""},{"raw":"b","passed":false,"message":"nope"}],"durationMs":2100,"startedAt":"2026-06-02T12:01:00Z"}`,
			scenario: "k8s-pod-status", iter: 3, phase: kubemootv1alpha1.CrewFitnessPhaseFailed, passed: 1, total: 2,
		},
		{
			name:     "no assertions → Error",
			key:      "crew-test/suite/run123/s0-i7.json",
			json:     `{"assertions":[],"durationMs":300,"startedAt":""}`,
			scenario: "gpu-utilization", iter: 7, phase: kubemootv1alpha1.CrewFitnessPhaseError, passed: 0, total: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ir, ok := iterationFromTranscript(suite, tc.key, []byte(tc.json))
			if !ok {
				t.Fatalf("iterationFromTranscript returned ok=false")
			}
			assertIterationFields(t, ir, tc.scenario, tc.iter, tc.phase, tc.passed, tc.total)
		})
	}

	// Malformed key → not ok.
	if _, ok := iterationFromTranscript(suite, "crew/suite/run/notakey.json", []byte("{}")); ok {
		t.Errorf("malformed key should return ok=false")
	}
}

// assertIterationFields checks the scenario, iteration, phase, and assertion
// counts of a parsed iteration result. Extracting it flattens the per-case
// verification out of the table loop's closure.
func assertIterationFields(t *testing.T, ir IterationResult, scenario string, iter int32, phase kubemootv1alpha1.CrewFitnessPhase, passed, total int) {
	t.Helper()
	if ir.Scenario != scenario {
		t.Errorf("Scenario = %q, want %q", ir.Scenario, scenario)
	}
	if ir.Iteration != iter {
		t.Errorf("Iteration = %d, want %d", ir.Iteration, iter)
	}
	if ir.Phase != phase {
		t.Errorf("Phase = %q, want %q", ir.Phase, phase)
	}
	if ir.AssertionsPassed != passed || ir.AssertionsTotal != total {
		t.Errorf("asserts = %d/%d, want %d/%d", ir.AssertionsPassed, ir.AssertionsTotal, passed, total)
	}
}

// TestIterationConsensusGate verifies the consensus floor (ConsensusOK) is read
// from the stored ">=N toolers agree" assertion, while participation is computed
// independently from the agree findings in the event stream (not the floor), so
// the two can diverge (floor met but agree depth below expected -> participation < 100).
func TestIterationConsensusGate(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
			Scripts: []kubemootv1alpha1.SuiteScript{{TestRef: "k8s-pod-status"}},
		},
	}
	// Participation is now the TRUE agree count from the event stream (not a mirror
	// of the consensus gate), scored against the assertion's expected N. ConsensusOK
	// still comes from the floor assertion result, so the two can diverge.
	agree2 := `,"events":[{"type":"finding","agent":"k8s","signal":"agree"},{"type":"finding","agent":"gpu","signal":"agree"}]`
	agree1 := `,"events":[{"type":"finding","agent":"k8s","signal":"agree"}]`
	cases := []struct {
		name          string
		json          string
		wantConsensus bool
		wantPartic    float64
	}{
		{
			name:          "floor met + 2 agree events -> consensus ok, participation 100",
			json:          `{"assertions":[{"raw":"at least 2 toolers contribute with signal=agree","passed":true}]` + agree2 + `,"durationMs":100}`,
			wantConsensus: true, wantPartic: 100,
		},
		{
			name:          "floor met but only 1 agree event (depth < expected) -> consensus ok, participation 50",
			json:          `{"assertions":[{"raw":"at least 2 toolers contribute with signal=agree","passed":true}]` + agree1 + `,"durationMs":100}`,
			wantConsensus: true, wantPartic: 50,
		},
		{
			name:          "floor missed, no agree events -> consensus failed, participation 0",
			json:          `{"assertions":[{"raw":"at least 2 toolers contribute with signal=agree","passed":false}],"durationMs":100}`,
			wantConsensus: false, wantPartic: 0,
		},
		{
			name:          "no consensus assertion → not penalized",
			json:          `{"assertions":[{"raw":"synthesis is non-empty","passed":true}]` + agree1 + `,"durationMs":100}`,
			wantConsensus: true, wantPartic: 100,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ir, ok := iterationFromTranscript(suite, "ns/suite/run/s0-i1.json", []byte(tc.json))
			if !ok {
				t.Fatalf("ok=false")
			}
			if ir.ConsensusOK != tc.wantConsensus {
				t.Errorf("ConsensusOK = %v, want %v", ir.ConsensusOK, tc.wantConsensus)
			}
			if ir.Participation != tc.wantPartic {
				t.Errorf("Participation = %v, want %v", ir.Participation, tc.wantPartic)
			}
		})
	}
}

// TestDeferredCacheRoundTrip checks the v2 checkpoint: scores survive a write/read,
// the Complete flag is preserved, and an absent checkpoint reads empty+incomplete.
func TestDeferredCacheRoundTrip(t *testing.T) {
	store := fakeStore{objs: map[string][]byte{}}
	prefix := "ns/suite/run/"

	if c := loadDeferredCache(store, prefix); c.Complete || len(c.Scores) != 0 {
		t.Errorf("absent checkpoint should be empty+incomplete, got %+v", c)
	}

	blob, _ := json.Marshal(deferredScoreCache{Scores: map[string]float64{"gpu": 82.5}, Complete: false})
	_, _ = store.PutObject(FitnessArtifactsBucket, deferredSidecarKey(prefix), blob, time.Hour)
	if got := readDeferredScores(store, prefix)["gpu"]; got != 82.5 {
		t.Errorf("readDeferredScores[gpu] = %v, want 82.5", got)
	}
	if loadDeferredCache(store, prefix).Complete {
		t.Errorf("checkpoint should be incomplete")
	}

	blob2, _ := json.Marshal(deferredScoreCache{Scores: map[string]float64{"gpu": 82.5}, Complete: true})
	_, _ = store.PutObject(FitnessArtifactsBucket, deferredSidecarKey(prefix), blob2, time.Hour)
	if !loadDeferredCache(store, prefix).Complete {
		t.Errorf("checkpoint should be complete after Complete=true write")
	}
}

// fakeStore implements objectStore for GenerateSuiteReport's transcript reads.
type fakeStore struct {
	objs   map[string][]byte
	delErr error // when set, DeleteObject returns it (for purge error-path tests)
}

func (s fakeStore) ListObjects(_, prefix string) ([]string, error) {
	var out []string
	for k := range s.objs {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, k)
		}
	}
	return out, nil
}
func (s fakeStore) GetObject(_, key string) ([]byte, error) { return s.objs[key], nil }
func (s fakeStore) PutObject(_, key string, data []byte, _ time.Duration) (*nats.ObjectInfo, error) {
	s.objs[key] = data
	return &nats.ObjectInfo{}, nil
}
func (s fakeStore) DeleteObject(_, key string) error {
	if s.delErr != nil {
		return s.delErr
	}
	delete(s.objs, key)
	return nil
}
