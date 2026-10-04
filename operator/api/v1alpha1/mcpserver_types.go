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

// MCPTransport defines the transport protocol for the MCP server
type MCPTransport string

const (
	// TransportHTTP uses HTTP for MCP communication
	TransportHTTP MCPTransport = "http"
	// TransportSSE uses Server-Sent Events for MCP communication
	TransportSSE MCPTransport = "sse"
	// TransportStdio uses stdio for MCP communication (sidecar pattern)
	TransportStdio MCPTransport = "stdio"
)

// MCPSecurityMode defines the pod security posture for the MCP server
type MCPSecurityMode string

const (
	// SecurityModeRelaxed runs the container without forcing runAsUser/runAsGroup.
	// Required for images built with uv (Python under /root/ mode 700).
	// Still enforces: no privilege escalation, drop ALL capabilities, seccomp.
	SecurityModeRelaxed MCPSecurityMode = "relaxed"
	// SecurityModeStrict enforces restricted PSS: runAsNonRoot, runAsUser:1000.
	// Use for images that don't require root filesystem access (e.g., Node.js).
	SecurityModeStrict MCPSecurityMode = "strict"
)

// SecretVolume defines a secret to mount as a volume
type SecretVolume struct {
	// Name is the name of the Kubernetes secret to mount
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// MountPath is the path where the secret will be mounted in the container
	// +kubebuilder:validation:Required
	MountPath string `json:"mountPath"`

	// SubPath optionally specifies a single key from the secret to mount as a file
	// If not specified, all keys in the secret are mounted as files
	// +optional
	SubPath string `json:"subPath,omitempty"`

	// ReadOnly specifies whether the volume should be mounted read-only
	// +kubebuilder:default=true
	// +optional
	ReadOnly *bool `json:"readOnly,omitempty"`
}

// EmptyDirVolume defines an emptyDir volume to mount (used to override baked-in configs)
type EmptyDirVolume struct {
	// MountPath is the path where the empty directory will be mounted
	// Use this to override/clear baked-in config files in MCP server images
	// +kubebuilder:validation:Required
	MountPath string `json:"mountPath"`

	// SizeLimit is the maximum size of the emptyDir volume
	// +optional
	SizeLimit string `json:"sizeLimit,omitempty"`
}

// ProxyInjectionConfig defines mcp-bridge injection settings for stdio transport
// When enabled, an init container copies the mcp-bridge binary to a shared volume,
// and the main container runs mcp-bridge which spawns the original command as a child process,
// bridging its stdio to HTTP/SSE endpoints
type ProxyInjectionConfig struct {
	// Enabled controls whether proxy injection is active
	// Defaults to true for stdio transport, false for http/sse
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Image specifies the mcp-bridge image containing the bridge binary
	// The image should have the bridge binary at /kubemoot-mcp-bridge
	// Defaults to the value from KubemootConfig if not set
	// +optional
	Image string `json:"image,omitempty"`

	// Port specifies the HTTP port for the bridge to listen on
	// +kubebuilder:default=8080
	// +optional
	Port int32 `json:"port,omitempty"`
}

// RegistryConfig configures how this MCPServer is registered with MCPGateways
type RegistryConfig struct {
	// Enabled controls whether this MCPServer registers with matching MCPGateways
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Categories are searchable tags for tool discovery (e.g., kubernetes, database)
	// +optional
	Categories []string `json:"categories,omitempty"`

	// AuthSecretRef references a secret containing auth credentials for this MCP server
	// The secret should have a key "bearer-token" or "api-key"
	// Used when the MCP server requires authentication from the gateway
	// +optional
	AuthSecretRef string `json:"authSecretRef,omitempty"`
}

