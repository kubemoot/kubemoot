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
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/xuri/excelize/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	invGotcha     = "gotcha"
	invCrew       = "inv-crew"
	invFlaggedKey = "ns/s/r1/s0-i1.json"
	invSecondKey  = "ns/s/r1/s0-i2.json"
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
		Spec:       kubemootv1alpha1.CrewFitnessSuiteSpec{SkipToolSmoke: true, CrewRef: testCrewName, Iterations: 1},
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

// transcripts with the runner's inventedNames reach status.scenarios: the rows of a
// suite are built from the CrewFitness children, which carry no transcript, so the
// count is added from the stored transcripts once the run is terminal.
func TestAddInventedCountsFromTranscripts(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{SkipToolSmoke: true, Scripts: []kubemootv1alpha1.SuiteScript{
			{TestRef: invGotcha}, {TestRef: "quiet"},
		}},
		Status: kubemootv1alpha1.CrewFitnessSuiteStatus{RunID: "r1"},
	}
	suite.Status.Scenarios = scenarioResults(suite.Spec.Scripts, nil)
	flagged := `{"events":[{"type":"synthesis","content":"x"}],"inventedNames":["homelab-k8s-1"],"evidenceCaptured":true}`
	clean := `{"events":[{"type":"synthesis","content":"x"}],"evidenceCaptured":true}`
	store := fakeStore{objs: map[string][]byte{
		invFlaggedKey: []byte(flagged), invSecondKey: []byte(flagged),
		"ns/s/r1/s0-i3.json": []byte(clean), "ns/s/r1/s1-i1.json": []byte(clean),
		"ns/s/r2/s1-i1.json": []byte(flagged), // another run
	}}
	addInventedCounts(store, suite)
	got := suite.Status.Scenarios
	if got[0].InventedNameRuns != 2 || got[1].InventedNameRuns != 0 {
		t.Errorf("inventedNameRuns = %d, %d; want 2, 0", got[0].InventedNameRuns, got[1].InventedNameRuns)
	}
	raw, _ := json.Marshal(suite.Status)
	if !bytes.Contains(raw, []byte(`"inventedNameRuns":2`)) {
		t.Errorf("status JSON must carry the count: %s", raw)
	}
}

func TestAddInventedCountsLeavesRowsWithoutAStore(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{Status: kubemootv1alpha1.CrewFitnessSuiteStatus{
		RunID: "r1", Scenarios: []kubemootv1alpha1.SuiteScenarioResult{{Name: "a", InventedNameRuns: 3}}}}
	addInventedCounts(nil, suite)
	addInventedCounts(listFailStore{}, suite)
	if suite.Status.Scenarios[0].InventedNameRuns != 3 {
		t.Error("no store or a failed listing must keep the rows as they are")
	}
}

// failingGetStore fails the read of one key, as a transient object store error.
type failingGetStore struct {
	fakeStore
	failKey string
}

func (s failingGetStore) GetObject(b, key string) ([]byte, error) {
	if key == s.failKey {
		return nil, errors.New("timeout")
	}
	return s.fakeStore.GetObject(b, key)
}

func TestAddInventedCountsKeepsTheCountWhenATranscriptIsUnreadable(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Spec:       kubemootv1alpha1.CrewFitnessSuiteSpec{SkipToolSmoke: true, Scripts: []kubemootv1alpha1.SuiteScript{{TestRef: invGotcha}}},
		Status:     kubemootv1alpha1.CrewFitnessSuiteStatus{RunID: "r1"},
	}
	suite.Status.Scenarios = scenarioResults(suite.Spec.Scripts, nil)
	suite.Status.Scenarios[0].InventedNameRuns = 2
	flagged := []byte(`{"events":[{"type":"synthesis","content":"x"}],"inventedNames":["a-1"]}`)
	store := failingGetStore{
		fakeStore: fakeStore{objs: map[string][]byte{invFlaggedKey: flagged, invSecondKey: flagged}},
		failKey:   invSecondKey,
	}
	addInventedCounts(store, suite)
	if got := suite.Status.Scenarios[0].InventedNameRuns; got != 2 {
		t.Errorf("a failed read must not lower the count: got %d, want 2", got)
	}
	store.failKey = ""
	addInventedCounts(store, suite)
	if got := suite.Status.Scenarios[0].InventedNameRuns; got != 2 {
		t.Errorf("all transcripts read: got %d, want 2", got)
	}
}

// persistArtifact writes the XLSX and the status in one update; the count reaches
// the stored status from the transcripts in the injected store.
func TestPersistArtifactWritesInventedCountsToStatus(t *testing.T) {
	scheme := reapScheme(t)
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Spec:       kubemootv1alpha1.CrewFitnessSuiteSpec{SkipToolSmoke: true, CrewRef: invCrew, Scripts: []kubemootv1alpha1.SuiteScript{{TestRef: invGotcha}, {TestRef: "quiet"}}},
		Status:     kubemootv1alpha1.CrewFitnessSuiteStatus{RunID: "r1", Phase: kubemootv1alpha1.CrewFitnessSuitePhaseCompleted},
	}
	suite.Status.Scenarios = scenarioResults(suite.Spec.Scripts, nil)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(suite).WithStatusSubresource(suite).Build()
	flagged := []byte(`{"events":[{"type":"synthesis","content":"x"}],"inventedNames":["homelab-k8s-1"]}`)
	clean := []byte(`{"events":[{"type":"synthesis","content":"x"}]}`)
	store := fakeStore{objs: map[string][]byte{invFlaggedKey: flagged, "ns/s/r1/s1-i1.json": clean}}
	r := &CrewFitnessSuiteReconciler{Client: cli, Scheme: scheme, artifacts: store}

	_, ok, err := r.persistArtifact(context.Background(), suite, nil, nil, "write failed")
	if err != nil || !ok {
		t.Fatalf("persistArtifact ok=%v err=%v", ok, err)
	}
	var got kubemootv1alpha1.CrewFitnessSuite
	if err := cli.Get(context.Background(), client.ObjectKeyFromObject(suite), &got); err != nil {
		t.Fatal(err)
	}
	rows := got.Status.Scenarios
	if len(rows) != 2 || rows[0].InventedNameRuns != 1 || rows[1].InventedNameRuns != 0 {
		t.Errorf("stored status.scenarios = %+v, want inventedNameRuns 1 and 0", rows)
	}
	if got.Status.ArtifactRef == nil {
		t.Error("the artifact ref is written in the same update")
	}
}
