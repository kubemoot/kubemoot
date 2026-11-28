/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// workspace_access.go turns the workspace access an Agent, its Crew or KubemootConfig declares
// into what the agent gets: its tool lists and its status. This file is the one place that decides access, and Reconcile resolves it once
// per pass (withWorkspace) so the tool lists, the contract and the status cannot disagree.
//
// In 0.94 read is enforced through the agent's tool list. The access governs only what an agent
// may read of other agents' stored results: every agent's own calls are always kept in the
// workspace, so nothing an agent did is hidden. NATS permissions (mesh.PermissionsFor) describe
// the read grants, but nothing provisions NATS users from them yet, so NATS does not enforce
// them until user provisioning lands. Other routes read the workspace regardless of the tool
// list: the code sandbox's /kubemoot/workspace mount (the materializer sidecar copies every object of its
// namespace), the coordinator's review catalog of keys, and direct MCP mode, where tool names
// carry a server prefix (server.tool) and so are not matched by the workspace tool names here.
// An agent with nothing declared is implicit: its tool lists and behavior are left as they are.
package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/kubemoot/kubemoot/operator/pkg/workspacetools"

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
)

// workspaceQueryTool is the one tool that replaces the four retired query tools.
const workspaceQueryTool = workspacetools.Query

// workspaceReadTools are the tools that read the workspace, by their current names.
func workspaceReadTools() []string { return workspacetools.ReadTools() }

// legacyWorkspaceTools maps each old tool name to the tool that replaces it. They keep working
// for one release, so a declared access governs them too.
func legacyWorkspaceTools() map[string]string { return workspacetools.Legacy() }

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
// the name is a workspace tool at all. A name is matched exactly: an access list names tools
// as they are configured, so a server-prefixed name (direct MCP mode) is not a workspace tool here.
func canonicalWorkspaceTool(name string) (string, bool) { return workspacetools.Canonical(name) }

// allowedWorkspaceTools are the workspace tools an access grants. workspace_query is offered
// only when the query engine is on.
func allowedWorkspaceTools(access kubemootv1alpha1.WorkspaceAccess, engine kubemootv1alpha1.WorkspaceQueryEngine) []string {
	if !access.CanRead() {
		return nil
	}
	tools := workspaceReadTools()
	allowed := make([]string, 0, len(tools))
	for _, t := range tools {
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
func applyWorkspaceAccess(enabled, disabled []string, d workspaceDecision, engine kubemootv1alpha1.WorkspaceQueryEngine) workspaceTools {
	out := workspaceTools{enabled: enabled, disabled: disabled}
	if d.implicit() {
		return out
	}
	allowed := allowedWorkspaceTools(d.access, engine)
	out.disabled = slices.Clone(disabled)
	for _, name := range append(workspaceReadTools(), sortedKeys(legacyWorkspaceTools())...) {
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

// workspaceResolution is what an Agent's workspace declarations resolve to in one reconcile.
type workspaceResolution struct {
	decision workspaceDecision
	tools    workspaceTools
}

type workspaceCtxKey struct{}

// withWorkspace resolves the Agent's workspace access once and carries it in the context, so every
// later step of the reconcile (env, contract, status) reads the same answer and the Crew is read once.
func (r *AgentReconciler) withWorkspace(ctx context.Context, agent *kubemootv1alpha1.Agent) context.Context {
	return context.WithValue(ctx, workspaceCtxKey{}, r.resolveWorkspace(ctx, agent))
}

// workspaceFor returns the resolution withWorkspace put in ctx, or resolves it when the caller
// did not (a test that builds one piece of an Agent directly).
func (r *AgentReconciler) workspaceFor(ctx context.Context, agent *kubemootv1alpha1.Agent) workspaceResolution {
	if res, ok := ctx.Value(workspaceCtxKey{}).(workspaceResolution); ok {
		return res
	}
	return r.resolveWorkspace(ctx, agent)
}

// resolveWorkspace reads the declarations that apply to an Agent and applies them to its tool
// lists. A Crew that cannot be read is treated as declaring nothing; the next reconcile reads it again.
func (r *AgentReconciler) resolveWorkspace(ctx context.Context, agent *kubemootv1alpha1.Agent) workspaceResolution {
	var declared, crewAccess, configAccess kubemootv1alpha1.WorkspaceAccess
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
	engine := kubemootv1alpha1.WorkspaceQueryEngineSQLite
	if r.ConfigCache != nil {
		configAccess = r.ConfigCache.GetWorkspaceDefaultAccess()
		engine = r.ConfigCache.GetWorkspaceQueryEngine()
	}
	d := resolveWorkspaceAccess(declared, crewAccess, configAccess)
	return workspaceResolution{decision: d, tools: applyWorkspaceAccess(agent.Spec.EnabledTools, agent.Spec.DisabledTools, d, engine)}
}

// hasWorkspaceServer reports whether the namespace has an MCPServer that selects the workspace
// image with spec.kubemootImage. It recognizes only that field: a server that names the image
// directly (spec.image) or an external endpoint is not counted, so a crew using one sees the
// NoWorkspaceServer condition although its read tools work.
func (r *AgentReconciler) hasWorkspaceServer(ctx context.Context, namespace string) (bool, error) {
	servers := &kubemootv1alpha1.MCPServerList{}
	if err := r.List(ctx, servers, client.InNamespace(namespace)); err != nil {
		return false, err
	}
	for i := range servers.Items {
		if isWorkspaceAccessImage(servers.Items[i].Spec.KubemootImage) {
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
		cond.Message += "; no MCPServer in the namespace uses spec.kubemootImage workspaceAccess, so the read tools have no server"
	}
	return cond
}

// syncWorkspaceAccess records the resolved access and its condition in the Agent's status, and
// writes the status when either changed.
func (r *AgentReconciler) syncWorkspaceAccess(ctx context.Context, agent *kubemootv1alpha1.Agent) error {
	res := r.workspaceFor(ctx, agent)
	d, tools := res.decision, res.tools
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

// crewAgentWiringChanged passes a Crew's creation, deletion and any change to what its Agents
// are wired from: spec.workspace (their access), spec.defaultTimeZone, and whether the crew
// tells its agents who is asking. Nothing else of a Crew changes an Agent.
func crewAgentWiringChanged() predicate.Predicate {
	return predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		oldCrew, ok1 := e.ObjectOld.(*kubemootv1alpha1.Crew)
		newCrew, ok2 := e.ObjectNew.(*kubemootv1alpha1.Crew)
		return !ok1 || !ok2 ||
			!apiequality.Semantic.DeepEqual(oldCrew.Spec.Workspace, newCrew.Spec.Workspace) ||
			oldCrew.Spec.DefaultTimeZone != newCrew.Spec.DefaultTimeZone ||
			exposesCaller(oldCrew) != exposesCaller(newCrew)
	}}
}

// workspaceServerChanged passes an MCPServer's creation and deletion and a change to
// spec.kubemootImage, the only changes that alter whether the namespace has a workspace server.
func workspaceServerChanged() predicate.Predicate {
	return predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		oldServer, ok1 := e.ObjectOld.(*kubemootv1alpha1.MCPServer)
		newServer, ok2 := e.ObjectNew.(*kubemootv1alpha1.MCPServer)
		return !ok1 || !ok2 || oldServer.Spec.KubemootImage != newServer.Spec.KubemootImage
	}}
}
