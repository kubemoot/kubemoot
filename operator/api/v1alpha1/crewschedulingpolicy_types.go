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

// CrewSchedulingPolicySpec defines the require/prefer rules the Kubemoot
// scheduler uses to match each discussion phase to a (Model, Provider) binding.
//
// Modeled on Kubernetes Pod scheduling: `require` is hard (no match →
// Unschedulable), `prefer` is soft weighted hints. See kubemoot/docs/scheduler-v2.md.
type CrewSchedulingPolicySpec struct {
	// CrewRef is the name of the Crew this policy applies to.
	// +kubebuilder:validation:Required
	CrewRef string `json:"crewRef"`

	// ArchetypeRef is the name of the MootArchetype that defines the phase
	// vocabulary used by Rules[].Phase. If unset, defaults to "consent-3".
	// +optional
	// +kubebuilder:default=consent-3
	ArchetypeRef string `json:"archetypeRef,omitempty"`

	// Rules is the ordered list of per-phase scheduling rules.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Rules []SchedulingRule `json:"rules"`

	// TopologySpread distributes phase bindings across providers to avoid
	// stacking phases on the same GPU when alternatives exist. Mirrors
	// Pod TopologySpreadConstraints.
	// +optional
	TopologySpread *TopologySpread `json:"topologySpread,omitempty"`

	// QualityBias is a per-capability scalar in [0.0, 1.0] biasing model
	// selection between speed (low) and quality (high) for SchedulingRules
	// that have no explicit Prefer block. For each Agent, the scheduler
	// computes effectiveBias = max(QualityBias[c]) across the agent's
	// declared spec.capabilities, falling back to QualityBias["default"]
	// when no capability is mapped. The effective bias compiles into a
	// per-Model score against the Model's "latencyClass" label:
	// high → bias*100, medium → 50, low → (1-bias)*100. Models without
	// a latencyClass label receive no bias contribution.
	//
	// Capability declarations are preference-shaping, not hard
	// requirements — agents always schedule against the best available
	// Model. Explicit Prefer blocks on a SchedulingRule override the
	// auto-derivation for that rule.
	//
	// String values avoid the CRD float-precision foot-gun. Values that
	// don't parse as floats in [0.0, 1.0] are ignored. See
	// kubemoot/docs/scheduler.md "Quality bias" section.
	// +optional
	QualityBias map[string]string `json:"qualityBias,omitempty"`
}

// SchedulingRule is the rule for a single discussion phase. Phase strings must
// match the vocabulary of the referenced MootArchetype.
type SchedulingRule struct {
	// Phase is the discussion phase this rule applies to (e.g., "mulling",
	// "triage", "synthesis" for consent-3; "motion", "debate", "vote" for
	// Robert's Rules archetype). Open string set — validated against the
	// active MootArchetype's phase vocabulary at admission time.
	// +kubebuilder:validation:Required
	Phase string `json:"phase"`

	// Require is the hard constraint a Model must satisfy to be feasible
	// for this phase. If no Model matches, the phase is Unschedulable and
	// a FailedScheduling event is emitted on the policy.
	// +optional
	Require *metav1.LabelSelector `json:"require,omitempty"`

	// Prefer is the ordered list of soft scheduling preferences. Each entry
	// adds Weight to the score of any candidate Model whose labels match.
	// +optional
	Prefer []PreferenceTerm `json:"prefer,omitempty"`
}

// PreferenceTerm is a single weighted preference. The scheduler adds Weight to
// a Model candidate's score if its labels match Selector.
type PreferenceTerm struct {
	// Weight to add to candidates that match Selector. Higher = more preferred.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:validation:Required
	Weight int32 `json:"weight"`

	// Selector matches Model labels. The Kubernetes LabelSelector idiom —
	// supports matchLabels and matchExpressions.
	// +kubebuilder:validation:Required
	Selector *metav1.LabelSelector `json:"selector"`
}

