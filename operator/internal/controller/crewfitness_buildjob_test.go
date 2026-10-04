/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// A fitness run whose crew endpoint is not ready yet should WAIT (the crew is
// still coming up), not error - until it has waited past the deadline, after
// which it gives up. See [[Fitness Suite Errors Instantly Against a Not-Ready Crew]].
func TestShouldWaitForEndpoint(t *testing.T) {
	now := time.Now()
	if !shouldWaitForEndpoint(now.Add(-10*time.Second), now) {
		t.Error("a run 10s old should still wait for the crew endpoint")
	}
	if !shouldWaitForEndpoint(now, now) {
		t.Error("a just-created run should wait for the crew endpoint")
	}
	if shouldWaitForEndpoint(now.Add(-endpointWaitDeadline-time.Second), now) {
		t.Error("a run past the deadline should give up, not wait forever")
	}
}

// The fitness-runner Job pods are the test harness itself. They must carry the
// kubemoot.ai/fitness-harness label so crews can exclude them from "what is
// failing" assessments. See [[Fitness Harness Pod Contamination]].
func TestBuildJobCarriesFitnessHarnessLabel(t *testing.T) {
	r := &CrewFitnessReconciler{ConfigCache: NewConfigCache()}
	cf := &kubemootv1alpha1.CrewFitness{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-abc-s4-i1",
			Namespace: testCrewHomelabPilotNS,
		},
		Spec: kubemootv1alpha1.CrewFitnessSpec{CrewRef: "homelab-pilot-crew"},
	}

	// The crew prompts tell agents to filter on this exact label key; if the
	// constant drifts, the label and the prompt rule silently disagree.
	if labelFitnessHarness != "kubemoot.ai/fitness-harness" {
		t.Fatalf("labelFitnessHarness = %q, want \"kubemoot.ai/fitness-harness\" (must match the crew prompt filter)", labelFitnessHarness)
	}

	job := r.buildJob(cf, "cf-run-abc-s4-i1", "http://endpoint", "test.adl", "cm")

	if got := job.Labels[labelFitnessHarness]; got != testHarnessLabelValue {
		t.Errorf("Job label %q = %q, want \"true\"", labelFitnessHarness, got)
	}
	// The pod template label is what the agent's tooling can filter on; it must
	// be present there too, not only on the Job object.
	if got := job.Spec.Template.Labels[labelFitnessHarness]; got != testHarnessLabelValue {
		t.Errorf("pod template label %q = %q, want \"true\"", labelFitnessHarness, got)
	}
}

// A transient runner failure (the runner exits non-zero only when it could not
// obtain a crew answer) must get retried instead of recording a spurious zero,
// so the Job carries a non-zero backoffLimit. See [[Rollout-Safety Fitness Job
// Fails BackoffLimitExceeded]].
func TestBuildJobRetriesTransientFailures(t *testing.T) {
	r := &CrewFitnessReconciler{ConfigCache: NewConfigCache()}
	cf := &kubemootv1alpha1.CrewFitness{
		ObjectMeta: metav1.ObjectMeta{Name: "run-abc-s25-i1", Namespace: testCrewHomelabPilotNS},
		Spec:       kubemootv1alpha1.CrewFitnessSpec{CrewRef: "homelab-pilot-crew"},
	}

	job := r.buildJob(cf, "cf-run-abc-s25-i1", "http://endpoint", "test.adl", "cm")

	if job.Spec.BackoffLimit == nil {
		t.Fatal("Job BackoffLimit is nil, want a non-zero retry budget")
	}
	if got := *job.Spec.BackoffLimit; got != 2 {
		t.Errorf("Job BackoffLimit = %d, want 2 (retry transient failures, bounded)", got)
	}
}
