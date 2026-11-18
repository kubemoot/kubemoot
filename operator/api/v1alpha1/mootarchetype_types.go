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

// MootArchetypeSpec defines a coordination archetype — the phase vocabulary
// and state machine that a Crew's coordinator uses to run discussions.
//
// Today's default archetype is "consent-3" (sociocracy 3.0): phases
// triaging → evaluating → synthesis. Future archetypes (Robert's Rules,
// debate, expert panel, devil's advocate) each define their own vocabulary
// without changing the scheduler.
//
// CrewSchedulingPolicy.spec.rules[].phase is validated against the active
// archetype's Phases at admission time.
type MootArchetypeSpec struct {
	// Description is the human-readable explanation of this archetype.
	// +optional
	Description string `json:"description,omitempty"`

	// Phases is the ordered set of phase definitions emitted by a coordinator
	// running this archetype.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Phases []ArchetypePhase `json:"phases"`

	// Signals is the set of signal types agents may emit during a discussion
	// (e.g., agree, concern, stand_aside, block, advisory). Open string set
	// — informational. The coordinator implementation interprets these.
	// +optional
	Signals []string `json:"signals,omitempty"`

	// Review declares which analysts the coordinator convenes for the review
	// phase. Optional; absent means every analyst reviews.
	// +optional
	Review *ArchetypeReview `json:"review,omitempty"`

	// StateMachine declares the legal phase transitions. Optional; absence
	// implies sequential progression through Phases in declaration order.
	// +optional
	StateMachine *ArchetypeStateMachine `json:"stateMachine,omitempty"`
}

// ArchetypePhase declares one phase by name plus optional metadata.
type ArchetypePhase struct {
	// Name of the phase as emitted by the coordinator and referenced from
	// CrewSchedulingPolicy.spec.rules[].phase.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Role categorizes what the phase accomplishes (gatekeeping, deliberation,
	// closure, vote, …). Informational; the scheduler does not interpret it.
	// +optional
	Role string `json:"role,omitempty"`

	// Description is a one-line explanation shown in dashboards.
	// +optional
	Description string `json:"description,omitempty"`
}

// ReviewConvene says which analysts review a discussion.
// +kubebuilder:validation:Enum=all;domain
type ReviewConvene string

// ReviewFallback says who reviews when no analyst matches by domain.
// +kubebuilder:validation:Enum=primary;all
type ReviewFallback string

const (
	// ReviewConveneAll convenes every analyst of the crew.
	ReviewConveneAll ReviewConvene = "all"
	// ReviewConveneDomain convenes only the analysts whose declared domains
	// touch the question or the data the toolers gathered.
	ReviewConveneDomain ReviewConvene = "domain"
	// ReviewFallbackPrimary convenes the single analyst that best matches.
	ReviewFallbackPrimary ReviewFallback = "primary"
	// ReviewFallbackAll convenes every analyst.
	ReviewFallbackAll ReviewFallback = "all"
)

// ArchetypeReview declares who reviews. The review phase never runs with no
// reviewer while the crew has an analyst: the fallback names who is convened
// when no analyst matches.
type ArchetypeReview struct {
	// Convene is "all" (every analyst reviews) or "domain" (only analysts whose
	// declared domains touch the question or the gathered data).
	// +kubebuilder:default=all
	// +optional
	Convene ReviewConvene `json:"convene,omitempty"`

	// Fallback applies when Convene is "domain" and no analyst matches: "primary"
	// convenes the best-matching analyst alone, "all" convenes every analyst.
	// +kubebuilder:default=primary
	// +optional
	Fallback ReviewFallback `json:"fallback,omitempty"`
}

// ArchetypeStateMachine declares the legal phase transitions.
type ArchetypeStateMachine struct {
	// Initial is the name of the starting phase.
	// +kubebuilder:validation:Required
	Initial string `json:"initial"`

	// Transitions are the legal edges of the state machine.
	// +kubebuilder:validation:MinItems=1
	Transitions []PhaseTransition `json:"transitions"`
}

// PhaseTransition is one edge in the state machine.
type PhaseTransition struct {
	// From phase name.
	// +kubebuilder:validation:Required
	From string `json:"from"`

	// To phase name.
	// +kubebuilder:validation:Required
	To string `json:"to"`

	// On is the trigger condition (e.g., "all-triaged", "settled",
	// "majority-vote"). Informational; the coordinator interprets it.
	// +optional
	On string `json:"on,omitempty"`
}

// MootArchetypeStatus reflects validation state.
type MootArchetypeStatus struct {
	// Valid is true when the spec parses cleanly (Phases referenced by
	// StateMachine edges all exist, no cycles in sequential interpretation).
	// +optional
	Valid bool `json:"valid,omitempty"`

	// Message provides additional status information.
	// +optional
	Message string `json:"message,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=moot
// +kubebuilder:printcolumn:name="Phases",type=string,JSONPath=`.spec.phases[*].name`
// +kubebuilder:printcolumn:name="Valid",type=boolean,JSONPath=`.status.valid`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MootArchetype declares a coordination archetype's phase vocabulary and
// state machine. Cluster-scoped — one MootArchetype CR can be referenced
// by many CrewSchedulingPolicy CRs across namespaces.
//
// See kubemoot/docs/scheduler-v2.md and kubemoot/docs/agentic-consensus.md
// (the consent-3 archetype's semantics).
type MootArchetype struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MootArchetypeSpec   `json:"spec,omitempty"`
	Status MootArchetypeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MootArchetypeList contains a list of MootArchetype.
type MootArchetypeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MootArchetype `json:"items"`
}

func init() {
	registerTypes(&MootArchetype{}, &MootArchetypeList{})
}
