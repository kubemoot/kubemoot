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
// later retry. Exercises Reconcile's scheduling head: currentProviderPicks,
// pickModel -> findPolicyAndRule -> feasibleCandidates -> evaluateCandidate ->
// candidateFeasible, and markUnschedulable.
func TestAgentReconcile_UnschedulableWhenNoReadyModel(t *testing.T) {
	ctx := context.Background()
	scheme := agentReconcileScheme(t)
	agent := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "a1", Namespace: "ns1"},
		Spec:       kubemootv1alpha1.AgentSpec{Capabilities: []string{"reasoning"}},
	}
	notReady := &kubemootv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "m1", Namespace: "ns1"},
		Status:     kubemootv1alpha1.ModelStatus{Ready: false},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(agent, notReady).WithStatusSubresource(agent).Build()
	r := &AgentReconciler{Client: cli, Scheme: scheme}

	res, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "a1", Namespace: "ns1"},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("an unschedulable agent should requeue for a later retry")
	}

	got := &kubemootv1alpha1.Agent{}
	if err := cli.Get(ctx, types.NamespacedName{Name: "a1", Namespace: "ns1"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != "Unschedulable" {
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
// applySticky, ensurePolicyConfigMap, ensureDeployment (+buildDeployment/env/labels),
// ensureService, and refreshStatus.
func TestAgentReconcile_SchedulesAndDeploys(t *testing.T) {
	ctx := context.Background()
	scheme := gatewayScheme(t) // kubemoot + appsv1 + corev1
	agent := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "a1", Namespace: "ns1", Labels: map[string]string{labelCrew: "crew1"}},
		Spec:       kubemootv1alpha1.AgentSpec{Capabilities: []string{"reasoning"}},
	}
	provider := &kubemootv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "prov1", Namespace: "ns1"},
		Spec:       kubemootv1alpha1.ModelProviderSpec{Type: kubemootv1alpha1.ProviderTypeOllama, Endpoint: "http://prov1:11434"},
		Status:     kubemootv1alpha1.ModelProviderStatus{Ready: true},
	}
	model := &kubemootv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "m1", Namespace: "ns1"},
		Spec:       kubemootv1alpha1.ModelSpec{Model: "qwen3:8b", ProviderRef: "prov1"},
		Status:     kubemootv1alpha1.ModelStatus{Ready: true},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(agent, provider, model).
		WithStatusSubresource(agent, provider, model).Build()
	r := &AgentReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "a1", Namespace: "ns1"}}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	deps := &appsv1.DeploymentList{}
	if err := cli.List(ctx, deps, client.InNamespace("ns1")); err != nil || len(deps.Items) != 1 {
		t.Fatalf("a schedulable agent should produce one Deployment, got %d (err %v)", len(deps.Items), err)
	}
	svcs := &corev1.ServiceList{}
	if err := cli.List(ctx, svcs, client.InNamespace("ns1")); err != nil || len(svcs.Items) != 1 {
		t.Fatalf("expected one agent Service, got %d (err %v)", len(svcs.Items), err)
	}
	got := &kubemootv1alpha1.Agent{}
	_ = cli.Get(ctx, req.NamespacedName, got)
	if got.Status.Phase == "Unschedulable" {
		t.Errorf("agent should be schedulable, got Unschedulable: %q", got.Status.Message)
	}
}

// Reconciling an Agent that no longer exists is a clean no-op.
func TestAgentReconcile_NotFoundIsNoOp(t *testing.T) {
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &AgentReconciler{Client: cli, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "absent", Namespace: "ns1"},
	}); err != nil {
		t.Errorf("absent agent reconcile must be a no-op, got %v", err)
	}
}