// TopologySpread distributes phase bindings across a topology key, mirroring
// Pod TopologySpreadConstraints. Used to avoid stacking multiple phases of the
// same discussion on the same provider when alternatives are feasible.
type TopologySpread struct {
	// TopologyKey identifies the dimension to spread across (e.g., "provider"
	// for "don't put both phases on the same GPU"). The scheduler interprets
	// this against ModelProvider names by default.
	// +kubebuilder:validation:Required
	// +kubebuilder:default=provider
	TopologyKey string `json:"topologyKey"`

	// PhaseKeys is the set of phase names that participate in spreading.
	// Bindings for these phases will be distributed across distinct
	// TopologyKey values when possible.
	// +kubebuilder:validation:MinItems=2
	PhaseKeys []string `json:"phaseKeys"`

	// WhenUnsatisfiable controls behavior when the spread cannot be honored.
	// DoNotSchedule emits Unschedulable; ScheduleAnyway falls back to the
	// highest-scoring binding even if the spread is violated.
	// +kubebuilder:validation:Enum=DoNotSchedule;ScheduleAnyway
	// +kubebuilder:default=ScheduleAnyway
	WhenUnsatisfiable string `json:"whenUnsatisfiable,omitempty"`
}

// CrewSchedulingPolicyStatus reflects the observed state of the policy.
type CrewSchedulingPolicyStatus struct {
	// LastValidated is the timestamp of the most recent successful validation
	// of this policy against the active MootArchetype's phase vocabulary.
	// +optional
	LastValidated *metav1.Time `json:"lastValidated,omitempty"`

	// ValidationError, when non-empty, describes why the policy could not be
	// applied (e.g., references a phase not in the archetype vocabulary).
	// +optional
	ValidationError string `json:"validationError,omitempty"`

	// PhaseStatus reports the most recent scheduling outcome per phase.
	// Populated by the scheduler on each binding decision.
	// +optional
	PhaseStatus []PhaseStatus `json:"phaseStatus,omitempty"`

	// Conditions represent the latest available observations of the policy.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// PhaseStatus captures the most recent scheduling outcome for one phase.
type PhaseStatus struct {
	// Phase name (matches a Rule's Phase).
	Phase string `json:"phase"`

	// State is the most recent scheduling outcome: Scheduled or Unschedulable.
	// +kubebuilder:validation:Enum=Scheduled;Unschedulable
	State string `json:"state"`

	// Reason is a short machine-readable token (Scheduled, NoFeasibleModel,
	// NoFeasibleProvider, FilteredByCapacity).
	// +optional
	Reason string `json:"reason,omitempty"`

	// Message is the human-readable explanation, especially useful for
	// Unschedulable outcomes ("no Model matches require selectors {...}").
	// +optional
	Message string `json:"message,omitempty"`

	// ObservedAt is the timestamp of the most recent scheduler decision for
	// this phase.
	// +optional
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=csp
// +kubebuilder:printcolumn:name="Crew",type=string,JSONPath=`.spec.crewRef`
// +kubebuilder:printcolumn:name="Archetype",type=string,JSONPath=`.spec.archetypeRef`
// +kubebuilder:printcolumn:name="Rules",type=integer,JSONPath=`.spec.rules[*].phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// CrewSchedulingPolicy expresses how the Kubemoot scheduler should bind each
// discussion phase to a (Model, Provider) for a given Crew. Pure Kubernetes
// affinity idiom: require/prefer with LabelSelector matching Model labels.
//
// See kubemoot/docs/scheduler-v2.md for the full design.
type CrewSchedulingPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CrewSchedulingPolicySpec   `json:"spec,omitempty"`
	Status CrewSchedulingPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CrewSchedulingPolicyList contains a list of CrewSchedulingPolicy.
type CrewSchedulingPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CrewSchedulingPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CrewSchedulingPolicy{}, &CrewSchedulingPolicyList{})
}
