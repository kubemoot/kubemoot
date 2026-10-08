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

// MCPGatewayImplementation defines which gateway implementation to deploy
type MCPGatewayImplementation string

const (
	// ImplementationKubemoot uses the native Spring AI MCP Gateway (default)
	ImplementationKubemoot MCPGatewayImplementation = "kubemoot"
	// ImplementationContextForge uses IBM ContextForge MCP Gateway (legacy)
	ImplementationContextForge MCPGatewayImplementation = "contextforge"
	// ImplementationMicrosoft uses Microsoft MCP Gateway
	ImplementationMicrosoft MCPGatewayImplementation = "microsoft"
	// ImplementationDocker uses Docker MCP Gateway
	ImplementationDocker MCPGatewayImplementation = "docker"
)

// MCPGatewaySpec defines the desired state of MCPGateway
type MCPGatewaySpec struct {
	// Implementation selects which gateway to deploy
	// +kubebuilder:validation:Enum=kubemoot;contextforge;microsoft;docker
	// +kubebuilder:default=kubemoot
	// +optional
	Implementation MCPGatewayImplementation `json:"implementation,omitempty"`

	// MCPServerSelector selects which MCPServers to register with the gateway
	// If empty, all MCPServers in the namespace are registered
	// +optional
	MCPServerSelector *metav1.LabelSelector `json:"mcpServerSelector,omitempty"`

	// LogLevel sets the kubemoot gateway log level. Tool calls log at INFO.
	// +kubebuilder:validation:Enum=DEBUG;INFO;WARN
	// +kubebuilder:default=INFO
	// +optional
	LogLevel string `json:"logLevel,omitempty"`

	// Port is the port for the gateway service
	// +kubebuilder:default=8080
	// +optional
	Port int32 `json:"port,omitempty"`

	// Replicas is the number of gateway instances
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Auth configures authentication for the gateway
	// +optional
	Auth *MCPGatewayAuth `json:"auth,omitempty"`

	// AdminUI enables the gateway admin interface. Unset means enabled.
	// +kubebuilder:default=true
	// +optional
	AdminUI *bool `json:"adminUI,omitempty"`

	// Resources defines resource requirements for the gateway
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// ContextForge contains ContextForge-specific configuration
	// +optional
	ContextForge *ContextForgeConfig `json:"contextForge,omitempty"`

	// ImagePullSecrets for pulling the gateway image
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// Registries configures external MCP registry sources for tool discovery
	// +optional
	Registries *MCPRegistriesConfig `json:"registries,omitempty"`

	// ToolIndex configures the vector store for semantic tool search
	// +optional
	ToolIndex *ToolIndexConfig `json:"toolIndex,omitempty"`

	// MetaTools configures discovery mode meta-tools (search_tools, load_tools)
	// +optional
	MetaTools *MetaToolsConfig `json:"metaTools,omitempty"`

	// CredentialPolicies define credentials for dynamically discovered MCPs
	// First matching policy wins. Explicit MCPServer CRs always take priority.
	// +optional
	CredentialPolicies []CredentialPolicy `json:"credentialPolicies,omitempty"`

	// QualityPolicyRef references an MCPQualityPolicy for filtering dynamically discovered MCPs
	// +optional
	QualityPolicyRef string `json:"qualityPolicyRef,omitempty"`

	// CatalogRefs references MCPCatalog resources for server discovery
	// Catalogs are synced periodically and discovered servers are evaluated against QualityPolicyRef
	// +optional
	CatalogRefs []string `json:"catalogRefs,omitempty"`
}

// MCPRegistriesConfig configures external MCP registry sources
type MCPRegistriesConfig struct {
	// SyncInterval is how often to sync registries (e.g., "1h", "30m")
	// +kubebuilder:default="1h"
	// +optional
	SyncInterval string `json:"syncInterval,omitempty"`

	// Sources lists the MCP registries to sync from
	// +optional
	Sources []MCPRegistrySource `json:"sources,omitempty"`

	// IndexerImage is the container image for the catalog indexer
	// Defaults to kubemoot/indexer-spring:latest
	// +optional
	IndexerImage string `json:"indexerImage,omitempty"`
}

