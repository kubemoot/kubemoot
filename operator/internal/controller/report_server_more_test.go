/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestConsistencySidecarKey(t *testing.T) {
	if got := consistencySidecarKey("ns/s/r/"); got != "ns/s/r/consistency-v1.json" {
		t.Errorf("consistencySidecarKey = %q", got)
	}
}

func TestCoversScenarios(t *testing.T) {
	cached := map[string]float64{"a": 1, "b": 2}
	if !coversScenarios(cached, map[string][]string{"a": {"x"}, "b": {"y"}}) {
		t.Error("a cache covering every scenario should report true")
	}
	if coversScenarios(cached, map[string][]string{"a": {"x"}, "c": {"z"}}) {
		t.Error("a scenario missing from the cache should report false")
	}
}

// GenerateSuiteReport reads the suite + its transcripts and renders an XLSX. With
// no embedder Model present, the consistency pass is a no-op; the report still
// builds. Covers GenerateSuiteReport, readTranscriptResults, and the consistency/
// quality wiring.
func TestGenerateSuiteReport(t *testing.T) {
	scheme := agentReconcileScheme(t)
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: testSuite, Namespace: "ns"},
		Spec:       kubemootv1alpha1.CrewFitnessSuiteSpec{Scripts: []kubemootv1alpha1.SuiteScript{{TestRef: testScenarioX}}},
		Status:     kubemootv1alpha1.CrewFitnessSuiteStatus{RunID: "run"},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(suite).Build()
	store := fakeStore{objs: map[string][]byte{
		testRunS0I1: []byte(feasibleTranscript),
	}}
	out, err := GenerateSuiteReport(context.Background(), cli, store, "ns", testSuite)
	if err != nil {
		t.Fatalf("GenerateSuiteReport: %v", err)
	}
	if len(out) == 0 {
		t.Error("expected non-empty XLSX bytes")
	}
}

func TestGenerateSuiteReport_SuiteNotFound(t *testing.T) {
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	if _, err := GenerateSuiteReport(context.Background(), cli,
		fakeStore{objs: map[string][]byte{}}, "ns", testAbsent); err == nil {
		t.Error("expected an error when the suite does not exist")
	}
}