// MCPServerSpec defines the desired state of MCPServer
type MCPServerSpec struct {
	// Image is the container image for the MCP server (for managed/private MCPs)
	// Either Image or ExternalEndpoint must be specified, but not both
	// +optional
	Image string `json:"image,omitempty"`

	// ExternalEndpoint is the URL of an external MCP server (for public/community MCPs)
	// Use this when connecting to pre-existing MCP servers without deploying them
	// Either Image or ExternalEndpoint must be specified, but not both
	// +optional
	ExternalEndpoint string `json:"externalEndpoint,omitempty"`

	// Command overrides the container entrypoint
	// +optional
	Command []string `json:"command,omitempty"`

	// Args are arguments passed to the MCP server
	// +optional
	Args []string `json:"args,omitempty"`

	// Transport specifies the MCP transport protocol (http, sse, stdio)
	// +kubebuilder:validation:Enum=http;sse;stdio
	// +kubebuilder:default=http
	// +optional
	Transport MCPTransport `json:"transport,omitempty"`

	// SecurityMode controls the pod security posture for the MCP server.
	// "relaxed" (default): no runAsUser/runAsGroup — required for Python/uv images
	// where the interpreter lives under /root/ (mode 700).
	// "strict": enforces restricted PSS (runAsNonRoot, runAsUser:1000) — use for
	// images that don't require root filesystem access (e.g., Node.js).
	// +kubebuilder:validation:Enum=relaxed;strict
	// +kubebuilder:default=relaxed
	// +optional
	SecurityMode MCPSecurityMode `json:"securityMode,omitempty"`

	// SecurityContext overrides fields of the MCP server container's security
	// context, on top of what securityMode sets (for example readOnlyRootFilesystem).
	// Fields left unset keep the securityMode values.
	// +optional
	SecurityContext *corev1.SecurityContext `json:"securityContext,omitempty"`

	// ProxyInjection configures automatic mcp-proxy injection for stdio servers
	// When transport is stdio, the operator injects an mcp-proxy sidecar that
	// bridges HTTP/SSE ↔ stdio, allowing stdio-based MCP servers to work in Kubernetes
	// +optional
	ProxyInjection *ProxyInjectionConfig `json:"proxyInjection,omitempty"`

	// Port is the port the MCP server listens on (for http/sse transport)
	// +kubebuilder:default=3000
	// +optional
	Port int32 `json:"port,omitempty"`

	// Replicas is the number of MCP server instances
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=2
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Env is a list of environment variables for the MCP server
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// SecretRef references a secret containing environment variables
	// +optional
	SecretRef string `json:"secretRef,omitempty"`

	// SecretVolumes mounts secrets as files in the container
	// Use this for config files (talosconfig, kubeconfig), certificates, or other file-based credentials
	// +optional
	SecretVolumes []SecretVolume `json:"secretVolumes,omitempty"`

	// EmptyDirVolumes mounts empty directories to override/clear baked-in configs
	// Use this when MCP server images contain stale configs that conflict with in-cluster auth
	// +optional
	EmptyDirVolumes []EmptyDirVolume `json:"emptyDirVolumes,omitempty"`

	// Sidecars are additional containers to run alongside the MCP server in the
	// same pod, rendered as modern native sidecars (initContainers with
	// restartPolicy=Always - the operator sets this regardless of what is
	// supplied). They share the pod's volumes (e.g. an EmptyDirVolume), so a
	// sidecar can stage data on a path the main container reads. Used by the
	// artifact-access materializer beside the no-network code sandbox.
	// +optional
	Sidecars []corev1.Container `json:"sidecars,omitempty"`

	// Resources defines the resource requirements for the MCP server
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// Capabilities is a list of capabilities this MCP server provides
	// +optional
	Capabilities []string `json:"capabilities,omitempty"`

	// HealthPath is the HTTP path for health checks
	// If not specified, TCP probes are used instead (suitable for MCP servers
	// that don't expose a health endpoint or use streaming protocols)
	// +optional
	HealthPath string `json:"healthPath,omitempty"`

	// ReadinessPath is the HTTP path for readiness checks
	// If not specified, uses HealthPath or TCP probes if HealthPath is also not set
	// +optional
	ReadinessPath string `json:"readinessPath,omitempty"`

	// ServiceAccountName is the Kubernetes service account for the MCP server pods
	// Required for MCP servers that need cluster access (e.g., kubernetes-mcp-server)
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// Registry configures how this MCPServer is registered with MCPGateways
	// +optional
	Registry *RegistryConfig `json:"registry,omitempty"`
}

// RegistryStatus tracks registration state with an MCPGateway
type RegistryStatus struct {
	// GatewayName is the name of the MCPGateway this server is registered with
	GatewayName string `json:"gatewayName"`

	// GatewayNamespace is the namespace of the MCPGateway
	GatewayNamespace string `json:"gatewayNamespace"`

	// Registered indicates successful registration with the gateway
	Registered bool `json:"registered"`

	// LastRegistration is when the server was last registered
	// +optional
	LastRegistration *metav1.Time `json:"lastRegistration,omitempty"`

	// Error contains the last registration error (if any)
	// +optional
	Error string `json:"error,omitempty"`
}

// MCPTool represents a tool provided by the MCP server
type MCPTool struct {
	// Name is the tool name
	Name string `json:"name"`

	// Description describes what the tool does
	// +optional
	Description string `json:"description,omitempty"`
}

// MCPServerStatus defines the observed state of MCPServer
type MCPServerStatus struct {
	// Phase is the current lifecycle phase (Pending, Deploying, Ready, Error)
	Phase string `json:"phase,omitempty"`

	// Ready indicates if the MCP server is ready to accept connections
	Ready bool `json:"ready,omitempty"`

	// Endpoint is the service endpoint for connecting to the MCP server
	Endpoint string `json:"endpoint,omitempty"`

	// Replicas is the current number of ready replicas
	Replicas int32 `json:"replicas,omitempty"`

	// AvailableReplicas is the number of available replicas
	AvailableReplicas int32 `json:"availableReplicas,omitempty"`

	// Tools is the list of tools discovered from the MCP server
	// +optional
	Tools []MCPTool `json:"tools,omitempty"`

	// Capabilities is the list of discovered capabilities
	// +optional
	Capabilities []string `json:"capabilities,omitempty"`

	// Message provides additional status information
	Message string `json:"message,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Discoverable indicates if this server is registered with at least one gateway
	// +optional
	Discoverable bool `json:"discoverable,omitempty"`

	// RegisteredWith lists gateways this MCPServer is registered with
	// +optional
	RegisteredWith []RegistryStatus `json:"registeredWith,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mcp
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`,priority=1
// +kubebuilder:printcolumn:name="Transport",type=string,JSONPath=`.spec.transport`
// +kubebuilder:printcolumn:name="Security",type=string,JSONPath=`.spec.securityMode`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.status.availableReplicas`
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.endpoint`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MCPServer is the Schema for the mcpservers API
type MCPServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPServerSpec   `json:"spec,omitempty"`
	Status MCPServerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MCPServerList contains a list of MCPServer
type MCPServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPServer `json:"items"`
}

func init() {
	registerTypes(&MCPServer{}, &MCPServerList{})
}