// MCPRegistrySource defines an external MCP registry
type MCPRegistrySource struct {
	// Name is a unique identifier for this registry
	Name string `json:"name"`

	// URL is the registry API endpoint
	URL string `json:"url"`

	// Type is the registry protocol type (mcp-run, smithery, custom)
	// +kubebuilder:validation:Enum=mcp-run;smithery;custom
	// +kubebuilder:default=mcp-run
	// +optional
	Type string `json:"type,omitempty"`

	// AuthSecretRef references a secret for registry authentication
	// +optional
	AuthSecretRef string `json:"authSecretRef,omitempty"`

	// Filter limits which tools to sync from this registry
	// +optional
	Filter *MCPRegistryFilter `json:"filter,omitempty"`
}

// MCPRegistryFilter filters tools from a registry
type MCPRegistryFilter struct {
	// Categories limits to specific tool categories
	// +optional
	Categories []string `json:"categories,omitempty"`

	// MinRating filters by minimum tool rating (as string, e.g., "4.0")
	// +optional
	MinRating string `json:"minRating,omitempty"`
}

// ToolIndexConfig configures the vector store for semantic search
type ToolIndexConfig struct {
	// VectorStore configures the vector database backend
	VectorStore GatewayVectorStoreConfig `json:"vectorStore"`

	// EmbeddingModel configures the model for generating embeddings
	EmbeddingModel GatewayEmbeddingModelConfig `json:"embeddingModel"`

	// Collection is the name of the vector store collection/table
	// +kubebuilder:default="mcp_tools"
	// +optional
	Collection string `json:"collection,omitempty"`
}

// GatewayVectorStoreConfig configures the vector database for MCPGateway
type GatewayVectorStoreConfig struct {
	// Type is the vector store backend (pgvector, qdrant, milvus)
	// +kubebuilder:validation:Enum=pgvector;qdrant;milvus
	// +kubebuilder:default=pgvector
	Type string `json:"type,omitempty"`

	// Host is the database host
	Host string `json:"host"`

	// Port is the database port
	// +kubebuilder:default=5432
	// +optional
	Port int32 `json:"port,omitempty"`

	// Database is the database name
	Database string `json:"database"`

	// SecretRef references credentials for the database
	SecretRef string `json:"secretRef"`
}

// GatewayEmbeddingModelConfig configures the embedding model for MCPGateway
type GatewayEmbeddingModelConfig struct {
	// Provider is the embedding provider (ollama, openai)
	// +kubebuilder:validation:Enum=ollama;openai
	// +kubebuilder:default=ollama
	// +optional
	Provider string `json:"provider,omitempty"`

	// Endpoint is the embedding API endpoint
	Endpoint string `json:"endpoint"`

	// Model is the embedding model name
	// +kubebuilder:default="nomic-embed-text"
	// +optional
	Model string `json:"model,omitempty"`

	// Dimensions is the embedding vector size
	// +kubebuilder:default=768
	// +optional
	Dimensions int `json:"dimensions,omitempty"`
}

// MetaToolsConfig configures discovery mode meta-tools
type MetaToolsConfig struct {
	// Enabled enables meta-tools for agents (search_tools, load_tools)
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// MaxResultsPerSearch limits results from search_tools
	// +kubebuilder:default=10
	// +optional
	MaxResultsPerSearch int `json:"maxResultsPerSearch,omitempty"`

	// Discovery configures on-demand tool discovery via mcp-catalog-discovery agent
	// +optional
	Discovery *DiscoveryConfig `json:"discovery,omitempty"`
}

