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
// +kubebuilder:validation:XValidation:rule="(has(self.source) == has(oldSelf.source) && (!has(self.source) || self.source == oldSelf.source)) || (!has(oldSelf.source) && has(self.source) && has(self.source.ollama))",message="spec.source is immutable except for adding an ollama source to a Model that has none: delete and recreate the Model to change where its weights come from"
// +kubebuilder:validation:XValidation:rule="!has(self.source) || !has(self.source.ollama) || self.source.ollama.tag == self.model",message="source.ollama.tag must equal spec.model: Ollama serves a model only under its tag"
type ModelSpec struct {
	// ProviderRef references the parent ModelProvider
	// +kubebuilder:validation:Required
	ProviderRef string `json:"providerRef"`

	// Model is the served name: the name every request uses to ask for this
	// model (e.g., "qwen3:14b"). Where the weights come from is spec.source.
	// For vLLM this is the name the server is started with
	// (--served-model-name); the operator checks it against the served names
	// the provider's servers report and reports a mismatch on the Model's
	// ServedNameFound condition. Ollama serves a model only under its tag, so
	// for an ollama source it must equal source.ollama.tag.
	// +kubebuilder:validation:Required
	Model string `json:"model"`

	// Source says where the weights come from, as exactly one of ollama or
	// huggingFace. It cannot change after creation, except that an ollama
	// source may be added to a Model that has none (its tag equals spec.model,
	// the same meaning as an absent source). It must fit the engine of the
	// provider: ollama for an ollama provider, huggingFace for a vLLM
	// provider. A mismatch is reported on the Model's SourceCompatible
	// condition and nothing is pulled; the operator never guesses or falls
	// back. When absent the source is an Ollama tag equal to spec.model on an
	// ollama provider. On a vLLM provider the source is optional: the server
	// decides its weights, and a huggingFace source is information, compared
	// with the repository the server reports (ServedRepoMatches condition)
	// when it reports one; the revision cannot be read from the server.
	// +optional
	Source *ModelSource `json:"source,omitempty"`

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

	// Parameters is the model's total parameter count, such as "14B", "0.5B" or
	// "751M". Declare it where the engine cannot report it (vLLM does not) or to
	// correct a wrong report. The size class (small under 12B, medium from 12B
	// to under 30B, large from 30B) is derived from it. A kubemoot.ai/size label
	// on the Model overrides the derived class.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?[KkMmBbTt]$`
	Parameters string `json:"parameters,omitempty"`

	// Capabilities are abilities the engine cannot report: tool-calling,
	// reasoning, vision. When set they replace what the engine reports, and each
	// becomes a capability/<name>: "true" label scheduling rules can select on.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	// +listType=set
	Capabilities []string `json:"capabilities,omitempty"`

	// Disabled keeps the model out of the cluster's model catalog: the operator
	// downloads nothing for it, no crew selects it, and calibration skips it. Use it to exclude a model
	// the provider has but the cluster owner does not want used.
	// +optional
	Disabled bool `json:"disabled,omitempty"`
}

// ModelSource is where a Model's weights come from. Exactly one field is set.
// +kubebuilder:validation:XValidation:rule="(has(self.ollama) ? 1 : 0) + (has(self.huggingFace) ? 1 : 0) == 1",message="exactly one of ollama or huggingFace must be set"
type ModelSource struct {
	// Ollama names an Ollama library tag.
	// +optional
	Ollama *OllamaSource `json:"ollama,omitempty"`

	// HuggingFace names a Hugging Face repository pinned to a revision.
	// +optional
	HuggingFace *HuggingFaceSource `json:"huggingFace,omitempty"`
}

// OllamaSource is an Ollama library tag.
type OllamaSource struct {
	// Tag is the Ollama tag to pull (e.g., "qwen3:14b"). It must equal
	// spec.model, because Ollama serves a model only under its tag.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Tag string `json:"tag"`
}

// HuggingFaceSource is a Hugging Face repository at a pinned revision.
type HuggingFaceSource struct {
	// Repo is the repository (e.g., "Qwen/Qwen3-14B-AWQ").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`

	// Revision is the commit sha the weights are pinned to: 7 to 40 hex
	// characters. A branch or tag name is not accepted, so the weights never
	// change under a running Model.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F]{7,40}$`
	Revision string `json:"revision"`

	// Quantization names the quantization of the repository's weights
	// (e.g., awq, gptq).
	// +optional
	Quantization string `json:"quantization,omitempty"`
}

// OllamaTag is the tag an Ollama provider pulls and serves for this Model:
// source.ollama.tag when set, else spec.model. It is empty when the source is
// not an Ollama one, which means there is nothing for Ollama to pull.
func (m *Model) OllamaTag() string {
	if s := m.Spec.Source; s != nil {
		if s.Ollama != nil {
			return s.Ollama.Tag
		}
		if s.HuggingFace != nil {
			return ""
		}
	}
	return m.Spec.Model
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
