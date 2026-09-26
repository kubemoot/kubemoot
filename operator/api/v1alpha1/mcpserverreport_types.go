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

// MCPServerReportVerdict represents the assessed reliability of an MCP server
type MCPServerReportVerdict string

const (
	// VerdictUse means the server is working reliably and recommended
	VerdictUse MCPServerReportVerdict = "use"
	// VerdictCaution means the server has flaky or mixed results
	VerdictCaution MCPServerReportVerdict = "caution"
	// VerdictAvoid means the server is broken and should not be deployed
	VerdictAvoid MCPServerReportVerdict = "avoid"
	// VerdictUntested means no trials have been recorded yet
	VerdictUntested MCPServerReportVerdict = "untested"
)

// TrialPhase represents the phase at which a trial succeeded or failed
type TrialPhase string

const (
	TrialPhaseDeploy     TrialPhase = "deploy"
	TrialPhaseConnect    TrialPhase = "connect"
	TrialPhaseInitialize TrialPhase = "initialize"
	TrialPhaseDiscover   TrialPhase = "discover"
	TrialPhaseCall       TrialPhase = "call"
)

// TrialRecord represents one attempt to deploy/connect/use an MCP server version
type TrialRecord struct {
	// Version of the MCP server that was tested
	Version string `json:"version"`

	// Transport used for the trial (http, sse, stdio)
	// +kubebuilder:validation:Enum=http;sse;stdio
	Transport string `json:"transport"`

	// Image is the container image used (if applicable)
	// +optional
	Image string `json:"image,omitempty"`

	// TestedAt is when this trial was performed
	TestedAt *metav1.Time `json:"testedAt"`

	// Phase is where in the lifecycle the trial succeeded or failed
	// +kubebuilder:validation:Enum=deploy;connect;initialize;discover;call
	Phase string `json:"phase"`

	// Success indicates whether the trial passed
	Success bool `json:"success"`

	// ErrorMessage contains the error if the trial failed
	// +optional
	ErrorMessage string `json:"errorMessage,omitempty"`

	// ToolsFound is the number of tools discovered (for discover phase)
	// +optional
	ToolsFound int `json:"toolsFound,omitempty"`

	// SetupRecipe is a YAML snippet of the working configuration
	// +optional
	SetupRecipe string `json:"setupRecipe,omitempty"`
}

// MCPServerReportSpec defines the identity and admin curation for an MCP server report
type MCPServerReportSpec struct {
	// ServerName is the canonical name of the MCP server
	// +kubebuilder:validation:Required
	ServerName string `json:"serverName"`

	// GitHubURL is the GitHub repository URL
	// +optional
	GitHubURL string `json:"githubUrl,omitempty"`

	// RegistryType is the package registry (npm, docker, pip)
	// +optional
	RegistryType string `json:"registryType,omitempty"`

	// PackageIdentifier is the registry-specific package ID
	// +optional
	PackageIdentifier string `json:"packageIdentifier,omitempty"`

	// AdminVerdict allows a human curator to pin a verdict, overriding automated assessment
	// When set, the controller preserves this verdict regardless of trial outcomes
	// +kubebuilder:validation:Enum=use;caution;avoid
	// +optional
	AdminVerdict string `json:"adminVerdict,omitempty"`

	// AdminNotes is a human-authored explanation for the pinned verdict
	// +optional
	AdminNotes string `json:"adminNotes,omitempty"`

	// AdminAuthor identifies who pinned the verdict
	// +optional
	AdminAuthor string `json:"adminAuthor,omitempty"`
}

// MCPServerReportStatus defines the continuously updated learning data
type MCPServerReportStatus struct {
	// Verdict is the computed or admin-pinned reliability assessment
	// +kubebuilder:validation:Enum=use;caution;avoid;untested
	Verdict string `json:"verdict"`

	// RecommendedTransport is the transport that has proven most reliable
	// +optional
	RecommendedTransport string `json:"recommendedTransport,omitempty"`

	// RecommendedVersion is the most recent version that succeeded
	// +optional
	RecommendedVersion string `json:"recommendedVersion,omitempty"`

	// SuccessCount is the total number of successful trials
	SuccessCount int64 `json:"successCount"`

	// FailureCount is the total number of failed trials
	FailureCount int64 `json:"failureCount"`

	// SuccessRate is the computed success ratio (0.0-1.0)
	SuccessRate string `json:"successRate"`

	// LastTested is when the most recent trial was performed
	// +optional
	LastTested *metav1.Time `json:"lastTested,omitempty"`

	// LastSuccessful is when the most recent successful trial was performed
	// +optional
	LastSuccessful *metav1.Time `json:"lastSuccessful,omitempty"`

	// Trials is the recent trial history, capped at 20 entries
	// +optional
	Trials []TrialRecord `json:"trials,omitempty"`

	// ChroniclerNotes contains AI-generated analysis and recommendations
	// +optional
	ChroniclerNotes string `json:"chroniclerNotes,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mcprpt
// +kubebuilder:printcolumn:name="Server",type=string,JSONPath=`.spec.serverName`
// +kubebuilder:printcolumn:name="Verdict",type=string,JSONPath=`.status.verdict`
// +kubebuilder:printcolumn:name="Success%",type=string,JSONPath=`.status.successRate`
// +kubebuilder:printcolumn:name="Transport",type=string,JSONPath=`.status.recommendedTransport`
// +kubebuilder:printcolumn:name="LastTested",type=date,JSONPath=`.status.lastTested`
// +kubebuilder:printcolumn:name="AdminVerdict",type=string,JSONPath=`.spec.adminVerdict`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MCPServerReport tracks deployment and runtime experience for an MCP server.
// One report per server name accumulates version-specific trial records,
// enabling the operator to learn which servers work reliably and which to avoid.
type MCPServerReport struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPServerReportSpec   `json:"spec,omitempty"`
	Status MCPServerReportStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MCPServerReportList contains a list of MCPServerReport
type MCPServerReportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPServerReport `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MCPServerReport{}, &MCPServerReportList{})
}
