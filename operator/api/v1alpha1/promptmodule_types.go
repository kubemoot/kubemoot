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

// PromptModuleSpec defines a reusable prompt fragment that Agents compose through spec.prompt.promptRefs
type PromptModuleSpec struct {
	// Content is the prompt text
	Content string `json:"content"`

	// Order controls where this module appears when composed (lower = earlier)
	// +kubebuilder:default=100
	// +optional
	Order int32 `json:"order,omitempty"`
}

// PromptModuleStatus defines the observed state of PromptModule
type PromptModuleStatus struct {
	// Ready indicates the module is valid and available
	Ready bool `json:"ready,omitempty"`

	// ReferencedBy is reserved for the names of Agents using this module; the operator does not populate it today
	// +optional
	ReferencedBy []string `json:"referencedBy,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=pm
// +kubebuilder:printcolumn:name="Order",type=integer,JSONPath=`.spec.order`
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Refs",type=integer,JSONPath=`.status.referencedBy`,description="Number of agents referencing this module"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PromptModule is a reusable, named prompt fragment that Agents compose into their system prompt.
// Multiple Agents can reference the same PromptModule. When a PromptModule changes,
// all agents that reference it are re-reconciled to update their system prompts.
type PromptModule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PromptModuleSpec   `json:"spec,omitempty"`
	Status PromptModuleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PromptModuleList contains a list of PromptModule
type PromptModuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromptModule `json:"items"`
}

func init() {
	registerTypes(&PromptModule{}, &PromptModuleList{})
}
