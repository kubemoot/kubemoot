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

// MCPCatalogType defines the type of MCP catalog and how to discover servers from it
type MCPCatalogType string

const (
	// CatalogTypeOfficialRegistry uses the official MCP registry API
	// https://registry.modelcontextprotocol.io/v0/servers
	// Schema: https://static.modelcontextprotocol.io/schemas/2025-07-09/server.schema.json
	CatalogTypeOfficialRegistry MCPCatalogType = "official-registry"

	// CatalogTypeSmithery uses the Smithery catalog API
	CatalogTypeSmithery MCPCatalogType = "smithery"

	// CatalogTypeGlama uses the Glama directory API
	CatalogTypeGlama MCPCatalogType = "glama"

	// CatalogTypeDocker uses Docker Hub with mcp/ namespace convention
	CatalogTypeDocker MCPCatalogType = "docker"

	// CatalogTypeNpm uses npm registry for @modelcontextprotocol packages
	CatalogTypeNpm MCPCatalogType = "npm"

	// CatalogTypeAgent uses a ReAct agent to navigate and discover servers
	// Required for catalogs without standardized APIs (the "wild west")
	CatalogTypeAgent MCPCatalogType = "agent"
)

// MCPCatalogSpec defines the desired state of MCPCatalog
type MCPCatalogSpec struct {
	// Type specifies the catalog type and discovery mechanism
	// +kubebuilder:validation:Enum=official-registry;smithery;glama;docker;npm;agent
	// +kubebuilder:validation:Required
	Type MCPCatalogType `json:"type"`

	// URL is the catalog entry point
	// For standard types: the API endpoint
	// For agent type: the starting URL for navigation
	// +kubebuilder:validation:Required
	URL string `json:"url"`

	// AgentRef references a ReAct Agent CR for discovery (required for type: agent)
	// The agent navigates the catalog, extracts server info, and finds GitHub links
	// +optional
	AgentRef string `json:"agentRef,omitempty"`

	// Queries specifies capabilities to search for (optional)
	// e.g., ["kubernetes", "database", "filesystem"]
	// If empty, discovers all available servers
	// +optional
	Queries []string `json:"queries,omitempty"`

	// SyncInterval is how often to re-discover servers from this catalog
	// +kubebuilder:default="24h"
	// +optional
	SyncInterval string `json:"syncInterval,omitempty"`

	// MaxServers limits the number of servers to discover from this catalog
	// +kubebuilder:default=100
	// +optional
	MaxServers int `json:"maxServers,omitempty"`

	// Auth configures authentication for the catalog API
	// +optional
	Auth *CatalogAuth `json:"auth,omitempty"`

	// QualityPolicyRef references an MCPQualityPolicy to evaluate discovered servers
	// If not specified, uses the MCPGateway's qualityPolicyRef
	// +optional
	QualityPolicyRef string `json:"qualityPolicyRef,omitempty"`
}

// CatalogAuth configures authentication for catalog APIs
type CatalogAuth struct {
	// SecretRef references a secret containing auth credentials
	// Expected keys depend on auth type: "token", "api-key", "username", "password"
	// +optional
	SecretRef string `json:"secretRef,omitempty"`
}

// DiscoveredServer represents an MCP server discovered from a catalog
type DiscoveredServer struct {
	// Name is the server name (e.g., "io.github.feiskyer/mcp-kubernetes-server")
	Name string `json:"name"`

	// Description of the server's capabilities
	// +optional
	Description string `json:"description,omitempty"`

	// Version of the server
	// +optional
	Version string `json:"version,omitempty"`

	// Author/publisher of the server
	// +optional
	Author string `json:"author,omitempty"`

	// GitHubURL is the source repository (derived from name or extracted by agent)
	// +optional
	GitHubURL string `json:"githubUrl,omitempty"`

	// RegistryType is the package registry (npm, docker, pip, etc.)
	// +optional
	RegistryType string `json:"registryType,omitempty"`

	// PackageIdentifier is the package name in the registry
	// +optional
	PackageIdentifier string `json:"packageIdentifier,omitempty"`

	// Transport is the MCP transport type (stdio, sse, http)
	// +optional
	Transport string `json:"transport,omitempty"`

	// Categories/tags for the server
	// +optional
	Categories []string `json:"categories,omitempty"`

	// QualityDecision is the result of quality policy evaluation
	// +optional
	QualityDecision string `json:"qualityDecision,omitempty"`

	// QualityReason explains the quality decision
	// +optional
	QualityReason string `json:"qualityReason,omitempty"`
}

// MCPCatalogStatus defines the observed state of MCPCatalog
type MCPCatalogStatus struct {
	// Phase is the current lifecycle phase (Syncing, Ready, Error)
	Phase string `json:"phase,omitempty"`

	// LastSync is when the catalog was last synchronized
	// +optional
	LastSync *metav1.Time `json:"lastSync,omitempty"`

	// NextSync is when the next sync is scheduled
	// +optional
	NextSync *metav1.Time `json:"nextSync,omitempty"`

	// ServersDiscovered is the total number of servers found
	ServersDiscovered int `json:"serversDiscovered,omitempty"`

	// ServersAllowed is the number passing quality policy
	ServersAllowed int `json:"serversAllowed,omitempty"`

	// ServersBlocked is the number blocked by quality policy
	ServersBlocked int `json:"serversBlocked,omitempty"`

	// DiscoveredServers lists the servers found (summary, not full list)
	// +optional
	DiscoveredServers []DiscoveredServer `json:"discoveredServers,omitempty"`

	// Message provides additional status information
	// +optional
	Message string `json:"message,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=mcpcat
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.url`,priority=1
// +kubebuilder:printcolumn:name="Discovered",type=integer,JSONPath=`.status.serversDiscovered`
// +kubebuilder:printcolumn:name="Allowed",type=integer,JSONPath=`.status.serversAllowed`
// +kubebuilder:printcolumn:name="Blocked",type=integer,JSONPath=`.status.serversBlocked`
// +kubebuilder:printcolumn:name="Last Sync",type=date,JSONPath=`.status.lastSync`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MCPCatalog defines an MCP server catalog source for discovery
type MCPCatalog struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPCatalogSpec   `json:"spec,omitempty"`
	Status MCPCatalogStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MCPCatalogList contains a list of MCPCatalog
type MCPCatalogList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPCatalog `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MCPCatalog{}, &MCPCatalogList{})
}
