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
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

const (
	testNamespaceA = "team-a"
	testNamespaceB = "team-b"
	testCrewPilot  = "pilot"
	testDottedName = "pi.lot"
	testOllamaURL  = "http://ollama:11434"
	testModelID    = "qwen3:8b"
)

func scopeTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	return s
}

// assertNamespaceFieldRef fails unless env sets KUBEMOOT_NAMESPACE from metadata.namespace.
func assertNamespaceFieldRef(t *testing.T, env []corev1.EnvVar) {
	t.Helper()
	for _, e := range env {
		if e.Name != "KUBEMOOT_NAMESPACE" {
			continue
		}
		if e.ValueFrom == nil || e.ValueFrom.FieldRef == nil || e.ValueFrom.FieldRef.FieldPath != fieldPathNamespace {
			t.Fatalf("KUBEMOOT_NAMESPACE must come from the downward API, got %+v", e)
		}
		return
	}
	t.Fatal("KUBEMOOT_NAMESPACE missing")
}

func TestAgentEnvCarriesNamespaceFromDownwardAPI(t *testing.T) {
	cli := fake.NewClientBuilder().WithScheme(scopeTestScheme(t)).Build()
	r := &AgentReconciler{Client: cli}
	pick := &modelPick{ModelID: testModelID, Endpoint: testOllamaURL}
	agent := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{
		Name: "a", Namespace: testNamespaceA, Labels: map[string]string{crewLabelKey: testCrewPilot},
	}}
	env := r.buildEnvVars(context.Background(), agent, pick, pick, 8080)
	assertNamespaceFieldRef(t, env)
	if v, _ := envValue(env, "KUBEMOOT_CREW"); v != testCrewPilot {
		t.Errorf("KUBEMOOT_CREW = %q, want pilot", v)
	}
}

func TestDiscussionGatewayDeploymentCarriesNamespace(t *testing.T) {
	r := &CrewReconciler{ConfigCache: NewConfigCache()}
	crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: testCrewPilot, Namespace: testNamespaceA}}
	dep := r.buildDeployment(crew)
	assertNamespaceFieldRef(t, dep.Spec.Template.Spec.Containers[0].Env)
}

func TestWithNamespaceEnv(t *testing.T) {
	in := []corev1.EnvVar{{Name: "A", Value: "1"}}
	out := withNamespaceEnv(in)
	assertNamespaceFieldRef(t, out)
	if len(in) != 1 {
		t.Errorf("input slice mutated: %+v", in)
	}
	explicit := []corev1.EnvVar{{Name: "KUBEMOOT_NAMESPACE", Value: "pinned"}}
	if got := withNamespaceEnv(explicit); len(got) != 1 || got[0].Value != "pinned" {
		t.Errorf("an explicit KUBEMOOT_NAMESPACE must be kept, got %+v", got)
	}
	assertNamespaceFieldRef(t, withNamespaceEnv(nil))
}

func TestMCPServerContainerAndSidecarsCarryNamespace(t *testing.T) {
	mcp := &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "sched", Namespace: testNamespaceA},
		Spec: kubemootv1alpha1.MCPServerSpec{
			Image:    "img",
			Sidecars: []corev1.Container{{Name: "materializer", Image: "aa"}},
		},
	}
	assertNamespaceFieldRef(t, buildMCPServerContainer(mcp, 8080).Env)
	sidecars := buildUserSidecars(mcp)
	if len(sidecars) != 1 {
		t.Fatalf("want one sidecar, got %d", len(sidecars))
	}
	assertNamespaceFieldRef(t, sidecars[0].Env)
	if len(mcp.Spec.Sidecars[0].Env) != 0 {
		t.Error("the MCPServer spec must not be mutated")
	}
}

func TestSetDiscussionEndpointUsesNamespacedRoute(t *testing.T) {
	t.Setenv("GATEWAY_HOSTNAME", "moot.example.com")
	r := &CrewReconciler{}
	crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: testCrewPilot, Namespace: testNamespaceA}}
	r.setDiscussionEndpoint(crew)
	want := "https://moot.example.com/api/v1/namespaces/team-a/discussions/pilot"
	if crew.Status.DiscussionEndpoint != want {
		t.Errorf("endpoint = %q, want %q", crew.Status.DiscussionEndpoint, want)
	}
}

