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

// NotificationSinkSpec configures a webhook target that fires whenever an
// agent in the same namespace publishes a `concern` signal on a discussion
// thread. First release scope is intentionally narrow: only the `concern`
// messageType triggers, only one webhook per sink, no templating beyond a
// simple title string. The payload always carries the concern text, the
// agent name, the crew, the channel, and the threadId — enough for the
// recipient notification to stand on its own if the source thread is
// later deleted.
//
// Filtering (Channels, Agents) is optional. Empty filters match anything
// in the sink's namespace.
type NotificationSinkSpec struct {
	// Webhook is the HTTP target to POST/PUT notification payloads to.
	// +kubebuilder:validation:Required
	Webhook WebhookTarget `json:"webhook"`

	// Channels filters which discussion channels trigger this sink. Empty
	// means all channels in the sink's namespace.
	// +optional
	Channels []string `json:"channels,omitempty"`

	// Agents filters which agent names trigger this sink. Empty means
	// any agent's concern fires the sink.
	// +optional
	Agents []string `json:"agents,omitempty"`

	// Title overrides the default notification title. Empty resolves to
	// "Kubemoot · <agent> raised a concern".
	// +optional
	Title string `json:"title,omitempty"`

	// Priority is a passthrough hint for ntfy-compatible receivers
	// (one of: min, low, default, high, urgent). Sent as the
	// `X-Priority` header when set.
	// +kubebuilder:validation:Enum=min;low;default;high;urgent
	// +kubebuilder:default=default
	// +optional
	Priority string `json:"priority,omitempty"`
}

// WebhookTarget describes where to send the notification.
type WebhookTarget struct {
	// URL is the full target URL. For ntfy: https://ntfy.example.com/<topic>.
	// +kubebuilder:validation:Required
	URL string `json:"url"`

	// Method is the HTTP method. POST works for ntfy and generic JSON sinks.
	// +kubebuilder:validation:Enum=POST;PUT
	// +kubebuilder:default=POST
	// +optional
	Method string `json:"method,omitempty"`

	// Headers attached to every dispatch. Use ValueFrom for bearer tokens or
	// other credentials sourced from secrets — never inline them.
	// +optional
	Headers []HeaderEntry `json:"headers,omitempty"`
}

// HeaderEntry attaches a single HTTP header to webhook dispatches. Exactly
// one of Value or ValueFrom must be set.
type HeaderEntry struct {
	// Name of the header.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Value is the literal header value. Mutually exclusive with ValueFrom.
	// +optional
	Value string `json:"value,omitempty"`

	// ValueFrom sources the value from a secret or configmap. Mutually
	// exclusive with Value.
	// +optional
	ValueFrom *corev1.EnvVarSource `json:"valueFrom,omitempty"`
}

// NotificationSinkStatus reports observed state.
type NotificationSinkStatus struct {
	// Ready indicates the sink config is valid and the operator is
	// dispatching to it. False values surface the reason via Conditions.
	Ready bool `json:"ready,omitempty"`

	// LastFiredAt is the timestamp of the most recent successful dispatch.
	// +optional
	LastFiredAt *metav1.Time `json:"lastFiredAt,omitempty"`

	// LastFiredAgent is the agent name from the most recent dispatch — a
	// quick way to see what's actively raising concerns through this sink.
	// +optional
	LastFiredAgent string `json:"lastFiredAgent,omitempty"`

	// TotalDispatched is the lifetime count of successful POST/PUTs.
	// +optional
	TotalDispatched int64 `json:"totalDispatched,omitempty"`

	// TotalFailed is the lifetime count of failed dispatches (non-2xx,
	// timeout, DNS, etc).
	// +optional
	TotalFailed int64 `json:"totalFailed,omitempty"`

	// LastError captures the most recent failure message, if any.
	// +optional
	LastError string `json:"lastError,omitempty"`

	// Conditions represent the latest observations about the sink.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=notif
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.webhook.url`
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Dispatched",type=integer,JSONPath=`.status.totalDispatched`
// +kubebuilder:printcolumn:name="Failed",type=integer,JSONPath=`.status.totalFailed`
// +kubebuilder:printcolumn:name="LastFired",type=date,JSONPath=`.status.lastFiredAt`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NotificationSink is a namespace-scoped resource that dispatches a webhook
// whenever an agent in the same namespace publishes a `concern` signal.
// First release intentionally hardcodes the concern filter — block, advisory,
// and synthesis signals are out of scope until the team builds confidence
// in the dispatch path. Multiple sinks in one namespace are allowed; each
// is evaluated independently.
type NotificationSink struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NotificationSinkSpec   `json:"spec,omitempty"`
	Status NotificationSinkStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NotificationSinkList contains a list of NotificationSink.
type NotificationSinkList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NotificationSink `json:"items"`
}

func init() {
	registerTypes(&NotificationSink{}, &NotificationSinkList{})
}
