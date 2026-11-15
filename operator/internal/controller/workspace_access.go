/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// workspace_access.go turns the workspace access an Agent, its Crew or KubemootConfig declares
// into what the agent gets: its tool lists, the KUBEMOOT_WORKSPACE_ACCESS the runtime reads,
// and its status. This file is the one place that decides access. Read is enforced through the
// agent's tool list; write is enforced by the agent's NATS permissions (mesh.PermissionsFor).
// An agent with nothing declared is implicit: its tool lists and behavior are left as they are.
package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	// ConditionWorkspaceAccessResolved is the Agent condition that reports the resolved
	// workspace access. It is False when the declared access cannot work as written, and absent
	// while the access is implicit.
	ConditionWorkspaceAccessResolved = "WorkspaceAccessResolved"

	reasonWorkspaceAccessDeclared = "Declared"
	reasonWorkspaceToolsOverruled = "ToolsOverruled"
	reasonNoWorkspaceServer       = "NoWorkspaceServer"

	// workspaceAccessEnv is the variable the agent-runtime reads to decide whether it stores
	// tool results. Absent means implicit: store, as before.
	workspaceAccessEnv = "KUBEMOOT_WORKSPACE_ACCESS"
)

// workspaceQueryTool is the one tool that replaces the four retired query tools.
const workspaceQueryTool = "workspace_query"

// workspaceReadTools are the tools that read the workspace, by their current names.
var workspaceReadTools = []string{
	"workspace_list", "workspace_describe", workspaceQueryTool,
	"workspace_head", "workspace_tail", "workspace_grep",
}

// legacyWorkspaceTools maps each old tool name to the tool that replaces it. They keep working
// for one release, so a declared access governs them too.
var legacyWorkspaceTools = map[string]string{
	"artifact_head": "workspace_head", "artifact_tail": "workspace_tail", "artifact_grep": "workspace_grep",
	"artifact_count": workspaceQueryTool, "artifact_rows": workspaceQueryTool,
	"artifact_select": workspaceQueryTool, "artifact_jq": workspaceQueryTool,
}

// workspaceDecision is the access resolved for one Agent. An empty access is implicit.
type workspaceDecision struct {
	access kubemootv1alpha1.WorkspaceAccess
	source kubemootv1alpha1.WorkspaceAccessSource
}

func (d workspaceDecision) implicit() bool { return d.access == "" }

// resolveWorkspaceAccess picks the closest declaration: the Agent's, then its Crew's, then
// KubemootConfig's, then implicit.
func resolveWorkspaceAccess(agent, crew, config kubemootv1alpha1.WorkspaceAccess) workspaceDecision {
	switch {
	case agent != "":
		return workspaceDecision{agent, kubemootv1alpha1.WorkspaceAccessSourceAgent}
	case crew != "":
		return workspaceDecision{crew, kubemootv1alpha1.WorkspaceAccessSourceCrew}
	case config != "":
		return workspaceDecision{config, kubemootv1alpha1.WorkspaceAccessSourceConfig}
	}
	return workspaceDecision{source: kubemootv1alpha1.WorkspaceAccessSourceImplicit}
}

// workspaceTools is an Agent's tool lists after its access is applied.
type workspaceTools struct {
	enabled, disabled []string
	// overruled are the enabledTools entries the declared access does not allow. They stay in
	// the list, but disabledTools wins, so the agent never gets them.
	overruled []string
}

// canonicalWorkspaceTool returns the current name of a workspace tool, old or new, and whether
// the name is a workspace tool at all.
func canonicalWorkspaceTool(name string) (string, bool) {
	if current, ok := legacyWorkspaceTools[name]; ok {
		return current, true
	}
	return name, slices.Contains(workspaceReadTools, name)
}