func TestReconcileHTTPRouteRewritesNamespacedPrefix(t *testing.T) {
	t.Setenv("GATEWAY_NAME", "shared")
	t.Setenv("GATEWAY_NAMESPACE", "gw")
	t.Setenv("GATEWAY_HOSTNAME", "moot.example.com")
	scheme := scopeTestScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &CrewReconciler{Client: cli, Scheme: scheme}
	crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: testCrewPilot, Namespace: testNamespaceA, UID: "u1"}}
	if err := r.reconcileHTTPRoute(context.Background(), crew); err != nil {
		t.Fatalf("reconcileHTTPRoute: %v", err)
	}
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"})
	if err := cli.Get(context.Background(), types.NamespacedName{Name: "pilot-discussion", Namespace: testNamespaceA}, route); err != nil {
		t.Fatalf("get route: %v", err)
	}
	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
	if len(rules) != 1 {
		t.Fatalf("want one rule, got %d", len(rules))
	}
	rule := rules[0].(map[string]interface{})
	matches := rule["matches"].([]interface{})
	path, _, _ := unstructured.NestedString(matches[0].(map[string]interface{}), "path", "value")
	if path != "/api/v1/namespaces/team-a/discussions/pilot" {
		t.Errorf("match path = %q", path)
	}
	filters := rule["filters"].([]interface{})
	filter := filters[0].(map[string]interface{})
	rewrite, _, _ := unstructured.NestedString(filter, "urlRewrite", "path", "replacePrefixMatch")
	kind, _, _ := unstructured.NestedString(filter, "urlRewrite", "path", "type")
	if filter["type"] != "URLRewrite" || kind != "ReplacePrefixMatch" || rewrite != "/api/v1/discussions/pilot" {
		t.Errorf("rewrite filter = %+v", filter)
	}
}

func TestDiscoverAgentsStaysInTheCrewNamespace(t *testing.T) {
	mk := func(ns, name string) *kubemootv1alpha1.Agent {
		return &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns, Labels: map[string]string{crewLabelKey: testCrewPilot},
		}}
	}
	cli := fake.NewClientBuilder().WithScheme(scopeTestScheme(t)).
		WithObjects(mk(testNamespaceA, "one"), mk(testNamespaceB, "two"), mk(testNamespaceB, "three")).Build()
	r := &CrewReconciler{Client: cli}
	crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: testCrewPilot, Namespace: testNamespaceA}}
	agents, _, err := r.discoverAgents(context.Background(), crew)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents.Items) != 1 || agents.Items[0].Name != "one" {
		t.Errorf("want only team-a's agent, got %d agents", len(agents.Items))
	}
}

func TestReconcileRejectsCrewNameThatIsNotOneSubjectToken(t *testing.T) {
	scheme := scopeTestScheme(t)
	crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{
		Name: testDottedName, Namespace: testNamespaceA, Finalizers: []string{crewFinalizer},
	}}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crew).WithStatusSubresource(crew).Build()
	r := &CrewReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	key := types.NamespacedName{Name: testDottedName, Namespace: testNamespaceA}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := &kubemootv1alpha1.Crew{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != "Error" {
		t.Errorf("phase = %q, want Error", got.Status.Phase)
	}
}

func TestBindsServiceAccount(t *testing.T) {
	crb := &rbacv1.ClusterRoleBinding{Subjects: []rbacv1.Subject{
		{Kind: rbacv1.ServiceAccountKind, Name: "pilot-discussion", Namespace: testNamespaceA},
	}}
	if !bindsServiceAccount(crb, "pilot-discussion", testNamespaceA) {
		t.Error("expected a match")
	}
	if bindsServiceAccount(crb, "pilot-discussion", testNamespaceB) {
		t.Error("another namespace must not match")
	}
	if bindsServiceAccount(crb, "other", testNamespaceA) {
		t.Error("another account must not match")
	}
}

func TestCoordinatorScope(t *testing.T) {
	labeled := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{
		Name: "c", Namespace: testNamespaceA, Labels: map[string]string{crewLabelKey: testCrewPilot},
	}}
	s, err := coordinatorScope(labeled)
	if err != nil || s.ResumeKey() != "team-a.pilot" || s.ResumeCollection() != "crew_team_a_pilot_resumes" {
		t.Errorf("labeled: %+v %v", s, err)
	}
	unlabeled := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: testNamespaceB}}
	s, err = coordinatorScope(unlabeled)
	if err != nil || s.ResumeKey() != "team-b.team-b" {
		t.Errorf("unlabeled falls back to the namespace: %+v %v", s, err)
	}
	dotted := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{
		Name: "c", Namespace: testNamespaceA, Labels: map[string]string{crewLabelKey: testDottedName},
	}}
	if _, err := coordinatorScope(dotted); err == nil {
		t.Error("a dotted crew label must be rejected")
	}
}
