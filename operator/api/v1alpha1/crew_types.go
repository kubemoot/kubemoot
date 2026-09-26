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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DiscussionConfig configures the crew's discussion gateway — the HTTP entry
// point for external clients to start discussions and stream agent signals.
type DiscussionConfig struct {
	// Enabled controls whether a discussion gateway is deployed for this crew
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Resources defines resource requirements for the gateway pod
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

// CrewSpec defines the desired state of Crew
type CrewSpec struct {
	// Description is a human-readable description of the crew's purpose
	// +optional
	Description string `json:"description,omitempty"`

	// Discussion configures the crew's discussion gateway
	// +optional
	Discussion *DiscussionConfig `json:"discussion,omitempty"`

	// Memory configures the crew's working memory — facts the crew learns at
	// runtime (e.g. discovered label/topology mappings) and recalls later.
	// +optional
	Memory *CrewMemoryConfig `json:"memory,omitempty"`
}

// CrewMemoryConfig governs a crew's working-memory storage, injection, and GC.
// (Distinct from the Agent's MemoryConfig, which is per-agent conversation
// memory.) Storage is cheap (NATS KV); injection into agent context is
// expensive (tokens + tool-calling degradation) — hence separate, generous
// storage caps vs a small injection limit. Defaults suit a typical crew.
type CrewMemoryConfig struct {
	// Enabled turns crew working memory on. When false, agents neither recall
	// nor persist facts. Pointer so an explicit false is distinguishable from
	// unset (a plain bool + omitempty drops false and can't disable).
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// MaxFacts is the per-crew storage cap. On write, least-recently-used facts
	// are evicted above this. Generous — facts are tiny and memory is cheap.
	// +kubebuilder:default=5000
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxFacts int32 `json:"maxFacts,omitempty"`

	// TTLDays is the age backstop: a fact not used/refreshed within this window
	// expires. Used facts are touched on recall, so they survive.
	// +kubebuilder:default=365
	// +kubebuilder:validation:Minimum=1
	// +optional
	TTLDays int32 `json:"ttlDays,omitempty"`

	// InjectLimit caps how many facts are injected into an agent's context per
	// query. Small and SEPARATE from MaxFacts — large context degrades
	// tool-calling. Only domain+query-relevant facts are injected, up to this.
	// +kubebuilder:default=8
	// +kubebuilder:validation:Minimum=0
	// +optional
	InjectLimit int32 `json:"injectLimit,omitempty"`

	// VerifyOnAdd vets a new fact against existing memory before storing —
	// skips duplicates and supersedes conflicts (same-key) rather than blindly
	// accumulating. When false, REMEMBER writes straight through. Pointer so an
	// explicit false is distinguishable from unset.
	// +kubebuilder:default=true
	// +optional
	VerifyOnAdd *bool `json:"verifyOnAdd,omitempty"`
}

// CrewStatus defines the observed state of Crew
type CrewStatus struct {
	// Phase is the crew infrastructure lifecycle (Pending, Deploying, Ready, Error)
	Phase string `json:"phase,omitempty"`

	// Ready indicates if the crew is operational and can accept questions
	Ready bool `json:"ready,omitempty"`

	// DiscussionEndpoint is the HTTP endpoint for the discussion gateway
	// Clients POST here to start discussions and GET SSE streams
	// +optional
	DiscussionEndpoint string `json:"discussionEndpoint,omitempty"`

	// CoordinatorRef is the name of the coordinator Agent for this crew
	// +optional
	CoordinatorRef string `json:"coordinatorRef,omitempty"`

	// AgentCount is the number of agents that belong to this crew
	AgentCount int32 `json:"agentCount,omitempty"`

	// Message provides additional status information
	Message string `json:"message,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=crew
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Coordinator",type=string,JSONPath=`.status.coordinatorRef`
// +kubebuilder:printcolumn:name="Agents",type=integer,JSONPath=`.status.agentCount`
// +kubebuilder:printcolumn:name="Discussion",type=string,JSONPath=`.status.discussionEndpoint`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Crew is the Schema for the crews API
// A Crew groups a coordinator and tooler agents into a named team.
// It optionally deploys a discussion gateway as the crew's HTTP entry point
// for external clients (CrewForge, Homelab Pilot, curl).
type Crew struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CrewSpec   `json:"spec,omitempty"`
	Status CrewStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CrewList contains a list of Crew
type CrewList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Crew `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Crew{}, &CrewList{})
}