// allowedWorkspaceTools are the workspace tools an access grants. workspace_query is offered
// only when the query engine is on.
func allowedWorkspaceTools(access kubemootv1alpha1.WorkspaceAccess, engine kubemootv1alpha1.ArtifactQueryEngine) []string {
	if !access.CanRead() {
		return nil
	}
	allowed := make([]string, 0, len(workspaceReadTools))
	for _, t := range workspaceReadTools {
		if t != workspaceQueryTool || queryEngineOn(engine) {
			allowed = append(allowed, t)
		}
	}
	return allowed
}

// applyWorkspaceAccess derives an agent's tool lists from its declared access. Implicit access
// returns the lists untouched. Otherwise every workspace tool the access does not grant is added
// to disabledTools, and a read access adds the tools it grants to a non-empty enabledTools (an
// empty one already means every tool).
func applyWorkspaceAccess(enabled, disabled []string, d workspaceDecision, engine kubemootv1alpha1.ArtifactQueryEngine) workspaceTools {
	out := workspaceTools{enabled: enabled, disabled: disabled}
	if d.implicit() {
		return out
	}
	allowed := allowedWorkspaceTools(d.access, engine)
	out.disabled = slices.Clone(disabled)
	for _, name := range append(slices.Clone(workspaceReadTools), sortedKeys(legacyWorkspaceTools)...) {
		if current, _ := canonicalWorkspaceTool(name); !slices.Contains(allowed, current) && !slices.Contains(out.disabled, name) {
			out.disabled = append(out.disabled, name)
		}
	}
	out.overruled = overruledTools(enabled, allowed)
	if len(enabled) > 0 {
		out.enabled = grantTools(enabled, disabled, allowed)
	}
	return out
}

// overruledTools are the enabledTools entries naming a workspace tool the access does not grant.
func overruledTools(enabled, allowed []string) []string {
	var out []string
	for _, name := range enabled {
		if current, ok := canonicalWorkspaceTool(name); ok && !slices.Contains(allowed, current) {
			out = append(out, name)
		}
	}
	return out
}

