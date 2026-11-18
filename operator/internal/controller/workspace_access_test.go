/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	wsAgentName  = "analyst"
	wsCrewName   = "ws-crew"
	wsNamespace  = "ws-ns"
	wsHeadTool   = "workspace_head"
	wsGrepTool   = "workspace_grep"
	wsLegacyHead = "artifact_head"
	wsDomainTool = "k8s_pods_list"
)

func readTools(t *testing.T, got []string, want ...string) {
	t.Helper()
	for _, name := range want {
		if !slices.Contains(got, name) {
			t.Errorf("%v lacks %q", got, name)
		}
	}
}

func TestResolveWorkspaceAccessTakesTheClosestDeclaration(t *testing.T) {
	const (
		none  = kubemootv1alpha1.WorkspaceAccessNone
		read  = kubemootv1alpha1.WorkspaceAccessRead
		write = kubemootv1alpha1.WorkspaceAccessWrite
	)
	cases := map[string]struct {
		agent, crew, config kubemootv1alpha1.WorkspaceAccess
		access              kubemootv1alpha1.WorkspaceAccess
		source              kubemootv1alpha1.WorkspaceAccessSource
	}{
		"agent over crew and config": {none, read, write, none, kubemootv1alpha1.WorkspaceAccessSourceAgent},
		"crew over config":           {"", read, write, read, kubemootv1alpha1.WorkspaceAccessSourceCrew},
		"config alone":               {"", "", write, write, kubemootv1alpha1.WorkspaceAccessSourceConfig},
		"nothing declared":           {"", "", "", "", kubemootv1alpha1.WorkspaceAccessSourceImplicit},
	}
	for name, c := range cases {
		d := resolveWorkspaceAccess(c.agent, c.crew, c.config)
		if d.access != c.access || d.source != c.source {
			t.Errorf("%s: got %q from %q, want %q from %q", name, d.access, d.source, c.access, c.source)
		}
		if d.implicit() != (c.access == "") {
			t.Errorf("%s: implicit() = %v", name, d.implicit())
		}
	}
}

func TestImplicitAccessLeavesTheToolListsAlone(t *testing.T) {
	enabled, disabled := []string{wsLegacyHead, wsDomainTool}, []string{"x"}
	got := applyWorkspaceAccess(enabled, disabled, resolveWorkspaceAccess("", "", ""), kubemootv1alpha1.WorkspaceQueryEngineSQLite)
	if !slices.Equal(got.enabled, enabled) || !slices.Equal(got.disabled, disabled) || len(got.overruled) != 0 {
		t.Errorf("implicit access changed the lists: %+v", got)
	}
}

func TestAccessDerivesToolGrants(t *testing.T) {
	sqlite := kubemootv1alpha1.WorkspaceQueryEngineSQLite
	decide := func(a kubemootv1alpha1.WorkspaceAccess) workspaceDecision { return resolveWorkspaceAccess(a, "", "") }
	allTools := append(workspaceReadTools(), sortedKeys(legacyWorkspaceTools())...)

	for _, a := range []kubemootv1alpha1.WorkspaceAccess{kubemootv1alpha1.WorkspaceAccessNone, kubemootv1alpha1.WorkspaceAccessWrite} {
		got := applyWorkspaceAccess([]string{wsDomainTool}, nil, decide(a), sqlite)
		readTools(t, got.disabled, allTools...)
		if !slices.Equal(got.enabled, []string{wsDomainTool}) {
			t.Errorf("%s: a domain-only enabledTools gained %v", a, got.enabled)
		}
	}
	for _, a := range []kubemootv1alpha1.WorkspaceAccess{kubemootv1alpha1.WorkspaceAccessRead, kubemootv1alpha1.WorkspaceAccessReadWrite} {
		got := applyWorkspaceAccess([]string{wsDomainTool}, nil, decide(a), sqlite)
		readTools(t, got.enabled, wsDomainTool, "workspace_list", "workspace_describe", "workspace_query", wsHeadTool, "workspace_tail", wsGrepTool)
		for _, name := range allTools {
			if slices.Contains(got.disabled, name) {
				t.Errorf("%s: %s is disabled: %v", a, name, got.disabled)
			}
		}
	}
}

func TestAnEmptyEnabledListStaysEmpty(t *testing.T) {
	// An empty enabledTools means every tool; granting must not turn it into a short allow-list.
	got := applyWorkspaceAccess(nil, nil, resolveWorkspaceAccess(kubemootv1alpha1.WorkspaceAccessRead, "", ""), kubemootv1alpha1.WorkspaceQueryEngineSQLite)
	if len(got.enabled) != 0 {
		t.Errorf("enabled = %v, want empty", got.enabled)
	}
}

