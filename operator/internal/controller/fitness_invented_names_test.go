/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/xuri/excelize/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const inventedSuiteKey = "crew/suite/run/s0-i1.json"

func TestIterationFromTranscriptReadsInventedNames(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{}
	cases := []struct {
		name string
		json string
		want []string
	}{
		{"flagged by the runner", `{"events":[{"type":"synthesis","content":"x"}],"inventedNames":["node3"],"evidenceCaptured":true}`,
			[]string{"node3"}},
		{"nothing flagged", `{"events":[{"type":"synthesis","content":"x"}],"evidenceCaptured":true}`, nil},
		{"older transcript", `{"events":[{"type":"synthesis","content":"x"}]}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ir, ok := iterationFromTranscript(suite, inventedSuiteKey, []byte(tc.json))
			if !ok {
				t.Fatal("transcript not read")
			}
			if !reflect.DeepEqual(ir.InventedNames, tc.want) {
				t.Errorf("InventedNames = %v, want %v", ir.InventedNames, tc.want)
			}
		})
	}
}

func TestRunsSheetListsInventedNames(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: testCrewTest},
		Spec:       kubemootv1alpha1.CrewFitnessSuiteSpec{CrewRef: testCrewName, Iterations: 1},
		Status:     kubemootv1alpha1.CrewFitnessSuiteStatus{RunID: "inv00001", Phase: kubemootv1alpha1.CrewFitnessSuitePhaseCompleted},
	}
	results := []IterationResult{
		{Scenario: "a", Iteration: 1, Phase: kubemootv1alpha1.CrewFitnessPhasePassed, InventedNames: []string{"node3", "node-12345"}},
		{Scenario: "a", Iteration: 2, Phase: kubemootv1alpha1.CrewFitnessPhasePassed},
	}
	raw, err := BuildFitnessSuiteXLSX(suite, results)
	if err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	assertCellEquals(t, f, testSheetRuns, "P1", "invented_names")
	assertCellEquals(t, f, testSheetRuns, "P2", "node3, node-12345")
	assertCellEquals(t, f, testSheetRuns, "P3", "")
}

func TestScenarioResultsCountsInventedNames(t *testing.T) {
	results := []IterationResult{
		{Scenario: "a", Phase: kubemootv1alpha1.CrewFitnessPhasePassed, InventedNames: []string{"x-1", "y-2"}},
		{Scenario: "a", Phase: kubemootv1alpha1.CrewFitnessPhasePassed, InventedNames: []string{"x-1"}},
		{Scenario: "a", Phase: kubemootv1alpha1.CrewFitnessPhasePassed},
	}
	if got := scenarioResults(scripts("a"), results)[0]; got.InventedNameRuns != 2 {
		t.Errorf("InventedNameRuns = %d, want 2 (runs, not names)", got.InventedNameRuns)
	}
}
