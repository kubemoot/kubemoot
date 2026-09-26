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

// StringMatcherType defines the type of string matching
type StringMatcherType string

const (
	// MatcherTypeExact performs exact string matching (case-insensitive)
	MatcherTypeExact StringMatcherType = "exact"
	// MatcherTypeGlob performs glob pattern matching
	MatcherTypeGlob StringMatcherType = "glob"
	// MatcherTypeRegex performs regular expression matching
	MatcherTypeRegex StringMatcherType = "regex"
)

// StringMatcher supports exact, glob, and regex matching
type StringMatcher struct {
	// Type of matching: exact, glob, or regex
	// +kubebuilder:validation:Enum=exact;glob;regex
	// +kubebuilder:default=exact
	// +optional
	Type StringMatcherType `json:"type,omitempty"`

	// Value to match against
	// +kubebuilder:validation:Required
	Value string `json:"value"`

	// Negate inverts the match result
	// +optional
	Negate bool `json:"negate,omitempty"`
}

// AllowingEntry defines MCPs that are accepted immediately without evaluation
// Use for essential infrastructure MCPs that the evaluator Agent depends on
// (e.g., GitHub MCP for fetching repo metrics during evaluation)
type AllowingEntry struct {
	// Name of the MCP server (exact match)
	// +optional
	Name string `json:"name,omitempty"`

	// Author of the MCP server (exact match)
	// +optional
	Author string `json:"author,omitempty"`
}

// BlockingEntry defines MCPs that are rejected immediately without evaluation
// Use for known bad actors, deprecated servers, or servers with security flaws
// Supports glob/regex matching and semver version constraints
type BlockingEntry struct {
	// Name matcher for server name
	// +optional
	Name *StringMatcher `json:"name,omitempty"`

	// Author matcher for server author/publisher
	// +optional
	Author *StringMatcher `json:"author,omitempty"`

	// Version constraint (semver range: >=1.2.8, ^2.0.0, ~1.5.0, 1.x, <2.0.0)
	// +optional
	Version string `json:"version,omitempty"`

	// Reason documents why this entry is blocked (for audit/review)
	// +optional
	Reason string `json:"reason,omitempty"`
}

// ConfigMapKeyRef references a key in a ConfigMap
type ConfigMapKeyRef struct {
	// Name of the ConfigMap
	Name string `json:"name"`
	// Key within the ConfigMap
	Key string `json:"key"`
}

// TestedConfig configures the "tested" evaluation tier that uses MCPServerReport data
type TestedConfig struct {
	// Enabled toggles tested-based evaluation
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled"`

	// MinSuccessRate is the minimum success rate to auto-allow a server (0.0-1.0)
	// +kubebuilder:default="0.8"
	// +optional
	MinSuccessRate string `json:"minSuccessRate,omitempty"`

	// BlockBroken automatically blocks servers with "avoid" verdict
	// +kubebuilder:default=true
	// +optional
	BlockBroken bool `json:"blockBroken,omitempty"`

	// PreferTested gives priority to servers with successful test history
	// +kubebuilder:default=true
	// +optional
	PreferTested bool `json:"preferTested,omitempty"`
}

// MCPQualityPolicySpec defines the desired state of MCPQualityPolicy
// Evaluation order: allowing → blocking → tested → considering (AI evaluation)
type MCPQualityPolicySpec struct {
	// Allowing entries - accepted immediately without evaluation
	// Use for essential infrastructure MCPs that the evaluator Agent depends on
	// (e.g., GitHub MCP for fetching repo metrics during quality evaluation)
	// +optional
	Allowing []AllowingEntry `json:"allowing,omitempty"`

	// Blocking entries - rejected immediately without evaluation
	// Use for known bad actors, deprecated servers, or servers with security flaws
	// +optional
	Blocking []BlockingEntry `json:"blocking,omitempty"`

	// Tested config - evaluation based on MCPServerReport trial history
	// Inserted between blocking and considering in the evaluation pipeline
	// +optional
	Tested *TestedConfig `json:"tested,omitempty"`

	// Considering config - AI-based evaluation for MCPs not in allowing/blocking
	// This is the DEFAULT behavior for all MCPs not explicitly allowed or blocked
	// +optional
	Considering *ConsideringConfig `json:"considering,omitempty"`
}

// ConsideringConfig defines AI-based evaluation for MCPs not in allowing/blocking lists
// Dogfoods the Kubemoot Agent for quality assessment
type ConsideringConfig struct {
	// Enabled toggles AI-based evaluation (default: true)
	// If disabled, MCPs not in allowing/blocking use FallbackAction
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// AgentRef references the quality evaluator Agent CR
	// +kubebuilder:validation:Required
	AgentRef string `json:"agentRef"`

	// CriteriaFromConfigMap loads evaluation criteria from a ConfigMap
	// +optional
	CriteriaFromConfigMap *ConfigMapKeyRef `json:"criteriaFromConfigMap,omitempty"`

	// Criteria inline evaluation criteria (alternative to ConfigMap)
	// +optional
	Criteria string `json:"criteria,omitempty"`

	// ConfidenceThreshold minimum confidence to accept AI decision (0.0-1.0)
	// +kubebuilder:default="0.7"
	// +optional
	ConfidenceThreshold string `json:"confidenceThreshold,omitempty"`

	// TimeoutSeconds for AI evaluation
	// +kubebuilder:default=30
	// +optional
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`

	// FallbackAction if AI unavailable, disabled, or below confidence threshold
	// +kubebuilder:validation:Enum=allow;deny
	// +kubebuilder:default=deny
	// +optional
	FallbackAction string `json:"fallbackAction,omitempty"`
}

// MCPQualityPolicyStatus defines the observed state of MCPQualityPolicy
type MCPQualityPolicyStatus struct {
	// Conditions represent the current state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastEvaluated timestamp
	// +optional
	LastEvaluated *metav1.Time `json:"lastEvaluated,omitempty"`

	// ServersEvaluated count
	ServersEvaluated int `json:"serversEvaluated,omitempty"`

	// ServersAllowed count
	ServersAllowed int `json:"serversAllowed,omitempty"`

	// ServersBlocked count
	ServersBlocked int `json:"serversBlocked,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=mqp
// +kubebuilder:printcolumn:name="AI",type=boolean,JSONPath=`.spec.considering.enabled`
// +kubebuilder:printcolumn:name="Fallback",type=string,JSONPath=`.spec.considering.fallbackAction`
// +kubebuilder:printcolumn:name="Evaluated",type=integer,JSONPath=`.status.serversEvaluated`
// +kubebuilder:printcolumn:name="Allowed",type=integer,JSONPath=`.status.serversAllowed`
// +kubebuilder:printcolumn:name="Blocked",type=integer,JSONPath=`.status.serversBlocked`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MCPQualityPolicy defines quality and trust filtering for MCP servers
type MCPQualityPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPQualityPolicySpec   `json:"spec,omitempty"`
	Status MCPQualityPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MCPQualityPolicyList contains a list of MCPQualityPolicy
type MCPQualityPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPQualityPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MCPQualityPolicy{}, &MCPQualityPolicyList{})
}
