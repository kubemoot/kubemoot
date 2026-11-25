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

// ConnectedAccountUsage says whether a crew can answer without the person's account.
// +kubebuilder:validation:Enum=Required;Optional
type ConnectedAccountUsage string

const (
	// ConnectedAccountRequired means the crew does not start a discussion until the asking
	// person has connected the account.
	ConnectedAccountRequired ConnectedAccountUsage = "Required"
	// ConnectedAccountOptional means the crew's tools fall back or skip when the person has
	// not connected the account.
	ConnectedAccountOptional ConnectedAccountUsage = "Optional"
)

// ConnectedAccount is an outside service whose account a person connects once, so the crew's
// tools act as that person there. The crew declares what it needs and where the service's
// sign-in lives; the install registers its own client with the service, so nothing here
// names an install.
// +kubebuilder:validation:XValidation:rule="!has(self.revocationURL) || self.revocationURL.matches('^https://[^/?#[:space:]]+')",message="revocationURL must be https with a host"
type ConnectedAccount struct {
	// Name keys the account across the install: a crew and an MCPServer's
	// registry.callerCredential.account name it, and two crews that declare one name declare
	// one service. It is a DNS label.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`
	Name string `json:"name"`

	// DisplayName is the service's name as the person sees it on the consent page and in an
	// answer that asks them to connect.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	DisplayName string `json:"displayName"`

	// Purpose is one sentence shown on the consent page: what the crew does with the account.
	// +kubebuilder:validation:MaxLength=300
	// +optional
	Purpose string `json:"purpose,omitempty"`

	// Usage is Required (the default: asking the crew needs the account connected) or
	// Optional (the crew's tools fall back or skip without it).
	// +kubebuilder:default=Required
	// +optional
	Usage ConnectedAccountUsage `json:"usage,omitempty"`

	// Scopes are the OAuth scopes the crew needs at the service.
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=128
	// +optional
	Scopes []string `json:"scopes,omitempty"`

	// AuthorizeURL is the service's OAuth authorization endpoint. It must be https.
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:XValidation:rule="self.matches('^https://[^/?#[:space:]]+')",message="authorizeURL must be https with a host"
	AuthorizeURL string `json:"authorizeURL"`

	// TokenURL is the service's OAuth token endpoint. It must be https.
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:XValidation:rule="self.matches('^https://[^/?#[:space:]]+')",message="tokenURL must be https with a host"
	TokenURL string `json:"tokenURL"`

	// RevocationURL is the service's RFC 7009 revocation endpoint. When set, disconnecting
	// the account revokes the token there. It must be https.
	// +kubebuilder:validation:MaxLength=512
	// +optional
	RevocationURL string `json:"revocationURL,omitempty"`

	// TokenHosts are the hosts the account's token may be sent to. A call to any other host
	// is refused, so a tool cannot send the token elsewhere. At least one is required.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=253
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	TokenHosts []string `json:"tokenHosts"`
}
