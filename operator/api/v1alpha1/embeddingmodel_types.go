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

// EmbeddingModelSpec defines the desired state of EmbeddingModel
type EmbeddingModelSpec struct {
	// ProviderRef references the ModelProvider to use for this embedding model
	// +kubebuilder:validation:Required
	ProviderRef string `json:"providerRef"`

	// Model is the embedding model name (e.g., nomic-embed-text, text-embedding-3-small)
	// +kubebuilder:validation:Required
	Model string `json:"model"`

	// Dimensions is the vector dimension size produced by this model
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=768
	// +optional
	Dimensions int32 `json:"dimensions,omitempty"`

	// BatchSize is the maximum number of texts to embed in a single request
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=32
	// +optional
	BatchSize int32 `json:"batchSize,omitempty"`
}

// EmbeddingModelInfo contains metadata about the embedding model
type EmbeddingModelInfo struct {
	// Dimensions is the actual vector dimension size
	Dimensions int32 `json:"dimensions,omitempty"`

	// MaxInputTokens is the maximum input size in tokens
	MaxInputTokens int32 `json:"maxInputTokens,omitempty"`

	// Family is the model family (e.g., nomic, openai, cohere)
	Family string `json:"family,omitempty"`
}

// EmbeddingModelStatus defines the observed state of EmbeddingModel
type EmbeddingModelStatus struct {
	// State is the current state (Pending, Pulling, Available, Error)
	State string `json:"state,omitempty"`

	// Ready indicates if the embedding model is ready for use
	Ready bool `json:"ready,omitempty"`

	// Endpoint is the embedding API endpoint (from the provider)
	// Used by RAGSource to configure the indexer
	Endpoint string `json:"endpoint,omitempty"`

	// ModelInfo contains discovered model metadata
	// +optional
	ModelInfo *EmbeddingModelInfo `json:"modelInfo,omitempty"`

	// Message provides additional status information
	Message string `json:"message,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=emb
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model`
// +kubebuilder:printcolumn:name="Dimensions",type=integer,JSONPath=`.spec.dimensions`
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// EmbeddingModel is the Schema for the embeddingmodels API
type EmbeddingModel struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EmbeddingModelSpec   `json:"spec,omitempty"`
	Status EmbeddingModelStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// EmbeddingModelList contains a list of EmbeddingModel
type EmbeddingModelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EmbeddingModel `json:"items"`
}

func init() {
	registerTypes(&EmbeddingModel{}, &EmbeddingModelList{})
}
