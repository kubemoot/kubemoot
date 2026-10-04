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

// SkillSpec defines the desired state of a Skill.
// A Skill is a standalone, crew-level packaged procedure that the coordinator
// selects on demand and injects into chosen agents just-in-time.
// Contrast with RAGSource (KNOW a fact) vs Skill (CARRY OUT a procedure).
type SkillSpec struct {
	// Description is the relevance trigger: a concise statement of what this
	// skill does and when it applies. The coordinator reads this to decide
	// whether to select the skill for a given discussion.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Description string `json:"description"`

	// Content is the ADL body of the skill: the WHEN/THEN/ASSERT procedure
	// that gets injected into an agent's context when the skill is selected.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Content string `json:"content"`

	// Order controls the position of this skill when multiple skills are
	// collected for a crew (lower = earlier). Used when sorting skills in the
	// per-crew ConfigMap and resume pool.
	// +kubebuilder:default=100
	// +optional
	Order int32 `json:"order,omitempty"`

	// RAGSources lists RAGSource references bundled with this skill.
	// Phase 2+ only: unused in Phase 1. Declared here so the CRD schema
	// is forward-compatible without a version bump.
	// +optional
	RAGSources []RAGSourceRef `json:"ragSources,omitempty"`

	// MCPServers lists MCPServer references bundled with this skill.
	// Phase 2+ only: unused in Phase 1. Declared here so the CRD schema
	// is forward-compatible without a version bump.
	// +optional
	MCPServers []MCPServerRef `json:"mcpServers,omitempty"`
}

// SkillStatus defines the observed state of a Skill.
type SkillStatus struct {
	// ObservedGeneration is the most recent generation reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the Skill.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=skill
// +kubebuilder:printcolumn:name="Order",type=integer,JSONPath=`.spec.order`
// +kubebuilder:printcolumn:name="Description",type=string,JSONPath=`.spec.description`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Skill is a standalone, crew-level packaged procedure. The coordinator selects
// relevant Skills on demand (using the resume pool) and injects their ADL
// content into chosen agents just-in-time for a discussion.
//
// Naming note: this is NOT the removed AgentPolicy.spec.a2a.skills[] concept
// (AgentSkill, an A2A capability card). Skill is a distinct top-level kind.
type Skill struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SkillSpec   `json:"spec,omitempty"`
	Status SkillStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SkillList contains a list of Skill.
type SkillList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Skill `json:"items"`
}

func init() {
	registerTypes(&Skill{}, &SkillList{})
}
