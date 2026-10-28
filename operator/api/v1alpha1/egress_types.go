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

// EgressCIDR is an address range a workload may reach, less the ranges in Except.
type EgressCIDR struct {
	// CIDR is the range, for example 203.0.113.0/24.
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:XValidation:rule="isCIDR(self)",message="cidr must be a valid CIDR"
	CIDR string `json:"cidr"`

	// Except lists ranges inside CIDR that stay blocked.
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=64
	// +kubebuilder:validation:items:XValidation:rule="isCIDR(self)",message="except entries must be valid CIDRs"
	// +optional
	Except []string `json:"except,omitempty"`
}

// EgressService is an in-cluster Service a workload may reach. The operator reads the
// Service, and the rule it writes names the Service's namespace and pod selector and the
// port its pods listen on, which every CNI that enforces NetworkPolicy understands.
type EgressService struct {
	// Namespace is the Service's namespace. Empty means the workload's own namespace.
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Name is the Service's name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// Ports are the Service ports allowed, as the Service lists them (the port clients
	// dial, not the pod port). Empty allows every TCP port of the Service.
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:Minimum=1
	// +kubebuilder:validation:items:Maximum=65535
	// +optional
	Ports []int32 `json:"ports,omitempty"`
}

// EgressRules declares where a workload may connect. When the operator's network policy
// flag is on and a workload sets egress, the operator restricts that workload's outbound
// traffic to what Kubemoot always allows (DNS, the message bus, the crew's tool gateway,
// the model servers) plus what is listed here. A workload without egress is not restricted.
type EgressRules struct {
	// AllowedHosts are DNS names the workload may reach: "api.example.com", or a wildcard
	// in the leftmost label only, "*.example.com". No scheme, port, or path. Lower case.
	// A cluster with Cilium enforces host names; any other cluster allows the internet
	// outside the cluster's private ranges and reports EgressEnforced=False.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=253
	// +kubebuilder:validation:items:Pattern=`^(\*\.)?[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`
	// +optional
	AllowedHosts []string `json:"allowedHosts,omitempty"`

	// AllowedServices are Services inside the cluster the workload may reach. Use it for an
	// in-cluster destination in place of allowedHosts: a NetworkPolicy cannot name a host,
	// and a CNI that translates a Service to its pods before it applies policy can miss a
	// host rule. The Service must have a pod selector.
	// +kubebuilder:validation:MaxItems=32
	// +optional
	AllowedServices []EgressService `json:"allowedServices,omitempty"`

	// AllowedCIDRs are address ranges the workload may reach. Every CNI that enforces
	// NetworkPolicy enforces these.
	// +kubebuilder:validation:MaxItems=64
	// +optional
	AllowedCIDRs []EgressCIDR `json:"allowedCIDRs,omitempty"`

	// Ports are the TCP ports allowed to the hosts and ranges above. Default [443].
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:Minimum=1
	// +kubebuilder:validation:items:Maximum=65535
	// +optional
	Ports []int32 `json:"ports,omitempty"`

	// Bus allows the workload to reach the message bus (NATS), at the address the operator
	// itself is configured with. An Agent always reaches the bus; set it on a tool server
	// that reads or writes the bus, such as one that reads artifacts.
	// +optional
	Bus bool `json:"bus,omitempty"`

	// APIServer allows the workload to reach the Kubernetes API server. Set it on a tool
	// server that talks to the cluster, such as the Kubernetes MCP server.
	// +optional
	APIServer bool `json:"apiServer,omitempty"`
}

// CrewNetwork configures how a crew's discussion gateway is reached when the operator
// generates network policies.
type CrewNetwork struct {
	// AllowedIngress lists namespaces whose pods may reach the crew's discussion gateway,
	// for example the namespace of the web server that fronts the crew. Each entry is a
	// namespace selector; an empty selector matches every namespace.
	// +kubebuilder:validation:MaxItems=16
	// +optional
	AllowedIngress []metav1.LabelSelector `json:"allowedIngress,omitempty"`
}