// DiscoveryConfig configures on-demand tool discovery
type DiscoveryConfig struct {
	// Enabled enables triggering discovery when no tools match search query
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// AgentEndpoint is the URL of the mcp-catalog-discovery agent
	// +optional
	AgentEndpoint string `json:"agentEndpoint,omitempty"`
}

// CredentialPolicy defines credentials for a category of MCP servers
// When MCPGateway provisions a dynamic MCP server, it matches categories
// and applies the first matching policy
type CredentialPolicy struct {
	// Categories this policy applies to (e.g., ["kubernetes", "k8s"])
	// Use ["*"] as catch-all default
	// +kubebuilder:validation:MinItems=1
	Categories []string `json:"categories"`

	// Transport overrides the MCP transport protocol for matched servers
	// Use this when the registry doesn't provide transport info or you need to override it
	// Most npm/pip MCP servers use stdio; docker images may use http/sse
	// +kubebuilder:validation:Enum=http;sse;stdio
	// +optional
	Transport MCPTransport `json:"transport,omitempty"`

	// ServiceAccountName for RBAC-based authentication
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// SecretRef references a Secret for environment variables
	// +optional
	SecretRef string `json:"secretRef,omitempty"`

	// SecretVolumes to mount as files (for file-based credentials)
	// +optional
	SecretVolumes []SecretVolume `json:"secretVolumes,omitempty"`

	// EmptyDirVolumes to mount (for clearing baked-in configs)
	// +optional
	EmptyDirVolumes []EmptyDirVolume `json:"emptyDirVolumes,omitempty"`
}

// MCPGatewayAuth configures authentication for the gateway
type MCPGatewayAuth struct {
	// Enabled requires authentication for gateway access
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Type is the authentication type (jwt, basic, none)
	// +kubebuilder:validation:Enum=jwt;basic;none
	// +kubebuilder:default=none
	// +optional
	Type string `json:"type,omitempty"`

	// JWTSecretRef references a secret containing the JWT signing key
	// The secret should have a key named "jwt-secret"
	// +optional
	JWTSecretRef string `json:"jwtSecretRef,omitempty"`

	// BasicAuth configures basic authentication
	// +optional
	BasicAuth *BasicAuthConfig `json:"basicAuth,omitempty"`
}

// BasicAuthConfig defines basic auth credentials
type BasicAuthConfig struct {
	// SecretRef references a secret containing username and password
	// The secret should have keys "username" and "password"
	// +optional
	SecretRef string `json:"secretRef,omitempty"`

	// Username for basic auth (alternative to secretRef)
	// +optional
	Username string `json:"username,omitempty"`

	// Password for basic auth (alternative to secretRef, not recommended)
	// +optional
	Password string `json:"password,omitempty"`
}

// ContextForgeConfig contains IBM ContextForge-specific settings
type ContextForgeConfig struct {
	// Image overrides the default ContextForge image
	// +kubebuilder:default="ghcr.io/ibm/mcp-context-forge:1.0.0-BETA-1"
	// +optional
	Image string `json:"image,omitempty"`

	// DatabaseURL is the database connection string
	// Default uses SQLite in-container
	// +optional
	DatabaseURL string `json:"databaseURL,omitempty"`

	// RedisURL enables Redis for caching and federation
	// +optional
	RedisURL string `json:"redisURL,omitempty"`

	// EnableFederation allows multiple gateways to share state
	// +optional
	EnableFederation bool `json:"enableFederation,omitempty"`

	// TLSEnabled enables HTTPS for the gateway
	// +optional
	TLSEnabled bool `json:"tlsEnabled,omitempty"`

	// TLSSecretRef references a secret containing TLS cert/key
	// +optional
	TLSSecretRef string `json:"tlsSecretRef,omitempty"`

	// AdminAPISecretRef references a secret containing the bearer token for Admin API
	// The secret should have a key "bearer-token"
	// Required for registering/unregistering MCPServers via Admin API
	// +optional
	AdminAPISecretRef string `json:"adminAPISecretRef,omitempty"`
}

