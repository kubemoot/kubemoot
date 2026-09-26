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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CrewFitnessSpec defines the desired state of CrewFitness
type CrewFitnessSpec struct {
	// CrewRef references the Crew CR in the same namespace
	// +kubebuilder:validation:Required
	CrewRef string `json:"crewRef"`

	// TestRef is the key in the ConfigMap (without .adl extension)
	// +kubebuilder:validation:Required
	TestRef string `json:"testRef"`

	// ConfigMapRef is the name of an existing ConfigMap containing ADL test definitions.
	// When empty, the operator creates a ConfigMap from TestContent.
	// +optional
	ConfigMapRef string `json:"configMapRef,omitempty"`

	// TestContent is the raw ADL test content. When set, the operator creates
	// a ConfigMap owned by this CR. Mutually exclusive with ConfigMapRef.
	// +optional
	TestContent string `json:"testContent,omitempty"`

	// TTL is the duration after completion before auto-deletion (e.g., "24h", "1h30m")
	// When unset, the CrewFitness CR is not auto-deleted.
	// +optional
	TTL *metav1.Duration `json:"ttl,omitempty"`
}

// AssertionResult captures the outcome of a single ADL assertion
type AssertionResult struct {
	// Raw is the original ASSERT(...) text
	Raw string `json:"raw"`

	// Passed indicates whether the assertion succeeded
	Passed bool `json:"passed"`

	// Message explains the result
	Message string `json:"message"`
}

// CrewFitnessPhase represents the lifecycle phase of a fitness test
type CrewFitnessPhase string

const (
	CrewFitnessPhasePending CrewFitnessPhase = "Pending"
	CrewFitnessPhaseRunning CrewFitnessPhase = "Running"
	CrewFitnessPhasePassed  CrewFitnessPhase = "Passed"
	CrewFitnessPhaseFailed  CrewFitnessPhase = "Failed"
	CrewFitnessPhaseError   CrewFitnessPhase = "Error"
)

// CrewFitnessStatus defines the observed state of CrewFitness
type CrewFitnessStatus struct {
	// Phase is the current lifecycle phase
	Phase CrewFitnessPhase `json:"phase,omitempty"`

	// StartedAt records when the fitness test Job was created
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt records when the fitness test Job finished
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// DurationMs is the wall-clock duration of the test in milliseconds
	DurationMs int64 `json:"durationMs,omitempty"`

	// JobRef is the name of the Kubernetes Job running the test
	// +optional
	JobRef string `json:"jobRef,omitempty"`

	// Assertions contains per-assertion pass/fail results
	// +optional
	Assertions []AssertionResult `json:"assertions,omitempty"`

	// Error provides details when Phase is Error
	// +optional
	Error string `json:"error,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cf
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Crew",type=string,JSONPath=`.spec.crewRef`
// +kubebuilder:printcolumn:name="Test",type=string,JSONPath=`.spec.testRef`
// +kubebuilder:printcolumn:name="Duration",type=integer,JSONPath=`.status.durationMs`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// CrewFitness is the Schema for the crewfitnesses API.
// A CrewFitness runs a single ADL fitness test against a deployed Crew's
// discussion endpoint. The operator creates a Job that POSTs a question,
// streams SSE signals, evaluates assertions, and reports results in status.
type CrewFitness struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CrewFitnessSpec   `json:"spec,omitempty"`
	Status CrewFitnessStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CrewFitnessList contains a list of CrewFitness
type CrewFitnessList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CrewFitness `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CrewFitness{}, &CrewFitnessList{})
}
