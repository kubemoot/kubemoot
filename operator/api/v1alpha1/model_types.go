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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ModelSpec defines the desired state of Model.
type ModelSpec struct {
	// ProviderRef references the parent ModelProvider
	// +kubebuilder:validation:Required
	ProviderRef string `json:"providerRef"`

	// Model is the model identifier (e.g., "qwen2.5:7b", "gpt-4o")
	// +kubebuilder:validation:Required
	Model string `json:"model"`

	// ContextLength is the maximum context window size (optional override)
	// +optional
	ContextLength int `json:"contextLength,omitempty"`

	// Quantization specifies the quantization method (e.g., q4_K_M)
	// +optional
	Quantization string `json:"quantization,omitempty"`

	// VRAMMib declares the VRAM footprint (in MiB) this Model needs when
	// loaded on a Provider. Used by the Scheduler v2 filter step to enforce
	// capacity: a Model is feasible on a Provider only when
	// VRAMMib <= ModelProvider.status.capacity.vramTotalMiB after eviction
	// of any displaceable resident models. Required for CrewSchedulingPolicy
	// matching; if unset, the Model is treated as best-fit-unknown and may
	// be filtered out under strict policies.
	//
	// See kubemoot/docs/scheduler-v2.md for the scheduling algorithm.
	// +optional
	// +kubebuilder:validation:Minimum=0
	VRAMMib int32 `json:"vramMib,omitempty"`
}

// ModelStatus defines the observed state of Model.
type ModelStatus struct {
	// State is the current state (Pending, Pulling, Available, Loaded, Error)
	// Available = downloaded, Loaded = in GPU memory
	// +optional
	State string `json:"state,omitempty"`

	// Ready indicates if the model is ready for inference
	// +optional
	Ready bool `json:"ready,omitempty"`

	// Endpoint is the inference endpoint for this model
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// ModelInfo contains metadata discovered after download
	// +optional
	ModelInfo *ModelInfo `json:"modelInfo,omitempty"`

	// Pull reports download progress while State is Pulling and is cleared
	// once the model is on the provider.
	// +optional
	Pull *PullProgress `json:"pull,omitempty"`

	// Message provides additional status information
	// +optional
	Message string `json:"message,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// PullProgress is the download progress of a model being pulled onto a provider.
type PullProgress struct {
	// CompletedBytes is how many bytes of the known layers have downloaded
	// +optional
	CompletedBytes int64 `json:"completedBytes,omitempty"`

	// TotalBytes is the combined size of the layers the provider has announced so
	// far; it can grow while the pull discovers more layers
	// +optional
	TotalBytes int64 `json:"totalBytes,omitempty"`

	// Percent is CompletedBytes as a whole percentage of TotalBytes
	// +optional
	Percent int32 `json:"percent,omitempty"`
}

// ModelInfo contains metadata about a downloaded model
type ModelInfo struct {
	// Size is the model size on disk (e.g., "4.7GB")
	// +optional
	Size string `json:"size,omitempty"`

	// Parameters is the parameter count (e.g., "7B", "70B")
	// +optional
	Parameters string `json:"parameters,omitempty"`

	// Family is the model family (e.g., "llama", "qwen", "mistral")
	// +optional
	Family string `json:"family,omitempty"`

	// Quantization is the detected quantization (e.g., "Q4_K_M", "F16")
	// +optional
	Quantization string `json:"quantization,omitempty"`

	// ContextLength is the maximum context window
	// +optional
	ContextLength int `json:"contextLength,omitempty"`

	// Format is the model format (e.g., "gguf", "safetensors")
	// +optional
	Format string `json:"format,omitempty"`

	// Digest is the model digest/hash
	// +optional
	Digest string `json:"digest,omitempty"`

	// ModifiedAt is when the model was last modified
	// +optional
	ModifiedAt string `json:"modifiedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mdl
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.providerRef`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Progress",type=integer,JSONPath=`.status.pull.percent`
// +kubebuilder:printcolumn:name="Size",type=string,JSONPath=`.status.modelInfo.size`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Model is the Schema for the models API.
type Model struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ModelSpec   `json:"spec,omitempty"`
	Status ModelStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ModelList contains a list of Model.
type ModelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Model `json:"items"`
}

func init() {
	registerTypes(&Model{}, &ModelList{})
}