func TestQueryEngineNoneWithholdsWorkspaceQuery(t *testing.T) {
	got := applyWorkspaceAccess([]string{wsDomainTool}, nil,
		resolveWorkspaceAccess(kubemootv1alpha1.WorkspaceAccessRead, "", ""), kubemootv1alpha1.WorkspaceQueryEngineNone)
	if slices.Contains(got.enabled, "workspace_query") || !slices.Contains(got.disabled, "workspace_query") {
		t.Errorf("workspace_query must be disabled and not granted: %+v", got)
	}
	readTools(t, got.enabled, wsHeadTool)
}

func TestDeclaredAccessOverrulesEnabledToolsByEitherName(t *testing.T) {
	got := applyWorkspaceAccess([]string{wsDomainTool, wsLegacyHead, "artifact_jq", wsGrepTool}, nil,
		resolveWorkspaceAccess(kubemootv1alpha1.WorkspaceAccessNone, "", ""), kubemootv1alpha1.WorkspaceQueryEngineSQLite)
	if want := []string{wsLegacyHead, "artifact_jq", wsGrepTool}; !slices.Equal(got.overruled, want) {
		t.Errorf("overruled = %v, want %v", got.overruled, want)
	}
	readTools(t, got.disabled, wsLegacyHead, "artifact_jq", wsGrepTool)
}

func TestAnExplicitDenialBeatsADerivedGrant(t *testing.T) {
	got := applyWorkspaceAccess([]string{wsDomainTool}, []string{wsGrepTool},
		resolveWorkspaceAccess(kubemootv1alpha1.WorkspaceAccessRead, "", ""), kubemootv1alpha1.WorkspaceQueryEngineSQLite)
	if slices.Contains(got.enabled, wsGrepTool) {
		t.Errorf("a denied tool was granted: %v", got.enabled)
	}
}

func TestAnOldNameCountsAsGranted(t *testing.T) {
	got := applyWorkspaceAccess([]string{wsLegacyHead}, nil,
		resolveWorkspaceAccess(kubemootv1alpha1.WorkspaceAccessRead, "", ""), kubemootv1alpha1.WorkspaceQueryEngineSQLite)
	if slices.Contains(got.enabled, wsHeadTool) {
		t.Errorf("workspace_head was added beside artifact_head: %v", got.enabled)
	}
	if len(got.overruled) != 0 {
		t.Errorf("overruled = %v", got.overruled)
	}
}

func wsFixture(t *testing.T, crewAccess, configAccess kubemootv1alpha1.WorkspaceAccess) *AgentReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: wsCrewName, Namespace: wsNamespace}}
	if crewAccess != "" {
		crew.Spec.Workspace = &kubemootv1alpha1.CrewWorkspace{DefaultAccess: crewAccess}
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&kubemootv1alpha1.Agent{}).
		WithObjects(crew).Build()
	cache := NewConfigCache()
	cfg := &kubemootv1alpha1.KubemootConfig{}
	if configAccess != "" {
		cfg.Spec.Workspace = &kubemootv1alpha1.WorkspaceConfig{DefaultAccess: configAccess}
	}
	cache.Update(cfg)
	return &AgentReconciler{Client: cli, ConfigCache: cache}
}

func wsAgent(access kubemootv1alpha1.WorkspaceAccess, enabled ...string) *kubemootv1alpha1.Agent {
	a := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: wsAgentName, Namespace: wsNamespace, Labels: map[string]string{labelCrew: wsCrewName}},
		Spec:       kubemootv1alpha1.AgentSpec{EnabledTools: enabled},
	}
	if access != "" {
		a.Spec.Workspace = &kubemootv1alpha1.AgentWorkspace{Access: access}
	}
	return a
}

func TestTheReconcilerResolvesAgentThenCrewThenConfig(t *testing.T) {
	ctx := context.Background()
	const (
		none = kubemootv1alpha1.WorkspaceAccessNone
		read = kubemootv1alpha1.WorkspaceAccessRead
		rw   = kubemootv1alpha1.WorkspaceAccessReadWrite
	)
	cases := map[string]struct {
		agent, crew, config kubemootv1alpha1.WorkspaceAccess
		want                string
		source              kubemootv1alpha1.WorkspaceAccessSource
	}{
		"the agent overrides the crew default":      {none, rw, read, string(none), kubemootv1alpha1.WorkspaceAccessSourceAgent},
		"the crew overrides the config default":     {"", rw, read, "readWrite", kubemootv1alpha1.WorkspaceAccessSourceCrew},
		"the config applies when nothing else does": {"", "", read, "read", kubemootv1alpha1.WorkspaceAccessSourceConfig},
		"an undeclared agent stays implicit":        {"", "", "", "implicit", kubemootv1alpha1.WorkspaceAccessSourceImplicit},
	}
	for name, c := range cases {
		r := wsFixture(t, c.crew, c.config)
		agent := wsAgent(c.agent)
		if err := r.Create(ctx, agent); err != nil {
			t.Fatal(err)
		}
		if err := r.syncWorkspaceAccess(ctx, agent); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := agent.Status.Workspace
		if got == nil || got.Access != c.want || got.Source != c.source {
			t.Errorf("%s: status.workspace = %+v, want %s from %s", name, got, c.want, c.source)
		}
	}
}

