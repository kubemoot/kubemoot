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

// AgentType defines the type of agent
type AgentType string

const (
	// AgentTypeChat is a conversational agent
	AgentTypeChat AgentType = "chat"
	// AgentTypeTask is a task-oriented agent
	AgentTypeTask AgentType = "task"
	// AgentTypeWorkflow is a workflow orchestration agent
	AgentTypeWorkflow AgentType = "workflow"
)

// RAGSourceRef references a RAGSource for knowledge retrieval
type RAGSourceRef struct {
	// Name is the RAGSource resource name
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Priority determines the order of retrieval (higher = first)
	// +kubebuilder:default=0
	// +optional
	Priority int32 `json:"priority,omitempty"`

	// TopK is the number of results to retrieve from this source
	// +kubebuilder:default=5
	// +optional
	TopK int32 `json:"topK,omitempty"`
}

// DiscussRelevance was the per-agent relevance filter configuration.
//
// Deprecated: the agent runtime no longer reads it. The coordinator selects the
// agents for a discussion from their resumes, so an agent has no relevance
// filter of its own. The field is still accepted so existing manifests apply
// unchanged.
type DiscussRelevance struct {
	// Mode is deprecated and unread. Accepted values are kept for compatibility:
	// keyword, llm, keyword-then-llm.
	// +kubebuilder:validation:Enum=keyword;llm;keyword-then-llm
	// +kubebuilder:default=keyword
	// +optional
	Mode string `json:"mode,omitempty"`

	// PromptHint is deprecated and unread.
	// +optional
	PromptHint string `json:"promptHint,omitempty"`
}

// MCPServerRef references an MCPServer for tools
type MCPServerRef struct {
	// Name is the MCPServer resource name
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Enabled tools from this server (empty = all enabled)
	// +optional
	EnabledTools []string `json:"enabledTools,omitempty"`

	// Disabled tools from this server
	// +optional
	DisabledTools []string `json:"disabledTools,omitempty"`
}

// MemoryConfig defines how the agent handles conversation memory
type MemoryConfig struct {
	// Type is the memory backend type (in-memory, redis, postgres)
	// +kubebuilder:validation:Enum=in-memory;redis;postgres
	// +kubebuilder:default=in-memory
	// +optional
	Type string `json:"type,omitempty"`

	// MaxMessages is the maximum number of messages to retain
	// +kubebuilder:default=100
	// +optional
	MaxMessages int32 `json:"maxMessages,omitempty"`

	// MaxTokens is the maximum context window to use for memory
	// +kubebuilder:default=4096
	// +optional
	MaxTokens int32 `json:"maxTokens,omitempty"`

	// TTLSeconds is how long to retain conversations (0 = forever)
	// +optional
	TTLSeconds int32 `json:"ttlSeconds,omitempty"`

	// Endpoint is the memory backend endpoint (for redis/postgres)
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// SecretRef references credentials for the memory backend
	// +optional
	SecretRef string `json:"secretRef,omitempty"`
}

// DeploymentConfig defines how the agent is deployed
type DeploymentConfig struct {
	// Replicas is the number of agent instances
	// +kubebuilder:default=1
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// Image is the agent runtime image (uses default if not specified)
	// +optional
	Image string `json:"image,omitempty"`

	// Resources defines resource requirements
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// Env is additional environment variables
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// ServiceAccountName is the service account for the agent
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// Port is the HTTP port for the agent API
	// +kubebuilder:default=8080
	// +optional
	Port int32 `json:"port,omitempty"`
}