// MCPGatewayStatus defines the observed state of MCPGateway
type MCPGatewayStatus struct {
	// Phase is the current lifecycle phase (Pending, Deploying, Indexing, Ready, Error)
	Phase string `json:"phase,omitempty"`

	// Ready indicates if the gateway is ready to accept connections
	Ready bool `json:"ready,omitempty"`

	// Endpoint is the gateway service endpoint
	Endpoint string `json:"endpoint,omitempty"`

	// AdminEndpoint is the admin UI endpoint (if enabled)
	AdminEndpoint string `json:"adminEndpoint,omitempty"`

	// ToolIndexEndpoint is the URL of the RAGSource query service for semantic tool search
	// This endpoint is used by the gateway to search for tools matching user intent
	// +optional
	ToolIndexEndpoint string `json:"toolIndexEndpoint,omitempty"`

	// RegisteredServers is the count of MCPServers registered with the gateway
	RegisteredServers int `json:"registeredServers,omitempty"`

	// MCPServers lists the names of registered MCPServers
	// +optional
	MCPServers []string `json:"mcpServers,omitempty"`

	// AvailableReplicas is the number of ready gateway replicas
	AvailableReplicas int32 `json:"availableReplicas,omitempty"`

	// Message provides additional status information
	Message string `json:"message,omitempty"`

	// CatalogSync tracks the status of MCP catalog indexing
	// +optional
	CatalogSync *CatalogSyncStatus `json:"catalogSync,omitempty"`

	// ToolIndex tracks the tool index status
	// +optional
	ToolIndex *ToolIndexStatus `json:"toolIndex,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// CatalogSyncStatus tracks catalog synchronization progress
type CatalogSyncStatus struct {
	// LastSync is the timestamp of the last successful sync
	// +optional
	LastSync *metav1.Time `json:"lastSync,omitempty"`

	// JobName is the name of the current/last indexer Job
	// +optional
	JobName string `json:"jobName,omitempty"`

	// JobStatus is the status of the indexer Job (Running, Succeeded, Failed)
	// +optional
	JobStatus string `json:"jobStatus,omitempty"`

	// RegistrySyncStatus tracks per-registry sync status
	// +optional
	RegistrySyncStatus []RegistrySyncStatus `json:"registrySyncStatus,omitempty"`
}

// RegistrySyncStatus tracks sync status for a single registry
type RegistrySyncStatus struct {
	// Name is the registry name
	Name string `json:"name"`

	// LastSync is when this registry was last synced
	// +optional
	LastSync *metav1.Time `json:"lastSync,omitempty"`

	// ToolCount is the number of tools indexed from this registry
	ToolCount int `json:"toolCount,omitempty"`

	// Checksum is the content hash for change detection
	// +optional
	Checksum string `json:"checksum,omitempty"`

	// Status is the sync status (Synced, Error, Pending)
	Status string `json:"status,omitempty"`
}

// ToolIndexStatus tracks the tool index state
type ToolIndexStatus struct {
	// TotalTools is the total number of indexed tools
	TotalTools int `json:"totalTools,omitempty"`

	// IndexedAt is when the index was last updated
	// +optional
	IndexedAt *metav1.Time `json:"indexedAt,omitempty"`

	// Ready indicates if the tool index is ready for queries
	Ready bool `json:"ready,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mcpgw
// +kubebuilder:printcolumn:name="Implementation",type=string,JSONPath=`.spec.implementation`
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Servers",type=integer,JSONPath=`.status.registeredServers`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.endpoint`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MCPGateway is the Schema for the mcpgateways API
// MCPGateway deploys and manages an MCP gateway that routes requests to MCPServers
type MCPGateway struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPGatewaySpec   `json:"spec,omitempty"`
	Status MCPGatewayStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MCPGatewayList contains a list of MCPGateway
type MCPGatewayList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPGateway `json:"items"`
}

func init() {
	registerTypes(&MCPGateway{}, &MCPGatewayList{})
}
