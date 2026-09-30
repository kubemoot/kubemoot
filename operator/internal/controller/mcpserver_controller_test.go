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
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// A managed (image-backed) stdio MCPServer reconciles to a Deployment plus a
// Service (stdio defaults proxy injection on, which exposes HTTP). First reconcile
// installs the finalizer + requeues; the second builds the Deployment (with the
// bridge sidecar) and the Service. Covers Reconcile / ensureFinalizer /
// reconcileDeployment / createDeployment / buildDeployment / applyBridgeSidecar /
// shouldCreateMCPServerService / reconcileService / updateStatusFromDeployment.
func TestMCPServerReconcile_ManagedCreatesDeploymentAndService(t *testing.T) {
	ctx := context.Background()
	scheme := gatewayScheme(t)
	ms := &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv1", Namespace: "ns1"},
		Spec: kubemootv1alpha1.MCPServerSpec{
			Image:     "registry/mcp:1",
			Transport: kubemootv1alpha1.TransportStdio,
		},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ms).WithStatusSubresource(ms).Build()
	r := &MCPServerReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "srv1", Namespace: "ns1"}}

	res, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if !res.Requeue {
		t.Error("expected requeue after finalizer addition")
	}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	deps := &appsv1.DeploymentList{}
	if err := cli.List(ctx, deps, client.InNamespace("ns1")); err != nil || len(deps.Items) != 1 {
		t.Fatalf("expected exactly one Deployment, got %d (err %v)", len(deps.Items), err)
	}
	svcs := &corev1.ServiceList{}
	if err := cli.List(ctx, svcs, client.InNamespace("ns1")); err != nil || len(svcs.Items) != 1 {
		t.Fatalf("expected a Service for the stdio+proxy server, got %d (err %v)", len(svcs.Items), err)
	}
}

// An external MCPServer (ExternalEndpoint set) needs no Deployment.
func TestMCPServerReconcile_ExternalNeedsNoDeployment(t *testing.T) {
	ctx := context.Background()
	scheme := gatewayScheme(t)
	ms := &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "ext1", Namespace: "ns1"},
		Spec:       kubemootv1alpha1.MCPServerSpec{ExternalEndpoint: "http://external:9000"},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ms).WithStatusSubresource(ms).Build()
	r := &MCPServerReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "ext1", Namespace: "ns1"}}

	// Reconcile twice (finalizer, then external path).
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	deps := &appsv1.DeploymentList{}
	if err := cli.List(ctx, deps, client.InNamespace("ns1")); err != nil || len(deps.Items) != 0 {
		t.Fatalf("external server must not create a Deployment, got %d (err %v)", len(deps.Items), err)
	}
}

// A managed server with neither Image nor ExternalEndpoint is an Error.
func TestMCPServerReconcile_NoImageNoEndpointIsError(t *testing.T) {
	ctx := context.Background()
	scheme := gatewayScheme(t)
	ms := &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "bad1", Namespace: "ns1"},
		Spec:       kubemootv1alpha1.MCPServerSpec{Transport: kubemootv1alpha1.TransportStdio},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ms).WithStatusSubresource(ms).Build()
	r := &MCPServerReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "bad1", Namespace: "ns1"}}
	_, _ = r.Reconcile(ctx, req) // finalizer
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &kubemootv1alpha1.MCPServer{}
	if err := cli.Get(ctx, req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Ready {
		t.Error("a server with no image and no endpoint must not be Ready")
	}
}

// Reconciling an absent MCPServer is a clean no-op.
func TestMCPServerReconcile_NotFoundIsNoOp(t *testing.T) {
	scheme := gatewayScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &MCPServerReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "absent", Namespace: "ns1"},
	}); err != nil {
		t.Errorf("absent MCPServer reconcile must be a no-op, got %v", err)
	}
}
