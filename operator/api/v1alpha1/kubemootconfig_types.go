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

// ImageConfig defines the default container images for Kubemoot components
type ImageConfig struct {
	// Indexer is the default indexer image for RAGSource indexing jobs
	// +kubebuilder:default="ghcr.io/kubemoot/indexer:latest"
	// +optional
	Indexer string `json:"indexer,omitempty"`

	// QueryService is the default query service image for RAGSource
	// +kubebuilder:default="ghcr.io/kubemoot/query-service:latest"
	// +optional
	QueryService string `json:"queryService,omitempty"`

	// AgentRuntime is the default agent runtime image for Agent deployments
	// +kubebuilder:default="ghcr.io/kubemoot/agent-runtime:latest"
	// +optional
	AgentRuntime string `json:"agentRuntime,omitempty"`

	// AgentSidecar is the Moot Mesh sidecar image the operator adds to the pod of an Agent
	// whose harness mode is sidecar. The chart stamps it from the release; a floating tag
	// would break version pinning.
	// +kubebuilder:default="ghcr.io/kubemoot/agent-sidecar:latest"
	// +optional
	AgentSidecar string `json:"agentSidecar,omitempty"`

	// HarnessGoose is the harness-goose image: the harness shim and the goose binary. An Agent
	// whose harness is named goose and sets no image of its own runs it. The chart stamps it
	// from the release; a floating tag would break version pinning.
	// +kubebuilder:default="ghcr.io/kubemoot/harness-goose:latest"
	// +optional
	HarnessGoose string `json:"harnessGoose,omitempty"`

	// McpGateway is the default MCP Gateway image
	// +kubebuilder:default="ghcr.io/kubemoot/mcp-gateway:latest"
	// +optional
	McpGateway string `json:"mcpGateway,omitempty"`

	// McpBridge is the default MCP bridge image for stdio-to-HTTP proxy injection
	// +kubebuilder:default="ghcr.io/kubemoot/mcp-bridge:latest"
	// +optional
	McpBridge string `json:"mcpBridge,omitempty"`

	// DiscussionGateway is the default Discussion Gateway image
	// +kubebuilder:default="ghcr.io/kubemoot/discussion-gateway:latest"
	// +optional
	DiscussionGateway string `json:"discussionGateway,omitempty"`

	// DoclingServe is the docling-serve image for document source RAGSource indexing
	// +kubebuilder:default="quay.io/docling-project/docling-serve-cpu:latest"
	// +optional
	DoclingServe string `json:"doclingServe,omitempty"`

	// FitnessRunner is the default fitness runner image for CrewFitness test jobs
	// +kubebuilder:default="ghcr.io/kubemoot/fitness-runner:latest"
	// +optional
	FitnessRunner string `json:"fitnessRunner,omitempty"`

	// ArtifactAccess is the artifact-access image: the read-ops MCP server a crew
	// runs (ARTIFACT_MODE=mcp) to read the artifact store when a tool result spills
	// over the inline limit. An MCPServer selects it with spec.kubemootImage
	// instead of naming an image. The operator chart stamps it from the release;
	// there is no default, because a floating tag would break version pinning.
	// +optional
	ArtifactAccess string `json:"artifactAccess,omitempty"`
}

// DefaultConfig defines default configurations for Kubemoot components
type DefaultConfig struct {
	// ImagePullSecrets are the default image pull secrets for all operator-managed workloads
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// VectorStoreType is the default vector store type (pgvector, qdrant, milvus, chroma)
	// +kubebuilder:validation:Enum=pgvector;qdrant;milvus;chroma
	// +kubebuilder:default=pgvector
	// +optional
	VectorStoreType string `json:"vectorStoreType,omitempty"`

	// VectorStoreEndpoint is the default vector store endpoint for tool indexing
	// When empty, automatic tool indexing on MCPServers is disabled
	// Example: "homelab-pilot-db.homelab-pilot:5432/homelab_pilot"
	// +optional
	VectorStoreEndpoint string `json:"vectorStoreEndpoint,omitempty"`

	// EmbeddingModel is the default EmbeddingModel CR name referenced by RAGSources
	// +kubebuilder:default=nomic-embed
	// +optional
	EmbeddingModel string `json:"embeddingModel,omitempty"`

	// OTelCollectorEndpoint is the default OpenTelemetry collector endpoint for all agents
	// Agents use this as their tracing endpoint
	// +optional
	OTelCollectorEndpoint string `json:"otelCollectorEndpoint,omitempty"`

	// Scheduler configures the GPU-aware agent scheduler
	// +optional
	Scheduler *SchedulerConfig `json:"scheduler,omitempty"`

	// MaxIndexBuildsPerServer is the most RAGSource index builds that run at once against
	// one embedding server. Further builds wait in the Queued phase and start when a
	// build on that server finishes.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=2
	// +optional
	MaxIndexBuildsPerServer int32 `json:"maxIndexBuildsPerServer,omitempty"`
}

