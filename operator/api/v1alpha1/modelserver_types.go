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

// ServerType defines the type of model server. ollama and vllm are
// supported today; the field stays a plain string so more types can be added
// later without a breaking schema change.
type ServerType string

const (
	ServerTypeOllama ServerType = "ollama"
	ServerTypeVLLM   ServerType = "vllm"
)

// UnsupportedServerTypeMessage is the admission and status message for any
// server type other than ollama and vllm. Keep it identical to the
// XValidation message on ModelServerSpec.Type (a marker cannot reference a
// const); the envtest admission test compares this const with the live CRD's
// error.
const UnsupportedServerTypeMessage = "only types ollama and vllm are supported today; openai and anthropic are not supported yet"

// ModelServerSpec defines the desired state of ModelServer. Like a Service, it
// has a type and flat fields that each apply to some types: endpoint to ollama;
// serviceRef, servers, and sleepLevel to vllm.
// +kubebuilder:validation:XValidation:rule="self.type != 'ollama' || has(self.endpoint)",message="an ollama server needs spec.endpoint"
// +kubebuilder:validation:XValidation:rule="self.type == 'ollama' || !has(self.endpoint)",message="spec.endpoint is only for an ollama server; a vllm server names its servers with spec.serviceRef or spec.servers"
// +kubebuilder:validation:XValidation:rule="self.type == 'vllm' || !has(self.serviceRef)",message="spec.serviceRef is only for a vllm server"
// +kubebuilder:validation:XValidation:rule="self.type == 'vllm' || !has(self.servers)",message="spec.servers is only for a vllm server"
// +kubebuilder:validation:XValidation:rule="self.type == 'vllm' || !has(self.sleepLevel)",message="spec.sleepLevel is only for a vllm server"
// +kubebuilder:validation:XValidation:rule="self.type != 'vllm' || (has(self.serviceRef) != has(self.servers))",message="a vllm server sets exactly one of spec.serviceRef (discover the servers from a Service) or spec.servers (list them)"
// +kubebuilder:validation:XValidation:rule="(has(self.sleepLevel) ? self.sleepLevel : 1) == (has(oldSelf.sleepLevel) ? oldSelf.sleepLevel : 1)",message="spec.sleepLevel is immutable: delete and recreate the server to change it"
type ModelServerSpec struct {
	// Type specifies the server type: ollama or vllm.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == 'ollama' || self == 'vllm'",message="only types ollama and vllm are supported today; openai and anthropic are not supported yet"
	Type ServerType `json:"type"`

	// Endpoint is the Ollama API endpoint (required for an ollama server, and
	// allowed only there). A vllm server has no single endpoint: its servers
	// come from serviceRef or servers.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// ServiceRef (vllm only) names the Service that fronts the vLLM servers, for
	// example the one the kubemoot-vllm chart installs. The operator reads the
	// Service's ports and asks each port's /v1/models which model it serves, so no
	// endpoints are written by hand. Exactly one of serviceRef or servers.
	// +optional
	ServiceRef *VLLMServiceRef `json:"serviceRef,omitempty"`

	// Servers (vllm only) lists the server's vLLM servers, one per served model,
	// for vLLM deployed some other way than behind one Service. The operator does
	// not start them: each is run with sleep mode on (--enable-sleep-mode and
	// VLLM_SERVER_DEV_MODE=1). Exactly one of serviceRef or servers.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	// +listType=map
	// +listMapKey=model
	// +optional
	Servers []VLLMServer `json:"servers,omitempty"`

	// SleepLevel (vllm only) is how a server is put to sleep when another model
	// takes the accelerator: 1 parks the weights in host memory (fast to wake), 2
	// drops them and reloads from disk on wake (holds no host memory). Unset means
	// 1. It cannot change once the server exists, because a sleeping server wakes
	// by the level the server names, and that must be the level it slept at; to
	// change it, delete and recreate the server.
	// +kubebuilder:validation:Enum=1;2
	// +optional
	SleepLevel int `json:"sleepLevel,omitempty"`

	// SecretRef is reserved for future server types and is not used by ollama
	// +optional
	SecretRef string `json:"secretRef,omitempty"`

	// Scheduling contains optional scheduling preferences for this server
	// +optional
	Scheduling *ServerScheduling `json:"scheduling,omitempty"`

	// Calibration controls whether this server may be probed to measure its models
	// +optional
	Calibration *ServerCalibrationSpec `json:"calibration,omitempty"`
}

