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

// WorkspaceAccess says what an agent may do with the discussion's workspace, the scratch data
// the agents of one discussion share. Left out everywhere, the access is implicit: the agent
// behaves as it did before access was declared.
// +kubebuilder:validation:Enum=none;read;write;readWrite
type WorkspaceAccess string

const (
	// WorkspaceAccessNone gives the agent no workspace: it stores nothing and gets no workspace
	// tools, and a large tool result returns inline under the runtime's budget.
	WorkspaceAccessNone WorkspaceAccess = "none"
	// WorkspaceAccessRead gives the agent the workspace read tools. Its own tool results are not stored.
	WorkspaceAccessRead WorkspaceAccess = "read"
	// WorkspaceAccessWrite stores the agent's large tool results and gives it no read tools. A
	// tooler that fetches and hands off needs this.
	WorkspaceAccessWrite WorkspaceAccess = "write"
	// WorkspaceAccessReadWrite stores the agent's tool results and gives it the read tools.
	WorkspaceAccessReadWrite WorkspaceAccess = "readWrite"
)

// CanRead reports whether the access includes the workspace read tools.
func (a WorkspaceAccess) CanRead() bool {
	return a == WorkspaceAccessRead || a == WorkspaceAccessReadWrite
}

// CanWrite reports whether the access includes storing the agent's tool results.
func (a WorkspaceAccess) CanWrite() bool {
	return a == WorkspaceAccessWrite || a == WorkspaceAccessReadWrite
}

// AgentWorkspace declares one Agent's access to the workspace. It overrides the Crew's default.
type AgentWorkspace struct {
	// Access is none, read, write or readWrite. Left out, the Crew's defaultAccess applies, then
	// the KubemootConfig's, and with none declared the agent behaves as before: it stores every
	// tool result and reads the workspace only through the tools in its enabledTools.
	// A declared access is authoritative: workspace tools the access does not allow are disabled
	// even when enabledTools names them. Read is enforced through the agent's tool list; write
	// is enforced by the agent's NATS permissions.
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
	// DefaultAccess is the access of every agent whose Agent and Crew declare none. Left empty,
	// such an agent keeps its implicit behavior.
	// +optional
	DefaultAccess WorkspaceAccess `json:"defaultAccess,omitempty"`
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
