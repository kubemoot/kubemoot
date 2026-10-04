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
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ProviderType defines the type of model provider. Only ollama is supported
// today; the field stays a plain string so more types can be added later
// without a breaking schema change.
type ProviderType string

const (
	ProviderTypeOllama ProviderType = "ollama"
)

// UnsupportedProviderTypeMessage is the admission and status message for any
// provider type other than ollama. Keep it identical to the XValidation
// message on ModelProviderSpec.Type (a marker cannot reference a const); the
// envtest admission test compares this const with the live CRD's error.
const UnsupportedProviderTypeMessage = "only type ollama is supported today; openai and anthropic are not supported yet"

// ModelProviderSpec defines the desired state of ModelProvider.
type ModelProviderSpec struct {
	// Type specifies the provider type. Only ollama is supported today.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == 'ollama'",message="only type ollama is supported today; openai and anthropic are not supported yet"
	Type ProviderType `json:"type"`

	// Endpoint is the Ollama API endpoint (required)
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// SecretRef is reserved for future provider types and is not used by ollama
	// +optional
	SecretRef string `json:"secretRef,omitempty"`

	// Scheduling contains optional scheduling preferences for this provider
	// +optional
	Scheduling *ProviderScheduling `json:"scheduling,omitempty"`
}

// ProviderScheduling contains scheduling preferences for a ModelProvider
type ProviderScheduling struct {
	// Weight is a scheduling preference (higher = preferred). Default 100.
	// Use to prefer one provider over another for non-capacity reasons.
	// +kubebuilder:default=100
	// +optional
	Weight int `json:"weight,omitempty"`

	// MemoryMiB is the memory budget for model residency that the fit gate may
	// spend on this provider: VRAM on a GPU host, the host RAM the server may use
	// on a CPU host. It applies when the operator cannot discover the budget (no
	// DCGM metrics, scheduler disabled, or a CPU provider); a discovered value wins.
	// Unset leaves the budget unknown, and agents refuse cold loads on this provider.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MemoryMiB int64 `json:"memoryMiB,omitempty"`
}

// ModelProviderStatus defines the observed state of ModelProvider.
type ModelProviderStatus struct {
	// Phase is the current phase (Pending, Ready, Failed)
	// +optional
	Phase string `json:"phase,omitempty"`

	// Ready indicates if the provider is ready
	// +optional
	Ready bool `json:"ready,omitempty"`

	// ProviderInfo contains metadata discovered after connecting
	// +optional
	ProviderInfo *ProviderInfo `json:"providerInfo,omitempty"`

	// Capacity contains discovered capacity information for scheduling
	// +optional
	Capacity *DiscoveredCapacity `json:"capacity,omitempty"`

	// Message provides additional status information
	// +optional
	Message string `json:"message,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// AvailableModel represents a model downloaded on a provider (from /api/tags).
// Carries the on-disk size so the JIT scheduler can compute a cold-load
// footprint estimate before the model is ever loaded into VRAM.
type AvailableModel struct {
	// Name is the model identifier (e.g. "qwen3:32b")
	Name string `json:"name"`

	// SizeBytes is the on-disk model size in bytes (from /api/tags .size).
	// Used as a proxy for VRAM cold-load footprint when no provider has yet
	// loaded the model and no /api/ps size_vram observation exists.
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`
}

// UnmarshalJSON tolerates the legacy on-disk form where AvailableModels was a
// []string of bare model names. Existing ModelProvider CRs in etcd predate the
// {name,sizeBytes} object form; without this the typed informer cache fails to
// decode a stored string into a struct ("cannot unmarshal string into
// AvailableModel") and the operator crash-loops on startup. A bare string
// decodes into {Name: s}; the object form decodes normally. The operator
// rebuilds this status field with sizes on the next reconcile, so legacy
// entries are transient.
func (m *AvailableModel) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var name string
		if err := json.Unmarshal(data, &name); err != nil {
			return err
		}
		m.Name = name
		m.SizeBytes = 0
		return nil
	}
	// Object form. Use an alias to avoid infinite recursion into this method.
	type availableModelAlias AvailableModel
	var alias availableModelAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	*m = AvailableModel(alias)
	return nil
}