// RAGAutoDiscoverConfig configures automatic RAGSource discovery by keyword matching.
// When enabled, the agent controller lists all Ready RAGSources in the namespace,
// compares their status.topics against the agent's keywords, and auto-wires
// sources with sufficient overlap.
type RAGAutoDiscoverConfig struct {
	// Enabled controls whether auto-discovery is active
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Keywords are domain keywords for matching against RAGSource topics
	// +optional
	Keywords []string `json:"keywords,omitempty"`

	// MinOverlap is the minimum number of matching keywords to auto-wire
	// +kubebuilder:default=3
	// +optional
	MinOverlap int32 `json:"minOverlap,omitempty"`

	// TopK is the default topK for auto-discovered sources
	// +kubebuilder:default=2
	// +optional
	TopK int32 `json:"topK,omitempty"`
}

// AgentSpec defines the desired state of Agent
type AgentSpec struct {
	// Type is the agent type (chat, task, workflow)
	// +kubebuilder:validation:Enum=chat;task;workflow
	// +kubebuilder:default=chat
	// +optional
	Type AgentType `json:"type,omitempty"`

	// Description is a human-readable description of the agent
	// +optional
	Description string `json:"description,omitempty"`

	// Capabilities is the open string set of capabilities this Agent provides
	// or requires. The Scheduler v2 matches these against Model labels
	// declared via Model.metadata.labels (e.g., "capability/tool-calling")
	// through the active CrewSchedulingPolicy.spec.rules[].require/prefer
	// selectors.
	//
	// Examples: "tool-calling", "reasoning", "kubernetes", "vision",
	// "embedding", "low-latency", "large-context".
	//
	// Replaces Models[] in v2 — see kubemoot/docs/scheduler-v2.md.
	// +optional
	Capabilities []string `json:"capabilities,omitempty"`

	// DiscussRole indicates how this agent participates in discussions.
	// Common values: "tooler" (full participant), "researcher"
	// (advisory inputs only, excluded from settle triggers), "coordinator"
	// (drives the phase state machine). Free-form string.
	// +optional
	DiscussRole string `json:"discussRole,omitempty"`

	// PromptRefs is an ordered list of PromptModule names in the same
	// namespace. The operator resolves them and concatenates their content
	// into a `system.txt` file in the agent's policy ConfigMap. The
	// agent-runtime reads this at startup via KUBEMOOT_SYSTEM_PROMPT_FILE.
	// +optional
	PromptRefs []string `json:"promptRefs,omitempty"`

	// EnabledTools is the allow-list of MCP tools this agent may invoke.
	// When non-empty, the agent-runtime restricts tool calling to this set
	// (set as KUBEMOOT_ENABLED_TOOLS env var).
	// +optional
	EnabledTools []string `json:"enabledTools,omitempty"`

	// DisabledTools is the deny-list of MCP tools this agent must not invoke.
	// +optional
	DisabledTools []string `json:"disabledTools,omitempty"`

	// DiscussKeywords are domain keywords embedded in this agent's resume,
	// which the coordinator searches to select the agents for a discussion.
	// +optional
	DiscussKeywords []string `json:"discussKeywords,omitempty"`

	// DiscussRelevance configured the per-agent relevance filter.
	//
	// Deprecated: unread; accepted so existing manifests apply unchanged.
	// +optional
	DiscussRelevance *DiscussRelevance `json:"discussRelevance,omitempty"`

	// DiscussChannels are the NATS discussion channels this agent
	// subscribes to. Coordinator MUST list every channel it orchestrates
	// (e.g., kubernetes, observability, proxmox, general). Toolers
	// subscribe to channels matching their domain. Set as
	// KUBEMOOT_DISCUSS_CHANNELS env var. The agent-runtime's
	// DiscussionOrchestrator/DiscussionSubscriber requires this to be
	// non-empty in order to subscribe.
	// +optional
	DiscussChannels []string `json:"discussChannels,omitempty"`

	// TriageSummary is a one-line description of this agent's specialty,
	// emitted to the coordinator's triage prompt so it can decide whether
	// this agent should be invited into a discussion thread.
	// +optional
	TriageSummary string `json:"triageSummary,omitempty"`

	// Temperature for LLM inference. Defaults to 0.3 (good for tool
	// calling). Set as KUBEMOOT_MODEL_TEMPERATURE env var.
	// +optional
	Temperature string `json:"temperature,omitempty"`

	// MaxTokens cap for LLM responses. Set as KUBEMOOT_MODEL_MAX_TOKENS.
	// +optional
	MaxTokens int32 `json:"maxTokens,omitempty"`

	// Think controls whether the model emits a reasoning chain before
	// answering (maps to ollama's request `think` flag, set as
	// KUBEMOOT_MODEL_THINK). Unset leaves the model/family default (qwen3
	// thinks by default). Set false for deterministic tool-calling agents
	// whose latency is dominated by an unneeded reasoning chain; true for
	// reasoning agents (analysts, coordinator). Behavior is declared here,
	// not special-cased in the runtime.
	// +optional
	Think *bool `json:"think,omitempty"`

	// RAGSources are the RAGSource references for knowledge retrieval
	// +optional
	RAGSources []RAGSourceRef `json:"ragSources,omitempty"`

	// RAGAutoDiscover configures automatic RAGSource discovery by keyword matching.
	// When enabled, RAGSources whose status.topics overlap with the agent's keywords
	// are automatically wired without explicit ragSources entries.
	// +optional
	RAGAutoDiscover *RAGAutoDiscoverConfig `json:"ragAutoDiscover,omitempty"`

	// MCPServers are the MCPServer references for tools
	// +optional
	MCPServers []MCPServerRef `json:"mcpServers,omitempty"`

	// Memory defines conversation memory configuration
	// +optional
	Memory *MemoryConfig `json:"memory,omitempty"`

	// Deployment defines how the agent is deployed
	// +optional
	Deployment *DeploymentConfig `json:"deployment,omitempty"`
}

