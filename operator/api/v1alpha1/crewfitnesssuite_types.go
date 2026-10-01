/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CrewFitnessSuiteSpec defines a fitness suite — a set of scripts to run
// N times against a crew. The reconciler creates one CrewFitness CR per
// (script × iteration) pair serially (or up to spec.concurrency in
// parallel), harvests their results, and aggregates into an XLSX artifact
// written to the NATS Object Store bucket {@code kubemoot_fitness_artifacts}.
// +kubebuilder:validation:XValidation:rule="has(self.rejudge) == has(oldSelf.rejudge) && (!has(self.rejudge) || self.rejudge == oldSelf.rejudge)",message="spec.rejudge is immutable: set it when the suite is created"
type CrewFitnessSuiteSpec struct {
	// CrewRef references the Crew CR in the same namespace that the
	// suite runs against. Required.
	// +kubebuilder:validation:Required
	CrewRef string `json:"crewRef"`

	// Description is a human-readable statement of the suite's purpose
	// and scope (e.g. "26-scenario READ-query baseline across all four
	// homelab layers"). Surfaced on the dashboard suite row/detail and
	// as a header line in the XLSX Overview tab. Optional.
	// +optional
	Description string `json:"description,omitempty"`

	// Iterations is how many times each script is executed. Total
	// CrewFitness CRs created is len(Scripts) × Iterations. Required.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	Iterations int32 `json:"iterations"`

	// Scripts is the ordered list of fitness scripts to execute.
	// Each entry mirrors the CrewFitness spec's testRef/testContent/
	// configMapRef triad — exactly one source must be set per entry.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Scripts []SuiteScript `json:"scripts"`

	// Concurrency is the max number of CrewFitness CRs the reconciler
	// runs in parallel. Default 1 (strictly serial). Keep at 1 for
	// baseline measurements — concurrent runs share the same crew and
	// the same Ollama providers, so parallel iterations measure
	// contention more than scenario behaviour.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	Concurrency int32 `json:"concurrency,omitempty"`

	// PerIterationTimeout caps the wall-clock for a single iteration's
	// CrewFitness CR. If an iteration doesn't reach a terminal phase
	// within this duration the reconciler records an error result for
	// that iteration and continues. Default 10m.
	// +optional
	// +kubebuilder:default="10m"
	PerIterationTimeout *metav1.Duration `json:"perIterationTimeout,omitempty"`

	// ArtifactRetention is the NATS Object Store TTL for the produced
	// XLSX. After this duration the artifact is auto-deleted from the
	// bucket. The CrewFitnessSuite CR's status.artifactRef will still
	// reference the (now-missing) object; downloads will 404 cleanly.
	// Default 168h (7 days).
	// +optional
	// +kubebuilder:default="168h"
	ArtifactRetention *metav1.Duration `json:"artifactRetention,omitempty"`

	// PurgeMemory clears the target crew's working memory once before the run so
	// the suite starts from a clean state (results aren't biased by facts the crew
	// learned in earlier runs). Defaults to true.
	// +optional
	// +kubebuilder:default=true
	PurgeMemory *bool `json:"purgeMemory,omitempty"`

	// Suspend pauses the suite between iterations. While true, no new
	// iteration starts; any iteration already running finishes and its result
	// is kept. The phase becomes Paused once nothing is in flight. Setting it
	// back to false resumes the suite at the next unscheduled iteration.
	// Ignored once the suite is terminal. Default false.
	// +optional
	// +kubebuilder:default=false
	Suspend bool `json:"suspend,omitempty"`

	// Cancel stops the suite. When true, the operator deletes any iteration
	// still in flight (its Job follows through owner refs), starts no new
	// iteration, and moves the suite to the terminal Cancelled phase with
	// completedAt set. Results of completed iterations are kept and written
	// to the XLSX, which is marked partial; the deferred judge is skipped.
	// Cancel takes precedence over Suspend and cannot be undone. Ignored once
	// the suite is terminal. Default false.
	// +optional
	// +kubebuilder:default=false
	Cancel bool `json:"cancel,omitempty"`

	// Rejudge makes this suite a judge-only pass over an earlier run. When set,
	// the suite runs no iterations and asks the crew nothing: it copies the
	// source run's per-scenario transcripts, re-evaluates their assertions
	// against THIS suite's scripts (matched by testRef), and the deferred judge
	// scores the saved answers against this suite's references. Iterations,
	// concurrency, purgeMemory and suspend do not apply. Scenarios of this suite
	// with no transcript in the source are listed in status.rejudge.notInSource
	// and not scored; source scenarios missing from this suite are ignored.
	// Optional; set at creation and immutable afterwards.
	// +optional
	Rejudge *RejudgeSource `json:"rejudge,omitempty"`
}

// RejudgeSource names the earlier run a re-judge suite scores again.
type RejudgeSource struct {
	// Suite is the source CrewFitnessSuite, in the same namespace. It must be
	// Completed and must still exist: deleting a suite purges its transcripts.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Suite string `json:"suite"`

	// RunID is the source run to re-judge; it must equal the source suite's
	// status.runId.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	RunID string `json:"runId"`
}

// RejudgeStatus reports what a re-judge suite took from its source run.
type RejudgeStatus struct {
	// Source is the run that was re-judged.
	Source RejudgeSource `json:"source"`

	// Transcripts is the number of source transcripts copied into this run.
	Transcripts int32 `json:"transcripts"`

	// NotInSource lists this suite's scenarios (testRef) that have no
	// transcript in the source run. They are not scored.
	// +optional
	NotInSource []string `json:"notInSource,omitempty"`
}

