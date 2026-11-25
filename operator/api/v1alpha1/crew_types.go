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

// DiscussionConfig configures the crew's discussion gateway — the HTTP entry
// point for external clients to start discussions and stream agent signals.
// +kubebuilder:validation:XValidation:rule="!has(self.identity) || !has(self.identity.exposeToAgents) || !self.identity.exposeToAgents || has(self.access)",message="identity.exposeToAgents needs access: a gateway that verifies no one has no caller to describe"
type DiscussionConfig struct {
	// Enabled controls whether a discussion gateway is deployed for this crew.
	// Unset means enabled.
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Resources defines resource requirements for the gateway pod
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// Access declares who may call the gateway. Unset, the gateway accepts every caller
	// as anonymous, as it always has, and the Crew reports Authentication=False with reason
	// Anonymous. Set, the gateway verifies each caller's Kubernetes token, requires get on
	// the Crew, records the caller as the owner of the conversations they start, and shows
	// each caller only their own.
	// +optional
	Access *DiscussionAccess `json:"access,omitempty"`

	// Identity says what the crew's agents are told about the caller. Unset, agents are told
	// nothing: the caller's opaque handle reaches tool calls as data and never a prompt.
	// +optional
	Identity *DiscussionIdentity `json:"identity,omitempty"`
}

// DiscussionIdentity is what a crew lets its agents know of the person asking. It needs
// spec.discussion.access: a gateway that verifies no one has no caller to describe.
type DiscussionIdentity struct {
	// ExposeToAgents puts the caller in each agent's turn context: the opaque handle (the
	// hashed principal id) and, for a Kubernetes caller, the user name as a label. Off by
	// default, so no prompt changes and no name reaches an agent or the bus. The label is only
	// stamped on the question when this is on, and is never stored with the conversation.
	// +kubebuilder:default=false
	// +optional
	ExposeToAgents bool `json:"exposeToAgents,omitempty"`
}

// DiscussionAccess is how a crew's discussion gateway knows who is asking. Setting it turns
// verification on; a caller then sends their Kubernetes token as "Authorization: Bearer".
// A caller needs get on the Crew to use it, and get or list on crews/conversations (the
// ClusterRole kubemoot-conversation-reader) to read other callers' conversations.
// +kubebuilder:validation:XValidation:rule="!has(self.serviceTokens) || size(self.serviceTokens) == 0 || has(self.hashKeySecretRef)",message="serviceTokens needs hashKeySecretRef"
// +kubebuilder:validation:XValidation:rule="!has(self.serviceTokens) || size(self.serviceTokens) < 2 || self.serviceTokens.all(t, has(t.audiences) && size(t.audiences) > 0)",message="with more than one serviceTokens entry each needs audiences"
type DiscussionAccess struct {
	// TokenReview makes the gateway check Kubernetes bearer tokens with a TokenReview and
	// a SubjectAccessReview.
	// +kubebuilder:default=true
	// +optional
	TokenReview *bool `json:"tokenReview,omitempty"`

	// AllowAnonymous lets a caller with no token ask as the anonymous caller. A token that is present and not valid is always refused.
	// +kubebuilder:default=false
	// +optional
	AllowAnonymous *bool `json:"allowAnonymous,omitempty"`

	// TokenAudiences are the audiences a token must be bound to, so a token minted for the
	// API server alone is not accepted. Empty accepts the API server's own audience.
	// +optional
	TokenAudiences []string `json:"tokenAudiences,omitempty"`

	// ServiceTokens are services whose acceptance of a person's own bearer token proves who
	// that person is, so a chat app signed in to the service can ask the crew as that person.
	// Empty by default: no service token is accepted. With more than one entry each needs
	// audiences, so a token is only ever tried against the service it was issued for.
	// +kubebuilder:validation:MaxItems=8
	// +listType=map
	// +listMapKey=name
	// +optional
	ServiceTokens []ServiceTokenVerifier `json:"serviceTokens,omitempty"`

	// HashKeySecretRef names the Secret key, in the crew's namespace, that holds the install's
	// hash key. The gateway uses it to turn a person's account id into the opaque principal id
	// the bus carries, the same key in every door (the liaison's chart takes the same Secret).
	// Required while serviceTokens is set; the installer creates the Secret.
	// +optional
	HashKeySecretRef *HashKeySecretRef `json:"hashKeySecretRef,omitempty"`
}

