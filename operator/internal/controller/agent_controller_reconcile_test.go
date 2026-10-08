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

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func agentReconcileScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add kubemoot scheme: %v", err)
	}
	return s
}

// With no Ready Model (and no Ready ModelProvider) in the namespace, pickModel
// finds nothing feasible, so the Agent is marked Unschedulable and requeued for a
// later retry. Exercises Reconcile's scheduling head: pickModel ->
// findPolicyAndRule -> feasibleCandidates -> evaluateCandidate ->
// candidateFeasible, and markUnschedulable.
func TestAgentReconcile_UnschedulableWhenNoReadyModel(t *testing.T) {
	ctx := context.Background()
	scheme := agentReconcileScheme(t)
	agent := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "a1", Namespace: testNS1},
		Spec:       kubemootv1alpha1.AgentSpec{Capabilities: []string{testReasoning}},
	}
	notReady := &kubemootv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "m1", Namespace: testNS1},
		Status:     kubemootv1alpha1.ModelStatus{Ready: false},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(agent, notReady).WithStatusSubresource(agent).Build()
	r := &AgentReconciler{Client: cli, Scheme: scheme}

	res, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "a1", Namespace: testNS1},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("an unschedulable agent should requeue for a later retry")
	}

	got := &kubemootv1alpha1.Agent{}
	if err := cli.Get(ctx, types.NamespacedName{Name: "a1", Namespace: testNS1}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != testPhaseUnschedulable {
		t.Errorf("expected Unschedulable phase, got %q (msg %q)", got.Status.Phase, got.Status.Message)
	}
	if got.Status.Ready {
		t.Error("an unschedulable agent must not be Ready")
	}
}

// The happy path: a Ready Model whose ProviderRef resolves to a Ready
// ModelProvider (VRAMMib 0 -> always fits, no scheduling policy -> no require
// selector) makes the agent schedulable, so reconcile creates the Deployment and
// Service. Exercises pickModel's success path, resolveProvider, modelFitsProvider,
// ensurePolicyConfigMap, ensureDeployment (+buildDeployment/env/labels),
// ensureService, and refreshStatus.
func TestAgentReconcile_SchedulesAndDeploys(t *testing.T) {
	ctx := context.Background()
	scheme := gatewayScheme(t) // kubemoot + appsv1 + corev1
	agent := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "a1", Namespace: testNS1, Labels: map[string]string{labelCrew: "crew1"}},
		Spec:       kubemootv1alpha1.AgentSpec{Capabilities: []string{testReasoning}},
	}
	provider := &kubemootv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "prov1", Namespace: testNS1},
		Spec:       kubemootv1alpha1.ModelProviderSpec{Type: kubemootv1alpha1.ProviderTypeOllama, Endpoint: "http://prov1:11434"},
		Status:     kubemootv1alpha1.ModelProviderStatus{Ready: true},
	}
	model := &kubemootv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "m1", Namespace: testNS1},
		Spec:       kubemootv1alpha1.ModelSpec{Model: testModelID, ProviderRef: "prov1"},
		Status:     kubemootv1alpha1.ModelStatus{Ready: true},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(agent, provider, model).
		WithStatusSubresource(agent, provider, model).Build()
	r := &AgentReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "a1", Namespace: testNS1}}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	deps := &appsv1.DeploymentList{}
	if err := cli.List(ctx, deps, client.InNamespace(testNS1)); err != nil || len(deps.Items) != 1 {
		t.Fatalf("a schedulable agent should produce one Deployment, got %d (err %v)", len(deps.Items), err)
	}
	svcs := &corev1.ServiceList{}
	if err := cli.List(ctx, svcs, client.InNamespace(testNS1)); err != nil || len(svcs.Items) != 1 {
		t.Fatalf("expected one agent Service, got %d (err %v)", len(svcs.Items), err)
	}
	got := &kubemootv1alpha1.Agent{}
	_ = cli.Get(ctx, req.NamespacedName, got)
	if got.Status.Phase == testPhaseUnschedulable {
		t.Errorf("agent should be schedulable, got Unschedulable: %q", got.Status.Message)
	}
}

// Reconciling an Agent that no longer exists is a clean no-op.
func TestAgentReconcile_NotFoundIsNoOp(t *testing.T) {
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &AgentReconciler{Client: cli, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: testAbsent, Namespace: testNS1},
	}); err != nil {
		t.Errorf("absent agent reconcile must be a no-op, got %v", err)
	}
}

// The pod template carries no scheduling output beyond the spec-derived default
// endpoint: provider bindings sit on the Deployment labels only, so a binding
// change updates metadata without changing the template (no pod roll).
func TestBuildDeployment_PodTemplateOmitsProviderLabels(t *testing.T) {
	scheme := agentReconcileScheme(t)
	r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Scheme: scheme, ConfigCache: NewConfigCache()}
	agent := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "a1", Namespace: testNS1, Labels: map[string]string{labelCrew: testCrewA}}}
	pickOn := func(provider string) *modelPick {
		return &modelPick{
			ModelID:  testModelID,
			Endpoint: "http://" + provider + ":11434",
			Provider: &kubemootv1alpha1.ModelProvider{ObjectMeta: metav1.ObjectMeta{Name: provider}},
		}
	}

	d := r.buildDeployment(context.Background(), agent, pickOn("rig0"), pickOn("rig1"), "cm", "hash")
	if d.Labels[labelMullingProvider] != "rig0" || d.Labels[labelTriageProvider] != "rig1" {
		t.Errorf("Deployment labels should record the bindings, got %v", d.Labels)
	}
	for _, key := range []string{labelMullingProvider, labelTriageProvider} {
		if _, found := d.Spec.Template.Labels[key]; found {
			t.Errorf("pod template must not carry %s", key)
		}
	}
	if d.Spec.Template.Labels[labelAgent] != "a1" || d.Spec.Selector.MatchLabels[labelAgent] != "a1" {
		t.Errorf("pod template keeps the identity labels the selector matches, got %v", d.Spec.Template.Labels)
	}

	// Same endpoint, different recorded provider name: identical template hash.
	renamed := func(provider string) *modelPick {
		pick := pickOn(provider)
		pick.Endpoint = "http://shared:11434"
		return pick
	}
	first := r.buildDeployment(context.Background(), agent, renamed("rig0"), renamed("rig1"), "cm", "hash")
	second := r.buildDeployment(context.Background(), agent, renamed("rig8"), renamed("rig9"), "cm", "hash")
	if computeDeploymentHash(first) != computeDeploymentHash(second) {
		t.Error("a provider binding change alone must not change the template hash")
	}
}
