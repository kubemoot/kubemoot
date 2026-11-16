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
	"encoding/json"
	"reflect"
	"testing"

	"github.com/xuri/excelize/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	inventedAgent     = "k8s-nodes"
	inventedNode3     = "node3"
	inventedSuiteKey  = "crew/suite/run/s0-i1.json"
	inventedQuestion  = "The recent pod restarts were caused by the node running out of disk, tell me which node."
	inventedSynthesis = "Restarts came from **node3** running out of disk; see `k8s-nodes/001-resources_list` " +
		"and **homelab-k8s-1-cp**."
)

// inventedTranscript is a stored run: the answer, the stream's agents, and the
// tool-result tokens the runner captured.
func inventedTranscript(captured bool, names []string) []byte {
	b, _ := json.Marshal(map[string]any{
		"question": inventedQuestion,
		"assertions": []map[string]any{
			{"raw": "synthesis is non-empty", "passed": true},
		},
		"events": []transcriptEvent{
			{Type: "finding", Agent: inventedAgent, Signal: "agree", Content: "nodes listed"},
			{Type: "synthesis", Content: inventedSynthesis},
		},
		"evidenceNames":    names,
		"evidenceCaptured": captured,
	})
	return b
}

func TestIterationFromTranscriptFlagsInventedNames(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{}
	cases := []struct {
		name     string
		captured bool
		names    []string
		want     []string
	}{
		{"name in no tool result", true, []string{"homelab-k8s-1-cp", "homelab-k8s-1-gpu-worker"}, []string{inventedNode3}},
		{"all names in tool results", true, []string{"homelab-k8s-1-cp", inventedNode3}, nil},
		{"tool results not captured", false, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ir, ok := iterationFromTranscript(suite, inventedSuiteKey, inventedTranscript(tc.captured, tc.names))
			if !ok {
				t.Fatal("transcript not read")
			}
			if !reflect.DeepEqual(ir.InventedNames, tc.want) {
				t.Errorf("InventedNames = %v, want %v", ir.InventedNames, tc.want)
			}
			if ir.Correctness != 100 {
				t.Errorf("the flag must not change scores, correctness = %v", ir.Correctness)
			}
		})
	}
}

func TestInventedNamesEmptySynthesisIsNotChecked(t *testing.T) {
	if got := inventedNamesFromTranscript(transcriptDoc{EvidenceCaptured: true}, ""); got != nil {
		t.Errorf("no answer, got %v", got)
	}
}

func TestRunsSheetListsInventedNames(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: testCrewTest},
		Spec:       kubemootv1alpha1.CrewFitnessSuiteSpec{CrewRef: testCrewName, Iterations: 1},
		Status:     kubemootv1alpha1.CrewFitnessSuiteStatus{RunID: "inv00001", Phase: kubemootv1alpha1.CrewFitnessSuitePhaseCompleted},
	}
	results := []IterationResult{
		{Scenario: "a", Iteration: 1, Phase: kubemootv1alpha1.CrewFitnessPhasePassed, InventedNames: []string{inventedNode3, "node-12345"}},
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