// DiscoveredCapacity contains capacity information discovered at runtime
type DiscoveredCapacity struct {
	// MaxParallel discovered from OLLAMA_NUM_PARALLEL env var on the Ollama pod
	// +optional
	MaxParallel int `json:"maxParallel,omitempty"`

	// ContextLength is the context window, in tokens, the engine gives each
	// request, discovered from OLLAMA_CONTEXT_LENGTH on the Ollama pod. Zero
	// when the engine chooses its own default; the context each loaded model
	// actually runs with is on LoadedModels.
	// +optional
	ContextLength int `json:"contextLength,omitempty"`

	// VRAMTotalMiB discovered from DCGM_FI_DEV_FB_TOTAL via Prometheus
	// +optional
	VRAMTotalMiB int64 `json:"vramTotalMiB,omitempty"`

	// VRAMUsedMiB from sum of loaded model size_vram via /api/ps
	// +optional
	VRAMUsedMiB int64 `json:"vramUsedMiB,omitempty"`

	// GPUModel discovered from DCGM metrics (e.g., "NVIDIA GeForce RTX 5090")
	// +optional
	GPUModel string `json:"gpuModel,omitempty"`

	// LoadedModels from /api/ps with VRAM details
	// +optional
	LoadedModels []LoadedModel `json:"loadedModels,omitempty"`

	// AvailableModels lists models downloaded on the provider (from /api/tags)
	// with their on-disk sizes. The JIT scheduler uses SizeBytes as a cold-load
	// footprint proxy when no loaded-model observation exists yet.
	// +optional
	AvailableModels []AvailableModel `json:"availableModels,omitempty"`

	// AgentCount is the total number of agent-phase bindings on this provider
	// (mulling + triage summed). Kept for backwards compatibility with status
	// readers; the scheduler uses the per-phase fields below for bin-packing.
	// +optional
	AgentCount int `json:"agentCount,omitempty"`

	// MullingAgentCount is the number of agents whose mulling phase binds to
	// this provider (kubemoot.ai/mulling-provider label on the Agent's
	// Deployment matches this provider's name). Used by scoreCandidate when
	// choosing a mulling-phase model to spread sustained tool-calling work
	// across providers.
	// +optional
	MullingAgentCount int `json:"mullingAgentCount,omitempty"`

	// TriageAgentCount is the number of agents whose triage phase binds to
	// this provider (kubemoot.ai/triage-provider label). Used by
	// scoreCandidate when choosing a triage-phase model.
	// +optional
	TriageAgentCount int `json:"triageAgentCount,omitempty"`

	// NodeName is the Kubernetes node hosting the Ollama pod
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// LastProbed is when capacity was last discovered
	// +optional
	LastProbed *metav1.Time `json:"lastProbed,omitempty"`
}

// LoadedModel represents a model currently loaded in the provider
type LoadedModel struct {
	// Name is the model identifier
	Name string `json:"name"`

	// SizeVRAM is the VRAM used by this model in bytes (from /api/ps)
	// +optional
	SizeVRAM int64 `json:"sizeVram,omitempty"`

	// Size is the total model size in bytes
	// +optional
	Size int64 `json:"size,omitempty"`

	// ContextLength is the context window, in tokens, each request to this
	// loaded model gets (from /api/ps context_length). A prompt larger than
	// this is cut by the engine, not rejected.
	// +optional
	ContextLength int `json:"contextLength,omitempty"`
}

// ProviderInfo contains metadata about a connected provider
type ProviderInfo struct {
	// Version is the provider API version
	// +optional
	Version string `json:"version,omitempty"`

	// RateLimits shows current rate limit info (for cloud providers)
	// +optional
	RateLimits *RateLimitInfo `json:"rateLimits,omitempty"`

	// LastChecked is when the provider was last verified
	// +optional
	LastChecked string `json:"lastChecked,omitempty"`
}

// RateLimitInfo contains rate limit details from the provider
type RateLimitInfo struct {
	// RequestsPerMinute is the allowed requests per minute
	// +optional
	RequestsPerMinute int `json:"requestsPerMinute,omitempty"`

	// TokensPerMinute is the allowed tokens per minute
	// +optional
	TokensPerMinute int `json:"tokensPerMinute,omitempty"`

	// RequestsRemaining is remaining requests in current window
	// +optional
	RequestsRemaining int `json:"requestsRemaining,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mdlp
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.spec.endpoint`
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ModelProvider is the Schema for the modelproviders API.
type ModelProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ModelProviderSpec   `json:"spec,omitempty"`
	Status ModelProviderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ModelProviderList contains a list of ModelProvider.
type ModelProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelProvider `json:"items"`
}

func init() {
	registerTypes(&ModelProvider{}, &ModelProviderList{})
}