// AgentStatus defines the observed state of Agent
type AgentStatus struct {
	// Phase is the current phase (Pending, Deploying, Running, Error)
	Phase string `json:"phase,omitempty"`

	// Ready indicates if the agent is ready to accept requests
	Ready bool `json:"ready,omitempty"`

	// Endpoint is the agent API endpoint
	Endpoint string `json:"endpoint,omitempty"`

	// AvailableReplicas is the number of running replicas
	AvailableReplicas int32 `json:"availableReplicas,omitempty"`

	// RAGSourceStatus summarizes the status of referenced RAG sources
	// +optional
	RAGSourceStatus []RAGSourceRefStatus `json:"ragSourceStatus,omitempty"`

	// AutoDiscoveredRAGSources lists RAGSources matched via auto-discovery
	// +optional
	AutoDiscoveredRAGSources []RAGSourceRefStatus `json:"autoDiscoveredRAGSources,omitempty"`

	// MCPServerStatus summarizes the status of referenced MCP servers
	// +optional
	MCPServerStatus []MCPServerRefStatus `json:"mcpServerStatus,omitempty"`

	// Message provides additional status information
	Message string `json:"message,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// RAGSourceRefStatus tracks the status of a referenced RAGSource
type RAGSourceRefStatus struct {
	// Name is the RAGSource name
	Name string `json:"name"`
	// Ready indicates if the source is indexed and ready
	Ready bool `json:"ready"`
	// DocumentCount is the number of indexed documents
	DocumentCount int32 `json:"documentCount,omitempty"`
}

// MCPServerRefStatus tracks the status of a referenced MCPServer
type MCPServerRefStatus struct {
	// Name is the MCPServer name
	Name string `json:"name"`
	// Ready indicates if the server is available
	Ready bool `json:"ready"`
	// ToolCount is the number of available tools
	ToolCount int32 `json:"toolCount,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=agent
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.endpoint`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Agent is the Schema for the agents API
// An Agent orchestrates AI models, knowledge sources, and tools
type Agent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentSpec   `json:"spec,omitempty"`
	Status AgentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentList contains a list of Agent
type AgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Agent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Agent{}, &AgentList{})
}