// SchedulerConfig configures the GPU-aware agent scheduling system
type SchedulerConfig struct {
	// Enabled turns on automatic agent scheduling. Default false.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Strategy is the scheduling strategy: "spread" (default) or "bin-pack"
	// +kubebuilder:validation:Enum=spread;bin-pack
	// +kubebuilder:default=spread
	// +optional
	Strategy string `json:"strategy,omitempty"`

	// ProbeIntervalSeconds is how often to probe servers for capacity. Default 30.
	// +kubebuilder:default=30
	// +optional
	ProbeIntervalSeconds int `json:"probeIntervalSeconds,omitempty"`

	// PrometheusEndpoint for DCGM metric queries
	// +optional
	PrometheusEndpoint string `json:"prometheusEndpoint,omitempty"`

	// PlacementPolicy is the realm default for how agents decide where each
	// model call runs: "algorithm" (the hand-written placement order),
	// "learned" (placement learned from observed performance), or "shadow"
	// (the algorithm places; the learned policy's choice is recorded beside
	// it). A crew's CrewSchedulingPolicy spec.placementPolicy overrides it.
	// Independent of Enabled.
	// +kubebuilder:validation:Enum=algorithm;learned;shadow
	// +kubebuilder:default=algorithm
	// +optional
	PlacementPolicy string `json:"placementPolicy,omitempty"`

	// PlacementService points agents at the placement service. When enabled,
	// agents call the service at its URL for placement and ticket ownership;
	// when off (the default), agents place in-process.
	// +optional
	PlacementService *PlacementServiceConfig `json:"placementService,omitempty"`
}

// PlacementServiceConfig configures agent use of the placement service.
type PlacementServiceConfig struct {
	// Enabled makes agents call the placement service. Default false.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// URL is the placement service base URL, for example
	// http://placement-service.kubemoot-system.svc:8080. Agents get no
	// service setting when it is empty.
	// +optional
	URL string `json:"url,omitempty"`
}

// KubemootConfigSpec defines the desired state of KubemootConfig
type KubemootConfigSpec struct {
	// Images defines the default container images for Kubemoot components
	// +optional
	Images ImageConfig `json:"images,omitempty"`

	// Defaults defines default configurations for Kubemoot components
	// +optional
	Defaults DefaultConfig `json:"defaults,omitempty"`
}

// KubemootConfigStatus defines the observed state of KubemootConfig
type KubemootConfigStatus struct {
	// Ready indicates if the configuration has been successfully applied
	Ready bool `json:"ready,omitempty"`

	// LastUpdated is the timestamp of the last configuration update
	// +optional
	LastUpdated *metav1.Time `json:"lastUpdated,omitempty"`

	// Message provides additional status information
	Message string `json:"message,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=wc
// +kubebuilder:printcolumn:name="Indexer",type=string,JSONPath=`.spec.images.indexer`,priority=1
// +kubebuilder:printcolumn:name="QueryService",type=string,JSONPath=`.spec.images.queryService`,priority=1
// +kubebuilder:printcolumn:name="AgentRuntime",type=string,JSONPath=`.spec.images.agentRuntime`,priority=1
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// KubemootConfig is the Schema for the kubemootconfigs API
// KubemootConfig is a cluster-scoped singleton that centralizes default images
// and configuration for all Kubemoot components. It eliminates hardcoded image
// versions in operator source code and enables GitOps-friendly version management.
type KubemootConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KubemootConfigSpec   `json:"spec,omitempty"`
	Status KubemootConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KubemootConfigList contains a list of KubemootConfig
type KubemootConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KubemootConfig `json:"items"`
}

func init() {
	registerTypes(&KubemootConfig{}, &KubemootConfigList{})
}