func TestAnUndeclaredAgentGetsTodaysEnvAndNoCondition(t *testing.T) {
	ctx := context.Background()
	r := wsFixture(t, "", "")
	agent := wsAgent("", wsLegacyHead, wsDomainTool)
	env := r.buildEnvVars(ctx, agent, &modelPick{}, &modelPick{}, 8080)
	if v, _ := envValue(env, "KUBEMOOT_ENABLED_TOOLS"); v != wsLegacyHead+","+wsDomainTool {
		t.Errorf("KUBEMOOT_ENABLED_TOOLS = %q", v)
	}
	if _, ok := envValue(env, "KUBEMOOT_DISABLED_TOOLS"); ok {
		t.Error("an undeclared agent gained disabled tools")
	}
	if _, ok := envValue(env, workspaceAccessEnv); ok {
		t.Error("an undeclared agent got KUBEMOOT_WORKSPACE_ACCESS: the runtime would stop storing")
	}
	if err := r.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if err := r.syncWorkspaceAccess(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if meta.FindStatusCondition(agent.Status.Conditions, ConditionWorkspaceAccessResolved) != nil {
		t.Error("an implicit agent got a WorkspaceAccessResolved condition")
	}
}

func TestEachAccessSetsTheRuntimeEnvAndToolLists(t *testing.T) {
	ctx := context.Background()
	for _, a := range []kubemootv1alpha1.WorkspaceAccess{
		kubemootv1alpha1.WorkspaceAccessNone, kubemootv1alpha1.WorkspaceAccessRead,
		kubemootv1alpha1.WorkspaceAccessWrite, kubemootv1alpha1.WorkspaceAccessReadWrite,
	} {
		r := wsFixture(t, "", "")
		env := r.buildEnvVars(ctx, wsAgent(a, wsDomainTool), &modelPick{}, &modelPick{}, 8080)
		if v, _ := envValue(env, workspaceAccessEnv); v != string(a) {
			t.Errorf("%s: %s = %q", a, workspaceAccessEnv, v)
		}
		enabled, _ := envValue(env, "KUBEMOOT_ENABLED_TOOLS")
		disabled, _ := envValue(env, "KUBEMOOT_DISABLED_TOOLS")
		if a.CanRead() != strings.Contains(enabled, wsHeadTool) {
			t.Errorf("%s: enabled tools %q", a, enabled)
		}
		if a.CanRead() == strings.Contains(disabled, wsHeadTool) {
			t.Errorf("%s: disabled tools %q", a, disabled)
		}
	}
}

func TestTheHarnessContractCarriesTheDerivedToolLists(t *testing.T) {
	r := wsFixture(t, kubemootv1alpha1.WorkspaceAccessWrite, "")
	agent := wsAgent("", wsDomainTool)
	agent.Spec.Harness = &kubemootv1alpha1.HarnessSpec{Name: "runtime", Mode: kubemootv1alpha1.HarnessModeSidecar}
	c := r.buildContract(context.Background(), agent, &modelPick{}, &modelPick{})
	readTools(t, c.Tools.Disabled, wsHeadTool, wsLegacyHead)
}

func syncedCondition(t *testing.T, r *AgentReconciler, agent *kubemootv1alpha1.Agent) *metav1.Condition {
	t.Helper()
	ctx := context.Background()
	if err := r.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if err := r.syncWorkspaceAccess(ctx, agent); err != nil {
		t.Fatal(err)
	}
	return meta.FindStatusCondition(agent.Status.Conditions, ConditionWorkspaceAccessResolved)
}

func TestAReadAgentWithNoWorkspaceServerIsReported(t *testing.T) {
	r := wsFixture(t, "", "")
	cond := syncedCondition(t, r, wsAgent(kubemootv1alpha1.WorkspaceAccessRead, wsDomainTool))
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonNoWorkspaceServer {
		t.Fatalf("condition = %+v", cond)
	}
}

func TestAReadAgentWithAWorkspaceServerIsResolved(t *testing.T) {
	r := wsFixture(t, "", "")
	server := &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "readops", Namespace: wsNamespace},
		Spec:       kubemootv1alpha1.MCPServerSpec{KubemootImage: kubemootv1alpha1.KubemootImageWorkspaceAccess},
	}
	if err := r.Create(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	cond := syncedCondition(t, r, wsAgent(kubemootv1alpha1.WorkspaceAccessRead, wsDomainTool))
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != reasonWorkspaceAccessDeclared {
		t.Fatalf("condition = %+v", cond)
	}
}

