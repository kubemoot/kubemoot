/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

// AskerRule is the one rule a crew's gateway applies to a person a trusted door asserts: who of
// the people the door signed in may ask this crew. The door only proves who is asking; this
// rule decides. Exactly one kind is set. Kubernetes tokens and service tokens keep their own
// checks, which this rule does not change.
// +kubebuilder:validation:XValidation:rule="(has(self.kubernetesRBAC) ? 1 : 0) + (has(self.connectedAccount) ? 1 : 0) + (has(self.issuerGroups) ? 1 : 0) + (has(self.anySignedIn) ? 1 : 0) + (has(self.external) ? 1 : 0) == 1",message="set exactly one of kubernetesRBAC, connectedAccount, issuerGroups, anySignedIn, external"
type AskerRule struct {
	// KubernetesRBAC maps the person to a Kubernetes user name and groups and asks the API
	// server with a SubjectAccessReview whether they may get this Crew. Use it for a crew only
	// a cluster's administrators ask. It is the only kind that calls the Kubernetes API for the
	// asker.
	// +optional
	KubernetesRBAC *KubernetesRBACAskers `json:"kubernetesRBAC,omitempty"`

	// ConnectedAccount admits every person the door signed in and names the account the crew's
	// tools act with, which this crew declares in spec.connectedAccounts. The outside service
	// decides entitlement on each call the crew's tools make. Today the gateway does not check
	// that the person has connected the account; leasing it per person is a later step.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`
	// +optional
	ConnectedAccount string `json:"connectedAccount,omitempty"`

	// IssuerGroups admits a person whose asserted groups, as the sign-in provider sent them
	// (with the door's group prefix), include at least one of these.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=256
	// +optional
	IssuerGroups []string `json:"issuerGroups,omitempty"`

	// AnySignedIn admits every person the door asserts.
	// +optional
	AnySignedIn *AnySignedInAskers `json:"anySignedIn,omitempty"`

	// External delegates the decision to a policy decision point the install runs, through the
	// OpenID AuthZEN Authorization API 1.0 access evaluation call.
	// +optional
	External *ExternalAskers `json:"external,omitempty"`
}

// KubernetesRBACAskers has no settings: RBAC on the Crew decides.
type KubernetesRBACAskers struct{}

// AnySignedInAskers has no settings: every asserted person is admitted.
type AnySignedInAskers struct{}

// ExternalAskers is a policy decision point that speaks the OpenID AuthZEN Authorization API
// 1.0 (OPA, Cerbos, OpenFGA and Topaz have adapters or implement it). For each question the
// gateway sends subject (the asserted user, with the groups as a property), action and
// resource (this Crew) and admits only a response of decision true. Any error, timeout or
// other answer refuses.
type ExternalAskers struct {
	// URL is the policy decision point's access evaluation endpoint, such as
	// https://pdp.example.org/access/v1/evaluation. It must be https, and its host must be one
	// the install lists as a trusted policy decision point (the chart's
	// discussionGateway.askerPolicyHosts); any other host is refused and reported on the Crew
	// as Authentication=False, reason UntrustedPolicyHost.
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^https://[^/?#\s]+(/[^?#\s]*)?$`
	URL string `json:"url"`

	// SubjectType is the type the subject is sent with.
	// +kubebuilder:default=user
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +optional
	SubjectType string `json:"subjectType,omitempty"`

	// ResourceType is the type the Crew is sent with. Its id is "<namespace>/<name>".
	// +kubebuilder:default=crew
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +optional
	ResourceType string `json:"resourceType,omitempty"`

	// Action is the action name sent.
	// +kubebuilder:default=ask
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Action string `json:"action,omitempty"`

	// TokenSecretRef names a Secret key, in the crew's namespace, with a bearer token the
	// gateway presents to the policy decision point. Without it no credential is sent.
	// +optional
	TokenSecretRef *SecretKeyReference `json:"tokenSecretRef,omitempty"`
}

// SecretKeyReference is a key of a Secret in the crew's namespace.
type SecretKeyReference struct {
	// Name is the Secret's name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Key is the key in the Secret.
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}