// grantTools appends each granted tool that enabled does not already cover, under either of its
// names, and that disabledTools does not name: an explicit denial wins over a derived grant.
func grantTools(enabled, disabled, allowed []string) []string {
	out := slices.Clone(enabled)
	covered := map[string]bool{}
	for _, name := range enabled {
		current, _ := canonicalWorkspaceTool(name)
		covered[current] = true
	}
	for _, name := range allowed {
		if !covered[name] && !slices.Contains(disabled, name) {
			out = append(out, name)
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// workspaceDecisionFor reads the declarations that apply to an Agent. A Crew that cannot be read
// is treated as declaring nothing; the next reconcile reads it again.
func (r *AgentReconciler) workspaceDecisionFor(ctx context.Context, agent *kubemootv1alpha1.Agent) workspaceDecision {
	var declared, crewAccess kubemootv1alpha1.WorkspaceAccess
	if agent.Spec.Workspace != nil {
		declared = agent.Spec.Workspace.Access
	}
	if name := agent.Labels[labelCrew]; name != "" {
		crew := &kubemootv1alpha1.Crew{}
		err := r.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: name}, crew)
		switch {
		case err == nil && crew.Spec.Workspace != nil:
			crewAccess = crew.Spec.Workspace.DefaultAccess
		case err != nil && !apierrors.IsNotFound(err):
			logf.FromContext(ctx).Error(err, "Reading the Crew for its workspace access failed", "agent", agent.Name)
		}
	}
	var configAccess kubemootv1alpha1.WorkspaceAccess
	if r.ConfigCache != nil {
		configAccess = r.ConfigCache.GetWorkspaceDefaultAccess()
	}
	return resolveWorkspaceAccess(declared, crewAccess, configAccess)
}

// workspaceToolsFor returns the Agent's tool lists with its workspace access applied.
func (r *AgentReconciler) workspaceToolsFor(ctx context.Context, agent *kubemootv1alpha1.Agent) workspaceTools {
	engine := kubemootv1alpha1.ArtifactQueryEngineSQLite
	if r.ConfigCache != nil {
		engine = r.ConfigCache.GetWorkspaceQueryEngine()
	}
	return applyWorkspaceAccess(agent.Spec.EnabledTools, agent.Spec.DisabledTools,
		r.workspaceDecisionFor(ctx, agent), engine)
}

// workspaceAccessEnvVar is the access the agent-runtime reads; implicit sets nothing.
func workspaceAccessEnvVar(d workspaceDecision) []corev1.EnvVar {
	if d.implicit() {
		return nil
	}
	return []corev1.EnvVar{{Name: workspaceAccessEnv, Value: string(d.access)}}
}

// hasWorkspaceServer reports whether the namespace runs an MCPServer built from the workspace
// access image, which read tools need.
func (r *AgentReconciler) hasWorkspaceServer(ctx context.Context, namespace string) (bool, error) {
	servers := &kubemootv1alpha1.MCPServerList{}
	if err := r.List(ctx, servers, client.InNamespace(namespace)); err != nil {
		return false, err
	}
	for i := range servers.Items {
		if servers.Items[i].Spec.KubemootImage == kubemootv1alpha1.KubemootImageArtifactAccess {
			return true, nil
		}
	}
	return false, nil
}

// workspaceCondition builds the condition for a resolved access, or nil while it is implicit.
func workspaceCondition(agent *kubemootv1alpha1.Agent, d workspaceDecision, tools workspaceTools, serverMissing bool) *metav1.Condition {
	if d.implicit() {
		return nil
	}
	cond := &metav1.Condition{
		Type: ConditionWorkspaceAccessResolved, Status: metav1.ConditionTrue, Reason: reasonWorkspaceAccessDeclared,
		ObservedGeneration: agent.Generation,
		Message:            fmt.Sprintf("workspace access %s, declared on the %s", d.access, d.source),
	}
	if len(tools.overruled) > 0 {
		cond.Reason = reasonWorkspaceToolsOverruled
		cond.Message += fmt.Sprintf("; enabledTools names %s, which that access does not allow, so the tools are disabled",
			strings.Join(tools.overruled, ", "))
	}
	if serverMissing {
		cond.Status, cond.Reason = metav1.ConditionFalse, reasonNoWorkspaceServer
		cond.Message += "; no MCPServer in the namespace uses spec.kubemootImage artifactAccess, so the read tools have no server"
	}
	return cond
}

// syncWorkspaceAccess records the resolved access and its condition in the Agent's status, and
// writes the status when either changed.
func (r *AgentReconciler) syncWorkspaceAccess(ctx context.Context, agent *kubemootv1alpha1.Agent) error {
	d := r.workspaceDecisionFor(ctx, agent)
	tools := r.workspaceToolsFor(ctx, agent)
	serverMissing := false
	if d.access.CanRead() {
		has, err := r.hasWorkspaceServer(ctx, agent.Namespace)
		if err != nil {
			return fmt.Errorf("listing MCPServers for the workspace access check: %w", err)
		}
		serverMissing = !has
	}
	changed := setWorkspaceStatus(agent, d)
	if cond := workspaceCondition(agent, d, tools, serverMissing); cond != nil {
		changed = meta.SetStatusCondition(&agent.Status.Conditions, *cond) || changed
	} else {
		changed = meta.RemoveStatusCondition(&agent.Status.Conditions, ConditionWorkspaceAccessResolved) || changed
	}
	if !changed {
		return nil
	}
	if err := r.Status().Update(ctx, agent); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("recording the workspace access in the status: %w", err)
	}
	return nil
}

// setWorkspaceStatus writes status.workspace and reports whether it changed.
func setWorkspaceStatus(agent *kubemootv1alpha1.Agent, d workspaceDecision) bool {
	want := &kubemootv1alpha1.AgentWorkspaceStatus{Access: kubemootv1alpha1.WorkspaceAccessImplicit, Source: d.source}
	if !d.implicit() {
		want.Access = string(d.access)
	}
	if agent.Status.Workspace != nil && *agent.Status.Workspace == *want {
		return false
	}
	agent.Status.Workspace = want
	return true
}