func TestAnAgentNamingAToolItsAccessForbidsIsToldWhich(t *testing.T) {
	r := wsFixture(t, "", "")
	cond := syncedCondition(t, r, wsAgent(kubemootv1alpha1.WorkspaceAccessNone, wsDomainTool, wsLegacyHead))
	if cond == nil || cond.Reason != reasonWorkspaceToolsOverruled || !strings.Contains(cond.Message, wsLegacyHead) {
		t.Fatalf("condition = %+v", cond)
	}
}

func TestACrewDefaultChangeReachesTheAgentEnv(t *testing.T) {
	ctx := context.Background()
	r := wsFixture(t, kubemootv1alpha1.WorkspaceAccessNone, "")
	agent := wsAgent("", wsDomainTool)
	if v, _ := envValue(r.buildEnvVars(ctx, agent, &modelPick{}, &modelPick{}, 8080), workspaceAccessEnv); v != coreTestNone {
		t.Errorf("crew default none gave %q", v)
	}
}

func TestTheReconcileResolvesTheWorkspaceOnce(t *testing.T) {
	ctx := context.Background()
	r := wsFixture(t, kubemootv1alpha1.WorkspaceAccessNone, "")
	agent := wsAgent("", wsDomainTool)
	ctx = r.withWorkspace(ctx, agent)

	crew := &kubemootv1alpha1.Crew{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: wsNamespace, Name: wsCrewName}, crew); err != nil {
		t.Fatal(err)
	}
	crew.Spec.Workspace.DefaultAccess = kubemootv1alpha1.WorkspaceAccessReadWrite
	if err := r.Update(ctx, crew); err != nil {
		t.Fatal(err)
	}
	env := r.buildEnvVars(ctx, agent, &modelPick{}, &modelPick{}, 8080)
	if v, _ := envValue(env, workspaceAccessEnv); v != coreTestNone {
		t.Errorf("env %q: a Crew edit mid-reconcile reached the env", v)
	}
	if got := r.workspaceFor(ctx, agent).decision.access; got != kubemootv1alpha1.WorkspaceAccessNone {
		t.Errorf("status would see %q", got)
	}
}

func TestWatchPredicatesFireOnlyOnWhatChangesAccess(t *testing.T) {
	crewA := &kubemootv1alpha1.Crew{}
	crewB := crewA.DeepCopy()
	crewB.Spec.Description = "x"
	crewC := crewA.DeepCopy()
	crewC.Spec.Workspace = &kubemootv1alpha1.CrewWorkspace{DefaultAccess: kubemootv1alpha1.WorkspaceAccessRead}
	p := crewWorkspaceChanged()
	if p.Update(event.UpdateEvent{ObjectOld: crewA, ObjectNew: crewB}) {
		t.Error("a Crew description change fired")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: crewA, ObjectNew: crewC}) || !p.Create(event.CreateEvent{Object: crewA}) ||
		!p.Delete(event.DeleteEvent{Object: crewA}) {
		t.Error("a workspace change, a create or a delete did not fire")
	}

	srvA := &kubemootv1alpha1.MCPServer{}
	srvB := srvA.DeepCopy()
	srvB.Spec.Port = 99
	srvC := srvA.DeepCopy()
	srvC.Spec.KubemootImage = kubemootv1alpha1.KubemootImageWorkspaceAccess
	sp := workspaceServerChanged()
	if sp.Update(event.UpdateEvent{ObjectOld: srvA, ObjectNew: srvB}) {
		t.Error("an unrelated MCPServer change fired")
	}
	if !sp.Update(event.UpdateEvent{ObjectOld: srvA, ObjectNew: srvC}) || !sp.Create(event.CreateEvent{Object: srvA}) ||
		!sp.Delete(event.DeleteEvent{Object: srvA}) {
		t.Error("a kubemootImage change, a create or a delete did not fire")
	}
}

func TestCanonicalWorkspaceToolMatchesExactlyAndTheServerPrefixIsNotOne(t *testing.T) {
	if current, ok := canonicalWorkspaceTool("artifact_select"); !ok || current != "workspace_query" {
		t.Errorf("an old name maps to the current one, got %q %v", current, ok)
	}
	if _, ok := canonicalWorkspaceTool("mcp.workspace_grep"); ok {
		t.Error("an access list names tools exactly: a server-prefixed name is not a workspace tool here")
	}
	if _, ok := canonicalWorkspaceTool("pods_list"); ok {
		t.Error("pods_list is not a workspace tool")
	}
}
