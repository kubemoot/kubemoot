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

// WorkspaceAccess says what an agent may read of the discussion's workspace, the scratch data
// the agents of one discussion share. It never decides whether the agent's own tool calls are
// kept: every call is stored in the workspace, so nothing an agent did is hidden. Left out
// everywhere, the access is implicit: the agent reads the workspace through the tools it enables.
// +kubebuilder:validation:Enum=none;read;write;readWrite
type WorkspaceAccess string

const (
	// WorkspaceAccessNone gives the agent no workspace read tools. Its own calls are still kept.
	WorkspaceAccessNone WorkspaceAccess = "none"
	// WorkspaceAccessRead gives the agent the workspace read tools. Its own calls are still kept.
	WorkspaceAccessRead WorkspaceAccess = "read"
	// WorkspaceAccessWrite gives the agent no read tools and keeps its calls. A tooler that
	// fetches and hands off needs this.
	WorkspaceAccessWrite WorkspaceAccess = "write"
	// WorkspaceAccessReadWrite keeps the agent's calls and gives it the read tools.
	WorkspaceAccessReadWrite WorkspaceAccess = "readWrite"
)

// CanRead reports whether the access includes the workspace read tools.
func (a WorkspaceAccess) CanRead() bool {
	return a == WorkspaceAccessRead || a == WorkspaceAccessReadWrite
}

// CanWrite reports whether the access names write: a declaration for the NATS write grant, not a switch on keeping calls.
func (a WorkspaceAccess) CanWrite() bool {
	return a == WorkspaceAccessWrite || a == WorkspaceAccessReadWrite
}

// AgentWorkspace declares one Agent's access to the workspace. It overrides the Crew's default.
type AgentWorkspace struct {
	// Access is none, read, write or readWrite. Left out, the Crew's defaultAccess applies, then
	// the KubemootConfig's, and with none declared the agent reads the workspace only
	// through the tools in its enabledTools. The access governs what the agent may read of other
	// agents' stored results; the agent's own calls are always kept.
	// A declared access is authoritative: workspace tools the access does not allow are disabled
	// even when enabledTools names them. In 0.94 read is enforced through the agent's tool list.
	// NATS does not enforce it yet; that lands with NATS user provisioning.
	// +optional
	Access WorkspaceAccess `json:"access,omitempty"`
}

// CrewWorkspace declares the workspace access of the Crew's agents.
type CrewWorkspace struct {
	// DefaultAccess is the access of every agent of the crew that does not declare its own.
	// +optional
	DefaultAccess WorkspaceAccess `json:"defaultAccess,omitempty"`
}

// WorkspaceConfig configures the discussion workspace cluster-wide.
type WorkspaceConfig struct {
	// DefaultAccess is the access of every agent whose Agent and Crew declare none. Omit it to
	// leave such an agent implicit; an empty string is refused by the enum.
	// +optional
	DefaultAccess WorkspaceAccess `json:"defaultAccess,omitempty"`

	// QueryEngine is the engine that answers workspace_query on every MCPServer that
	// selects spec.kubemootImage workspaceAccess. sqlite (the default, also when unset) runs in
	// the workspace-access process and needs nothing added to the image; duckdb needs its program
	// on the image; none offers no query tool. The operator sets WORKSPACE_QUERY_ENGINE on those
	// servers; a server that sets the variable itself keeps its own value.
	// +optional
	QueryEngine WorkspaceQueryEngine `json:"queryEngine,omitempty"`
}

// WorkspaceAccessSource names where an Agent's resolved workspace access was declared.
type WorkspaceAccessSource string

// The places a workspace access can come from, closest to the agent first.
const (
	WorkspaceAccessSourceAgent    WorkspaceAccessSource = "agent"
	WorkspaceAccessSourceCrew     WorkspaceAccessSource = "crew"
	WorkspaceAccessSourceConfig   WorkspaceAccessSource = "config"
	WorkspaceAccessSourceImplicit WorkspaceAccessSource = "implicit"
)

// WorkspaceAccessImplicit is the access reported in status when nothing declares one.
const WorkspaceAccessImplicit = "implicit"

// AgentWorkspaceStatus reports the workspace access the operator resolved for an Agent.
type AgentWorkspaceStatus struct {
	// Access is the resolved access: none, read, write, readWrite, or implicit.
	Access string `json:"access,omitempty"`
	// Source says where the access was declared: agent, crew, config or implicit.
	Source WorkspaceAccessSource `json:"source,omitempty"`
}