// HashKeySecretRef is a key of a Secret in the crew's namespace.
type HashKeySecretRef struct {
	// Name is the Secret's name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Key is the key in the Secret that holds the hash key.
	// +kubebuilder:default=hash-key
	// +optional
	Key string `json:"key,omitempty"`
}

// ServiceTokenVerifier is one service that vouches for a person by accepting their own
// bearer token. The gateway tries the token against VerifyURL and reads the person's id from
// the token's payload; it never reads an identity from anything the caller says.
// +kubebuilder:validation:XValidation:rule="self.authorize != 'Subjects' || (has(self.subjects) && size(self.subjects) > 0)",message="authorize Subjects needs at least one subject"
// +kubebuilder:validation:XValidation:rule="self.authorize == 'Subjects' || !has(self.subjects) || size(self.subjects) == 0",message="subjects are only used with authorize Subjects"
type ServiceTokenVerifier struct {
	// Name is the issuer of the people this service vouches for, a DNS label. It appears in
	// the principal, so it cannot change without changing every person's id.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self != 'kubernetes'",message="kubernetes is the issuer of Kubernetes tokens"
	Name string `json:"name"`

	// VerifyURL is the service endpoint the token is tried against. https only.
	// +kubebuilder:validation:Pattern=`^https://[^\s/]+(/\S*)?$`
	VerifyURL string `json:"verifyURL"`

	// VerifyMethod is how the token is tried: McpInitialize posts an MCP initialize request.
	// +kubebuilder:validation:Enum=McpInitialize
	// +kubebuilder:default=McpInitialize
	// +optional
	VerifyMethod string `json:"verifyMethod,omitempty"`

	// SubjectClaim is the dotted path in the token's payload that names the person,
	// for example upstream_claims.sub.
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_]+(\.[A-Za-z0-9_]+)*$`
	// +kubebuilder:validation:MaxLength=256
	SubjectClaim string `json:"subjectClaim"`

	// Audiences, when set, are the values the token's aud must have one of.
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Audiences []string `json:"audiences,omitempty"`

	// MaxCacheSeconds bounds how long an accepted token is remembered, never beyond its own
	// expiry. Empty means 300.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=900
	// +kubebuilder:default=300
	// +optional
	MaxCacheSeconds int32 `json:"maxCacheSeconds,omitempty"`

	// PassToTools lets the gateway keep the accepted token in memory for the turn, so a tool
	// server registered with callerCredential Required can call the service as the person.
	// +kubebuilder:default=false
	// +optional
	PassToTools bool `json:"passToTools,omitempty"`

	// Authorize says who the crew accepts: AnyAccepted lets every person the service accepts
	// use the crew, Subjects only the listed ones.
	// +kubebuilder:validation:Enum=AnyAccepted;Subjects
	// +kubebuilder:default=AnyAccepted
	// +optional
	Authorize string `json:"authorize,omitempty"`

	// Subjects are the raw account ids allowed when authorize is Subjects.
	// +kubebuilder:validation:MaxItems=256
	// +optional
	Subjects []string `json:"subjects,omitempty"`
}

// TokenReviewEnabled reports whether Kubernetes tokens are checked: on unless set off.
func (a *DiscussionAccess) TokenReviewEnabled() bool { return a.TokenReview == nil || *a.TokenReview }

// AnonymousAllowed reports whether a caller with no token is accepted: off unless set on.
func (a *DiscussionAccess) AnonymousAllowed() bool {
	return a.AllowAnonymous != nil && *a.AllowAnonymous
}

// CrewSpec defines the desired state of Crew
type CrewSpec struct {
	// Description is a human-readable description of the crew's purpose
	// +optional
	Description string `json:"description,omitempty"`

	// Discussion configures the crew's discussion gateway
	// +optional
	Discussion *DiscussionConfig `json:"discussion,omitempty"`

	// Memory configures the crew's working memory — facts the crew learns at
	// runtime (e.g. discovered label/topology mappings) and recalls later.
	// +optional
	Memory *CrewMemoryConfig `json:"memory,omitempty"`

	// Retention declares how long the crew's data is kept in NATS: conversations, working
	// memory and the crew's other messages. The operator removes what has expired on the
	// declared schedule. Unset fields take the defaults on this schema.
	// +kubebuilder:default={}
	// +optional
	Retention *CrewRetention `json:"retention,omitempty"`

	// Network configures who may reach the crew's discussion gateway when the operator
	// generates network policies.
	// +optional
	Network *CrewNetwork `json:"network,omitempty"`

	// Workspace declares the default workspace access of the crew's agents. An Agent's own
	// workspace.access overrides it.
	// +optional
	Workspace *CrewWorkspace `json:"workspace,omitempty"`

	// DefaultTimeZone is the IANA time zone (such as America/New_York) in which the crew
	// states today's date when the asker's client sent no zone. Without it the crew uses UTC.
	// A name that is not in the IANA time zone database is ignored at runtime.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	DefaultTimeZone string `json:"defaultTimeZone,omitempty"`

	// ConnectedAccounts are the outside services whose accounts a person connects so this
	// crew's tools act as them. Each name is unique within the crew, and an MCPServer takes its
	// per-person token from one with registry.callerCredential.account.
	// +kubebuilder:validation:MaxItems=16
	// +listType=map
	// +listMapKey=name
	// +optional
	ConnectedAccounts []ConnectedAccount `json:"connectedAccounts,omitempty"`
}

// CrewMemoryConfig governs a crew's working-memory storage, injection, and GC.
// (Distinct from the Agent's MemoryConfig, which is per-agent conversation
// memory.) Storage is cheap (NATS KV); injection into agent context is
// expensive (tokens + tool-calling degradation) — hence separate, generous
// storage caps vs a small injection limit. Defaults suit a typical crew.
type CrewMemoryConfig struct {
	// Enabled turns crew working memory on. When false, agents neither recall
	// nor persist facts. Pointer so an explicit false is distinguishable from
	// unset (a plain bool + omitempty drops false and can't disable).
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// MaxFacts is the per-crew storage cap. On write, least-recently-used facts
	// are evicted above this. Generous — facts are tiny and memory is cheap.
	// +kubebuilder:default=5000
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxFacts int32 `json:"maxFacts,omitempty"`

	// TTLDays is deprecated: set retention.memory.purgeUnused instead. When set, it
	// overrides that value: a fact not used within this many days is removed.
	// +kubebuilder:validation:Minimum=1
	// +optional
	TTLDays int32 `json:"ttlDays,omitempty"`

	// InjectLimit caps how many facts are injected into an agent's context per
	// query. Small and SEPARATE from MaxFacts — large context degrades
	// tool-calling. Only domain+query-relevant facts are injected, up to this.
	// +kubebuilder:default=8
	// +kubebuilder:validation:Minimum=0
	// +optional
	InjectLimit int32 `json:"injectLimit,omitempty"`

	// VerifyOnAdd vets a new fact against existing memory before storing —
	// skips duplicates and supersedes conflicts (same-key) rather than blindly
	// accumulating. When false, REMEMBER writes straight through. Pointer so an
	// explicit false is distinguishable from unset.
	// +kubebuilder:default=true
	// +optional
	VerifyOnAdd *bool `json:"verifyOnAdd,omitempty"`
}

// RetentionDuration is how long data is kept, written as in Prometheus' retention: a
// number and a unit (s, m, h, d, w, y), several combined from the largest unit down
// (1y6w, 90d, 36h), or never.
// +kubebuilder:validation:Pattern=`^(never|([1-9][0-9]*y)?([1-9][0-9]*w)?([1-9][0-9]*d)?([1-9][0-9]*h)?([1-9][0-9]*m)?([1-9][0-9]*s)?)$`
// +kubebuilder:validation:MinLength=2
type RetentionDuration string

// CrewRetention is how long a crew's NATS data is kept and when the operator removes
// what has expired. Retention is only as fine as the schedule: a duration shorter than
// the interval between runs is removed at the next run, not when it expires.
type CrewRetention struct {
	// Schedule is when the cleanup runs, in UTC: a cron expression of five fields, or @hourly,
	// @daily, @weekly, @monthly, @yearly. The operator reports a duration finer than the
	// interval between runs.
	// +kubebuilder:default="@daily"
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// PurgeUnused removes anything with no activity (written or recalled) for this long.
	// It applies to conversations (their last turn is their activity) and to memory,
	// unless memory sets its own.
	// +kubebuilder:default="90d"
	// +optional
	PurgeUnused RetentionDuration `json:"purgeUnused,omitempty"`

	// Conversations governs the earlier turns the crew keeps for follow-up questions.
	// +kubebuilder:default={}
	// +optional
	Conversations *ConversationRetention `json:"conversations,omitempty"`

	// Memory governs the facts the crew learns.
	// +kubebuilder:default={}
	// +optional
	Memory *MemoryRetention `json:"memory,omitempty"`

	// Other governs the crew's remaining NATS data: its discussion messages and the
	// coordinators' logs. These streams also keep the size and age limits the install
	// sets on them, so this can only shorten what the streams keep.
	// +optional
	Other *OtherRetention `json:"other,omitempty"`
}

// ConversationRetention rotates conversations first in, first out.
type ConversationRetention struct {
	// Window is how long a conversation is kept after its last turn; the oldest go first.
	// never is not accepted: conversations are people's data and always age out.
	// +kubebuilder:default="30d"
	// +optional
	Window RetentionDuration `json:"window,omitempty"`
}

// MemoryRetention sets how long an unused fact is kept.
type MemoryRetention struct {
	// PurgeUnused removes a fact not written or recalled for this long. never keeps
	// every fact until it is superseded, invalidated, evicted by the size cap, or deleted.
	// Unset takes the crew-wide purgeUnused.
	// +optional
	PurgeUnused RetentionDuration `json:"purgeUnused,omitempty"`
}

// OtherRetention sets the window for the crew's other NATS data.
type OtherRetention struct {
	// Window is how long a message of the crew is kept. Unset leaves the streams' own limits.
	// never is not accepted.
	// +optional
	Window RetentionDuration `json:"window,omitempty"`
}

// RetentionRemoved counts what one cleanup run removed, by kind of data.
type RetentionRemoved struct {
	Conversations int32 `json:"conversations"`
	Memory        int32 `json:"memory"`
	Other         int32 `json:"other"`
}

// RetentionStatus reports the crew's last cleanup run.
type RetentionStatus struct {
	// LastRunTime is when the last cleanup run finished.
	// +optional
	LastRunTime *metav1.Time `json:"lastRunTime,omitempty"`

	// Removed counts what the last run removed.
	// +optional
	Removed RetentionRemoved `json:"removed,omitempty"`

	// Error is why the last run did not finish, empty when it did.
	// +optional
	Error string `json:"error,omitempty"`
}

// CrewStatus defines the observed state of Crew
type CrewStatus struct {
	// Phase is the crew lifecycle: Pending while the coordinator is not yet running,
	// Ready when the crew is reconciled and a model can run its agents, WaitingForCapacity
	// when the crew is reconciled but no model in the cluster catalog can run its
	// coordinator (or any specialist) now, and Error when a reconcile step failed.
	Phase string `json:"phase,omitempty"`

	// Ready mirrors the Ready condition: the crew is reconciled as declared (its agents,
	// gateways and policies exist as specified). It stays true while the crew waits for
	// capacity; the CapacityAvailable condition and the WaitingForCapacity phase say that
	// the crew cannot answer yet.
	Ready bool `json:"ready,omitempty"`

	// ObservedGeneration is the Crew generation the status describes.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// DiscussionEndpoint is the HTTP endpoint for the discussion gateway
	// Clients POST here to start discussions and GET SSE streams
	// +optional
	DiscussionEndpoint string `json:"discussionEndpoint,omitempty"`

	// CoordinatorRef is the name of the coordinator Agent for this crew
	// +optional
	CoordinatorRef string `json:"coordinatorRef,omitempty"`

	// AgentCount is the number of agents that belong to this crew
	AgentCount int32 `json:"agentCount,omitempty"`

	// Message provides additional status information
	Message string `json:"message,omitempty"`

	// Conditions represent the latest available observations: Ready (reconciled as
	// declared) and CapacityAvailable (a model in the cluster catalog can run the
	// crew's agents now).
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Retention reports the crew's last retention cleanup.
	// +optional
	Retention *RetentionStatus `json:"retention,omitempty"`

	// Revisions records the crew's deployments, newest first, capped at 10.
	// The operator appends an entry when the Crew's revision annotation,
	// deployed-at annotation, or crew-version label changes.
	// +optional
	// +kubebuilder:validation:MaxItems=10
	Revisions []CrewRevision `json:"revisions,omitempty"`
}

// CrewRevision is one observed deployment of a Crew. Revision, Source, Owner,
// Channel and DeployedAt come from the crewforge.kubemoot.ai/* annotations a
// CrewForge deploy stamps; CrewVersion comes from the kubemoot.ai/crew-version
// label a chart deploy stamps.
type CrewRevision struct {
	// Revision is the git short hash of the crew source, optionally suffixed "-dirty"
	// +optional
	Revision string `json:"revision,omitempty"`

	// Source is the crew source location as repo//path
	// +optional
	Source string `json:"source,omitempty"`

	// Owner is the email of the developer who deployed the crew
	// +optional
	Owner string `json:"owner,omitempty"`

	// Channel is how the crew was deployed (helm, bundle, flux)
	// +optional
	Channel string `json:"channel,omitempty"`

	// CrewVersion is the crew chart version from the kubemoot.ai/crew-version label
	// +optional
	CrewVersion string `json:"crewVersion,omitempty"`

	// DeployedAt is the RFC3339 deploy time the deployer stamped
	// +optional
	DeployedAt string `json:"deployedAt,omitempty"`

	// ObservedAt is when the operator first observed this revision
	ObservedAt metav1.Time `json:"observedAt"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=crew
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Capacity",type=string,JSONPath=`.status.conditions[?(@.type=="CapacityAvailable")].status`
// +kubebuilder:printcolumn:name="Coordinator",type=string,JSONPath=`.status.coordinatorRef`
// +kubebuilder:printcolumn:name="Agents",type=integer,JSONPath=`.status.agentCount`
// +kubebuilder:printcolumn:name="Discussion",type=string,JSONPath=`.status.discussionEndpoint`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Crew is the Schema for the crews API
// A Crew groups a coordinator and tooler agents into a named team.
// It optionally deploys a discussion gateway as the crew's HTTP entry point
// for external clients (CrewForge, Homelab Pilot, curl).
type Crew struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CrewSpec   `json:"spec,omitempty"`
	Status CrewStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CrewList contains a list of Crew
type CrewList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Crew `json:"items"`
}

func init() {
	registerTypes(&Crew{}, &CrewList{})
}
