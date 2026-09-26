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

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func reapScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return s
}

func childCR(name, suite string) *kubemootv1alpha1.CrewFitness {
	return &kubemootv1alpha1.CrewFitness{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "crew-test",
			Labels:    map[string]string{suiteOwnerLabel: suite},
		},
	}
}

// TestReapChildrenDeletesOnlyOwnedChildren verifies reapChildren removes
// exactly the children owned by the suite (matched by suiteOwnerLabel) and
// leaves everything else alone — no over-deletion of another suite's
// children or unrelated CrewFitness CRs.
func TestReapChildrenDeletesOnlyOwnedChildren(t *testing.T) {
	scheme := reapScheme(t)
	ours1 := childCR("run-abc-s0-i1", "baseline")
	ours2 := childCR("run-abc-s0-i2", "baseline")
	other := childCR("run-xyz-s0-i1", "other-suite")    // different suite
	bare := &kubemootv1alpha1.CrewFitness{ObjectMeta: metav1.ObjectMeta{ // no suite label
		Name: "standalone", Namespace: "crew-test"}}

	cli := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(ours1, ours2, other, bare).Build()
	r := &CrewFitnessSuiteReconciler{Client: cli, Scheme: scheme}

	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "baseline", Namespace: "crew-test"},
	}
	if err := r.reapChildren(context.Background(), suite); err != nil {
		t.Fatalf("reapChildren: %v", err)
	}

	remaining := &kubemootv1alpha1.CrewFitnessList{}
	if err := cli.List(context.Background(), remaining); err != nil {
		t.Fatalf("list: %v", err)
	}
	got := map[string]bool{}
	for _, c := range remaining.Items {
		got[c.Name] = true
	}
	if got["run-abc-s0-i1"] || got["run-abc-s0-i2"] {
		t.Errorf("owned children should be reaped, still present: %v", got)
	}
	if !got["run-xyz-s0-i1"] {
		t.Errorf("other suite's child must survive")
	}
	if !got["standalone"] {
		t.Errorf("unlabelled CrewFitness must survive")
	}
}

// TestReapChildrenIdempotent confirms a second reap on an already-clean
// suite is a no-op (no error) — required because terminalUpkeep requeues
// and may call it again after the children are gone.
func TestReapChildrenIdempotent(t *testing.T) {
	scheme := reapScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &CrewFitnessSuiteReconciler{Client: cli, Scheme: scheme}
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "baseline", Namespace: "crew-test"},
	}
	if err := r.reapChildren(context.Background(), suite); err != nil {
		t.Fatalf("reap on empty must be a no-op, got: %v", err)
	}
}

// TestTerminalUpkeepActionReapGate pins the reap-gate decision, the core of
// the XLSX-regenerated-after-deferred-judge fix: children must NEVER be reaped
// while the deferred judge is still scoring (they are the only source of
// IterationResults for the score-embedded rebuild), and a provisional artifact
// is always written first so a stuck judge cannot strand the run artifact-less.
func TestTerminalUpkeepActionReapGate(t *testing.T) {
	cases := []struct {
		name          string
		hasArtifact   bool
		judgeComplete bool
		childCount    int
		want          terminalAction
	}{
		{"no artifact, children present -> write provisional", false, false, 5, actionWriteProvisional},
		{"no artifact, judge complete, children -> still just write", false, true, 5, actionWriteProvisional},
		{"no artifact, no children -> give up", false, true, 0, actionGiveUp},
		{"artifact, judge NOT complete -> WAIT (never reap mid-judge)", true, false, 5, actionWaitForJudge},
		{"artifact, judge NOT complete, no children -> still wait", true, false, 0, actionWaitForJudge},
		{"artifact, judge complete, children -> finalize + reap", true, true, 5, actionFinalizeAndReap},
		{"artifact, judge complete, no children -> done", true, true, 0, actionDone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := terminalUpkeepAction(c.hasArtifact, c.judgeComplete, c.childCount)
			if got != c.want {
				t.Errorf("terminalUpkeepAction(%v,%v,%d) = %d, want %d",
					c.hasArtifact, c.judgeComplete, c.childCount, got, c.want)
			}
		})
	}
}

// TestTerminalUpkeepActionNeverReapsWhileJudging is the explicit regression
// guard: for EVERY child count, an incomplete judge with an existing artifact
// must wait, never finalize+reap. A reap here would destroy the IterationResults
// before the score-embedded artifact is rebuilt (the original all-0-XLSX bug).
func TestTerminalUpkeepActionNeverReapsWhileJudging(t *testing.T) {
	for n := 0; n <= 400; n += 50 {
		if got := terminalUpkeepAction(true, false, n); got == actionFinalizeAndReap {
			t.Fatalf("childCount=%d: reaped while judge incomplete", n)
		}
	}
}

// TestTerminalUpkeepKeepsChildrenUntilArtifactWritten pins the safety
// ordering: when there is NO artifactRef yet and NATS is unconfigured
// (NATSPublisher nil → writeArtifact returns nil), terminalUpkeep must NOT
// reap — the children are the only copy of the results. This guards against
// destroying data when the artifact can't be written.
func TestTerminalUpkeepKeepsChildrenUntilArtifactWritten(t *testing.T) {
	scheme := reapScheme(t)
	ch := childCR("run-abc-s0-i1", "baseline")
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ch).Build()
	// NATSPublisher nil → writeArtifact yields (nil, nil) → no artifactRef.
	r := &CrewFitnessSuiteReconciler{Client: cli, Scheme: scheme}
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "baseline", Namespace: "crew-test"},
		Status:     kubemootv1alpha1.CrewFitnessSuiteStatus{Phase: kubemootv1alpha1.CrewFitnessSuitePhaseCompleted},
	}
	if _, err := r.terminalUpkeep(context.Background(), suite); err != nil {
		t.Fatalf("terminalUpkeep: %v", err)
	}
	remaining := &kubemootv1alpha1.CrewFitnessList{}
	_ = cli.List(context.Background(), remaining)
	if len(remaining.Items) != 1 {
		t.Errorf("child must be KEPT when no artifact was written (data safety); got %d remaining", len(remaining.Items))
	}
}