// SuiteScript is one entry in a suite — mirrors the CrewFitness spec's
// test source fields. Exactly one of TestContent or ConfigMapRef must be
// set; TestRef is the friendly name used as a column value and as part
// of the per-iteration CrewFitness CR's name.
type SuiteScript struct {
	// TestRef is the script's friendly name (e.g. "gpu-utilization-live").
	// Used as a column value in the XLSX and as part of the per-iteration
	// CrewFitness CR's name.
	// +kubebuilder:validation:Required
	TestRef string `json:"testRef"`

	// TestContent is the raw ADL script. When set, the per-iteration
	// CrewFitness CR is created with this content inline. Mutually
	// exclusive with ConfigMapRef.
	// +optional
	TestContent string `json:"testContent,omitempty"`

	// ConfigMapRef references an existing ConfigMap whose data key
	// {@code <TestRef>.adl} (or matching key) holds the script. Mutually
	// exclusive with TestContent.
	// +optional
	ConfigMapRef string `json:"configMapRef,omitempty"`
}

// CrewFitnessSuitePhase is the lifecycle phase of a suite run.
type CrewFitnessSuitePhase string

const (
	CrewFitnessSuitePhasePending   CrewFitnessSuitePhase = "Pending"
	CrewFitnessSuitePhaseRunning   CrewFitnessSuitePhase = "Running"
	CrewFitnessSuitePhaseCompleted CrewFitnessSuitePhase = "Completed"
	CrewFitnessSuitePhaseFailed    CrewFitnessSuitePhase = "Failed"
	CrewFitnessSuitePhaseError     CrewFitnessSuitePhase = "Error"
	// CrewFitnessSuitePhasePaused: spec.suspend is true and no iteration is
	// in flight. Not terminal; clearing spec.suspend returns to Running.
	CrewFitnessSuitePhasePaused CrewFitnessSuitePhase = "Paused"
	// CrewFitnessSuitePhaseCancelled: spec.cancel stopped the suite before
	// every iteration ran. Terminal; the XLSX holds the completed iterations
	// and is marked partial.
	CrewFitnessSuitePhaseCancelled CrewFitnessSuitePhase = "Cancelled"
)

// SuiteArtifactRef points to the XLSX object the reconciler wrote to the
// NATS Object Store on suite completion.
type SuiteArtifactRef struct {
	// Bucket is the NATS Object Store bucket. Always
	// {@code kubemoot_fitness_artifacts} in v1.
	Bucket string `json:"bucket"`

	// ObjectKey is the bucket-relative key:
	// {@code <namespace>/<suite-name>/<runId>.xlsx}.
	ObjectKey string `json:"objectKey"`

	// SizeBytes of the XLSX object. Reported by the operator for
	// dashboard pre-fetch sizing; not load-bearing.
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`
}

// CrewFitnessSuiteStatus reflects the observed state of a suite run.
type CrewFitnessSuiteStatus struct {
	// Phase is the lifecycle phase.
	Phase CrewFitnessSuitePhase `json:"phase,omitempty"`

	// RunID identifies this suite execution; set on transition to
	// Running. Used as the trailing path segment of the artifact key.
	// +optional
	RunID string `json:"runId,omitempty"`

	// StartedAt records when the reconciler began iteration scheduling.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt records when the last iteration reached a terminal
	// phase (Passed/Failed/Error), or when the suite was cancelled.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// IterationsTotal is len(spec.scripts) × spec.iterations.
	IterationsTotal int32 `json:"iterationsTotal,omitempty"`

	// IterationsCompleted is the number of per-iteration CrewFitness
	// CRs that have reached a terminal phase (any outcome). Increments
	// monotonically up to IterationsTotal.
	IterationsCompleted int32 `json:"iterationsCompleted,omitempty"`

	// Passed / Failed / Errored break down IterationsCompleted by
	// outcome.
	Passed  int32 `json:"passed,omitempty"`
	Failed  int32 `json:"failed,omitempty"`
	Errored int32 `json:"errored,omitempty"`

	// ArtifactRef is set when the XLSX has been written to the NATS
	// Object Store. Populated only on terminal phase
	// (Completed/Failed/Cancelled).
	// +optional
	ArtifactRef *SuiteArtifactRef `json:"artifactRef,omitempty"`

	// Error provides details when Phase is Error.
	// +optional
	Error string `json:"error,omitempty"`

	// Rejudge is set on a re-judge suite (spec.rejudge) once its source
	// transcripts are copied.
	// +optional
	Rejudge *RejudgeStatus `json:"rejudge,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cfs
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Crew",type=string,JSONPath=`.spec.crewRef`
// +kubebuilder:printcolumn:name="Progress",type=string,JSONPath=`.status.iterationsCompleted`
// +kubebuilder:printcolumn:name="Total",type=integer,JSONPath=`.status.iterationsTotal`
// +kubebuilder:printcolumn:name="Passed",type=integer,JSONPath=`.status.passed`
// +kubebuilder:printcolumn:name="Failed",type=integer,JSONPath=`.status.failed`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// CrewFitnessSuite drives an N-iteration sweep of a fitness script set
// against a crew, aggregates per-iteration results, and persists an
// XLSX artifact to the NATS Object Store. The artifact is downloaded
// from the kubemoot dashboard.
type CrewFitnessSuite struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CrewFitnessSuiteSpec   `json:"spec,omitempty"`
	Status CrewFitnessSuiteStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CrewFitnessSuiteList contains a list of CrewFitnessSuite.
type CrewFitnessSuiteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CrewFitnessSuite `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CrewFitnessSuite{}, &CrewFitnessSuiteList{})
}