// VLLMServiceRef names a Service whose ports are vLLM servers.
type VLLMServiceRef struct {
	// Name is the Service's name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace is the Service's namespace. Default: the server's namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// VLLMServer is one vLLM server and the model it serves.
type VLLMServer struct {
	// Model is the served name of the model this server answers to: the Model's
	// spec.model, and the --served-model-name the server runs with.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Model string `json:"model"`

	// Endpoint is the server's OpenAI-compatible base URL, without /v1
	// (for example http://vllm-qwen3-14b.models:8000).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^https?://[^/\s]+.*$`
	Endpoint string `json:"endpoint"`
}

// VLLMServers is the servers the server's models are served by: the listed
// ones with spec.servers, the ones the operator discovered from the Service (kept
// in its status) with spec.serviceRef. A server of another type has none.
func (p *ModelServer) VLLMServers() []VLLMServer {
	switch {
	case p.Spec.Type != ServerTypeVLLM:
		return nil
	case p.Spec.ServiceRef != nil:
		return p.Status.VLLMServers
	}
	return p.Spec.Servers
}

// EffectiveSpec is the spec with spec.servers filled in for both ways of naming
// the servers, so the engine adapter and EndpointFor read one list. It shares
// nothing with p.
func (p *ModelServer) EffectiveSpec() ModelServerSpec {
	spec := *p.Spec.DeepCopy()
	if spec.Type == ServerTypeVLLM {
		spec.Servers = append([]VLLMServer(nil), p.VLLMServers()...)
	}
	return spec
}

// EndpointFor is the endpoint a request for model goes to, however the server
// names its servers; see ModelServerSpec.EndpointFor.
func (p *ModelServer) EndpointFor(model string) string {
	spec := p.EffectiveSpec()
	return spec.EndpointFor(model)
}

// EndpointFor is the endpoint a request for model goes to: the endpoint of the
// vLLM server serving it in spec.servers, or spec.endpoint for a server that is
// one process. It is empty when a vllm server lists no server for the model.
// It reads spec.servers only: for a server that uses spec.serviceRef the servers
// are in the status, so call ModelServer.EndpointFor (or EffectiveSpec first).
func (s *ModelServerSpec) EndpointFor(model string) string {
	if s.Type != ServerTypeVLLM {
		return s.Endpoint
	}
	for _, srv := range s.Servers {
		if srv.Model == model {
			return srv.Endpoint
		}
	}
	return ""
}

// SleepLevelOrDefault is the vLLM sleep level, 1 when unset.
func (s *ModelServerSpec) SleepLevelOrDefault() int {
	if s.SleepLevel == 0 {
		return 1
	}
	return s.SleepLevel
}

// ServerCalibrationSpec controls calibration of a ModelServer. To ask for a
// fresh calibration, set the annotation kubemoot.ai/calibration-reset to a new
// value; with kubemoot.ai/calibration-reset-scope: all, the start of that
// calibration also forgets the server's recorded performance history.
type ServerCalibrationSpec struct {
	// Enabled allows calibration probes on this server (the default). When false,
	// the accelerator-mcp probe_model tool refuses the server: use it for an
	// accelerator whose probe calls cost money and are not wanted.
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
}

// ServerScheduling contains scheduling preferences for a ModelServer
type ServerScheduling struct {
	// Weight is a scheduling preference (higher = preferred). Default 100.
	// Use to prefer one server over another for non-capacity reasons.
	// +kubebuilder:default=100
	// +optional
	Weight int `json:"weight,omitempty"`

	// MemoryMiB is the memory budget for model residency that the fit gate may
	// spend on this server: VRAM on a GPU host, the host RAM the server may use
	// on a CPU host. It applies when the operator cannot discover the budget (no
	// DCGM metrics, scheduler disabled, or a CPU server); a discovered value wins.
	// Unset leaves the budget unknown, and agents refuse cold loads on this server.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MemoryMiB int64 `json:"memoryMiB,omitempty"`
}

// ModelServerStatus defines the observed state of ModelServer.
type ModelServerStatus struct {
	// Phase is the current phase (Pending, Ready, Failed)
	// +optional
	Phase string `json:"phase,omitempty"`

	// Ready indicates if the server is ready
	// +optional
	Ready bool `json:"ready,omitempty"`

	// ServerInfo contains metadata discovered after connecting
	// +optional
	ServerInfo *ServerInfo `json:"serverInfo,omitempty"`

	// Capacity contains discovered capacity information for scheduling
	// +optional
	Capacity *DiscoveredCapacity `json:"capacity,omitempty"`

	// VLLMServers is, for a vllm server with spec.serviceRef, the servers the
	// operator discovered: each served name a port reports on /v1/models and that
	// port's URL. A port that stops answering keeps the servers last found there.
	// Empty with spec.servers, which says it itself.
	// +optional
	VLLMServers []VLLMServer `json:"vllmServers,omitempty"`

	// Storage is the disk the server keeps its models on, when the operator
	// can find the volume behind the Ollama pod
	// +optional
	Storage *ServerStorage `json:"storage,omitempty"`

	// Message provides additional status information
	// +optional
	Message string `json:"message,omitempty"`

	// Models is the server's part of the cluster's model catalog: every chat
	// model the server has (downloaded for Ollama, answering for vLLM) merged
	// with the Model objects in the server's namespace that name it. A Model
	// with spec.disabled is left out; an embedding-only model is never admitted.
	// Scheduling rules match each entry's labels.
	// +optional
	// +listType=map
	// +listMapKey=name
	Models []CatalogModel `json:"models,omitempty"`

	// Calibration is what calibration measured on this server: per model, how
	// long it takes to load, how fast it processes prompts and generates, and
	// whether it fits the accelerator. It is recorded through the accelerator-mcp
	// tools; the scheduler does not read it yet
	// +optional
	Calibration *ServerCalibration `json:"calibration,omitempty"`

	// CalibrationRequest is the operator's last request to calibrate this server,
	// made through the accelerator-mcp core server. The operator asks once per need
	// (a new server, a reset, a changed engine, newly bound models), and again for
	// the same need only after the calibration paused as Waiting and the server
	// fell idle
	// +optional
	CalibrationRequest *CalibrationRequest `json:"calibrationRequest,omitempty"`

	// CalibrationDrift lists the models whose crew calls, running alone on the
	// server, generated much slower or faster than calibration measured. The
	// operator asks the scheduling crew once per drift to judge whether to
	// recalibrate; it is cleared once the speeds agree again
	// +optional
	CalibrationDrift *CalibrationDrift `json:"calibrationDrift,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ServerCalibration is the outcome of calibrating a ModelServer.
type ServerCalibration struct {
	// Phase is Calibrating, Calibrated, Waiting (paused because crew calls were
	// using the server), or Failed
	// +optional
	Phase string `json:"phase,omitempty"`

	// Message explains the phase
	// +optional
	Message string `json:"message,omitempty"`

	// EngineVersion is the engine version the results were measured on
	// +optional
	EngineVersion string `json:"engineVersion,omitempty"`

	// ResetToken is the last kubemoot.ai/calibration-reset value acted on: set when
	// the calibration it asked for starts
	// +optional
	ResetToken string `json:"resetToken,omitempty"`

	// CompletedAt is when the last calibration finished
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// UpdatedAt is when the calibration was last recorded, in any phase
	// +optional
	UpdatedAt *metav1.Time `json:"updatedAt,omitempty"`

	// Models holds one result per model calibrated
	// +optional
	Models []ModelCalibration `json:"models,omitempty"`
}

// CalibrationRequest is a request the operator made to calibrate a server.
type CalibrationRequest struct {
	// Key names the need asked about, such as new, reset/<token>, engine/<version>,
	// or models/<digest>
	Key string `json:"key"`

	// Reason is the need in words
	// +optional
	Reason string `json:"reason,omitempty"`

	// RequestedAt is when the request was made
	RequestedAt metav1.Time `json:"requestedAt"`
}

// CalibrationDrift is a difference between what crew calls showed and what
// calibration measured.
type CalibrationDrift struct {
	// Key names this drift: the models and the calibration they drifted from
	Key string `json:"key"`

	// DetectedAt is when the operator first saw it
	DetectedAt metav1.Time `json:"detectedAt"`

	// Models are the models that drifted
	Models []ModelDrift `json:"models"`

	// AskedAt is when the scheduling crew was asked to judge it
	// +optional
	AskedAt *metav1.Time `json:"askedAt,omitempty"`
}

// ModelDrift is one model's calibrated and observed generation speed.
type ModelDrift struct {
	// Model is the model name on the server
	Model string `json:"model"`

	// CalibratedTokensPerSecond is the generation speed calibration measured
	CalibratedTokensPerSecond int64 `json:"calibratedTokensPerSecond"`

	// ObservedTokensPerSecond is the median generation speed of warm crew calls
	// that ran alone on the server since the calibration
	ObservedTokensPerSecond int64 `json:"observedTokensPerSecond"`

	// Samples is how many such calls the median is over
	Samples int `json:"samples"`
}

// ModelCalibration is what calibration measured for one model on a server.
// Speeds are tokens per second, rounded down.
type ModelCalibration struct {
	// Model is the model name on the server, such as qwen3:14b
	Model string `json:"model"`

	// Fits is false when the model does not fit the server's memory: it would
	// spill to the CPU, or it is larger than the memory budget
	Fits bool `json:"fits"`

	// LoadMs is how long the engine took to load the model, in milliseconds
	// +optional
	LoadMs int64 `json:"loadMs,omitempty"`

	// ResidentMiB is the accelerator memory the model took once loaded
	// +optional
	ResidentMiB int64 `json:"residentMiB,omitempty"`

	// ContextLength is the context the engine gives the model, in tokens
	// +optional
	ContextLength int `json:"contextLength,omitempty"`

	// PromptTokensPerSecond is how fast the model processes a long prompt
	// +optional
	PromptTokensPerSecond int64 `json:"promptTokensPerSecond,omitempty"`

	// GenerateTokensPerSecond is how fast one request generates
	// +optional
	GenerateTokensPerSecond int64 `json:"generateTokensPerSecond,omitempty"`

	// ParallelRequests is how many requests ran together for the parallel measure
	// +optional
	ParallelRequests int `json:"parallelRequests,omitempty"`

	// ParallelGenerateTokensPerSecond is the total generation speed with
	// ParallelRequests running together
	// +optional
	ParallelGenerateTokensPerSecond int64 `json:"parallelGenerateTokensPerSecond,omitempty"`

	// Reason explains a model that does not fit or could not be measured
	// +optional
	Reason string `json:"reason,omitempty"`

	// CalibratedAt is when this model was measured
	// +optional
	CalibratedAt *metav1.Time `json:"calibratedAt,omitempty"`
}

// ServerStorage describes the volume that holds a server's model files.
// FreeBytes is an estimate: the volume's declared capacity minus the sizes
// Ollama lists for its downloaded models and the unfinished downloads the
// operator knows about. Layers shared between models are counted once per model,
// and partial files left by a download the operator no longer remembers (after an
// operator restart) are not counted, so the real free space can differ slightly.
type ServerStorage struct {
	// Volume names the backing volume, for example "pvc/ollama-data"
	// +optional
	Volume string `json:"volume,omitempty"`

	// TotalBytes is the declared capacity of the volume
	// +optional
	TotalBytes int64 `json:"totalBytes,omitempty"`

	// ModelBytes is the sum of the sizes of the models the server lists
	// +optional
	ModelBytes int64 `json:"modelBytes,omitempty"`

	// PartialBytes is the disk held by downloads that have not finished: pulls in
	// progress, and the partial files a cancelled or failed pull leaves behind
	// until the model server restarts. Ollama lists none of it, so the operator
	// counts it from the pulls it ran.
	// +optional
	PartialBytes int64 `json:"partialBytes,omitempty"`

	// FreeBytes is TotalBytes minus ModelBytes and PartialBytes, never below zero
	// +optional
	FreeBytes int64 `json:"freeBytes,omitempty"`

	// LastProbed is when the storage was last measured
	// +optional
	LastProbed *metav1.Time `json:"lastProbed,omitempty"`
}

// AvailableModel represents a model downloaded on a server (from /api/tags).
// Carries the on-disk size so the JIT scheduler can compute a cold-load
// footprint estimate before the model is ever loaded into VRAM.
type AvailableModel struct {
	// Name is the model identifier (e.g. "qwen3:32b")
	Name string `json:"name"`

	// SizeBytes is the on-disk model size in bytes (from /api/tags .size).
	// Used as a proxy for VRAM cold-load footprint when no server has yet
	// loaded the model and no /api/ps size_vram observation exists.
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`
}

// UnmarshalJSON tolerates the legacy on-disk form where AvailableModels was a
// []string of bare model names. Existing ModelServer CRs in etcd predate the
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

	// AvailableModels lists models downloaded on the server (from /api/tags)
	// with their on-disk sizes. The JIT scheduler uses SizeBytes as a cold-load
	// footprint proxy when no loaded-model observation exists yet.
	// +optional
	AvailableModels []AvailableModel `json:"availableModels,omitempty"`

	// AgentCount is the total number of agent-phase bindings on this server
	// (mulling + triage summed), for status readers and the dashboard. The
	// scheduler does not read it.
	// +optional
	AgentCount int `json:"agentCount,omitempty"`

	// MullingAgentCount is the number of agents whose mulling phase binds to
	// this server (kubemoot.ai/mulling-server label on the Agent's
	// Deployment matches this server's name). Observability only; it does
	// not feed model selection.
	// +optional
	MullingAgentCount int `json:"mullingAgentCount,omitempty"`

	// TriageAgentCount is the number of agents whose triage phase binds to
	// this server (kubemoot.ai/triage-server label). Observability only; it does not
	// feed model selection.
	// +optional
	TriageAgentCount int `json:"triageAgentCount,omitempty"`

	// NodeName is the Kubernetes node hosting the Ollama pod
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// LastProbed is when capacity was last discovered
	// +optional
	LastProbed *metav1.Time `json:"lastProbed,omitempty"`
}

// LoadedModel represents a model currently loaded in the server
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

// ServerInfo contains metadata about a connected server
type ServerInfo struct {
	// Version is the server API version
	// +optional
	Version string `json:"version,omitempty"`

	// RateLimits shows current rate limit info (for cloud providers)
	// +optional
	RateLimits *RateLimitInfo `json:"rateLimits,omitempty"`

	// LastChecked is when the server was last verified
	// +optional
	LastChecked string `json:"lastChecked,omitempty"`
}

// RateLimitInfo contains rate limit details from the server
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

// ModelServer is the Schema for the modelservers API.
type ModelServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ModelServerSpec   `json:"spec,omitempty"`
	Status ModelServerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ModelServerList contains a list of ModelServer.
type ModelServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelServer `json:"items"`
}

func init() {
	registerTypes(&ModelServer{}, &ModelServerList{})
}
