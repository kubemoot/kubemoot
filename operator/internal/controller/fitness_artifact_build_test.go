/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"testing"
	"time"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BuildFitnessSuiteXLSXWithMeasures renders every report sheet. Driving it with a
// passing run, a failed-assertion run, and an errored run exercises the run-row,
// assertions, failure-summary, scenarios, and overview writers in one pass.
func TestBuildFitnessSuiteXLSXWithMeasures(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "suite", Namespace: "ns"},
		Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
			CrewRef:    "crew-a",
			Iterations: 2,
			Scripts:    []kubemootv1alpha1.SuiteScript{{TestRef: "scenario-x"}},
		},
		Status: kubemootv1alpha1.CrewFitnessSuiteStatus{RunID: "run1"},
	}
	started := time.Unix(1_700_000_000, 0)
	results := []IterationResult{
		{
			Scenario: "scenario-x", Iteration: 1, StartedAt: started, DurationMs: 1200,
			Phase: kubemootv1alpha1.CrewFitnessPhasePassed, AssertionsPassed: 2, AssertionsTotal: 2,
			Assertions: []kubemootv1alpha1.AssertionResult{
				{Raw: "ASSERT answer mentions pods", Passed: true, Message: "ok"},
				{Raw: "at least 1 tooler agree", Passed: true, Message: "ok"},
			},
			Synthesis: "the answer", Question: "q", Correctness: 90, Adherence: 85, Efficiency: 80,
			ConsensusOK: true, Participation: 70, Selectivity: 65,
		},
		{
			Scenario: "scenario-x", Iteration: 2, StartedAt: started, DurationMs: 1500,
			Phase: kubemootv1alpha1.CrewFitnessPhaseFailed, AssertionsPassed: 1, AssertionsTotal: 2,
			Assertions: []kubemootv1alpha1.AssertionResult{
				{Raw: "ASSERT answer mentions pods", Passed: true, Message: "ok"},
				{Raw: "ASSERT answer cites a source", Passed: false, Message: "no citation found"},
			},
			Synthesis: "partial", Question: "q", Correctness: 40, Adherence: 50, Efficiency: 60,
			ConsensusOK: true, Participation: 30, Selectivity: 20,
		},
		{
			Scenario: "scenario-y", Iteration: 1, StartedAt: started, DurationMs: 0,
			Phase: kubemootv1alpha1.CrewFitnessPhaseError, Error: "crew did not start",
		},
	}
	consistency := map[string]float64{"scenario-x": 0.92}
	judgeQuality := map[string]float64{"scenario-x": 88}

	out, err := BuildFitnessSuiteXLSXWithMeasures(suite, results, consistency, judgeQuality)
	if err != nil {
		t.Fatalf("BuildFitnessSuiteXLSXWithMeasures: %v", err)
	}
	if len(out) == 0 {
		t.Error("expected non-empty XLSX bytes")
	}
	// XLSX is a zip archive; sanity-check the magic bytes.
	if len(out) < 2 || out[0] != 'P' || out[1] != 'K' {
		t.Errorf("output is not a zip/xlsx (magic = %x)", out[:min(2, len(out))])
	}
}
