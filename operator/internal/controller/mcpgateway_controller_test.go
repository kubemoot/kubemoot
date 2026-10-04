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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func gatewayScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		kubemootv1alpha1.AddToScheme, appsv1.AddToScheme, corev1.AddToScheme,
	} {
		if err := add(s); err != nil {
			t.Fatalf("add to scheme: %v", err)
		}
	}
	return s
}

// A first reconcile installs the finalizer and requeues; the second creates the
// gateway Deployment + Service (owner-referenced for GC). The freshly-created
// Deployment has no ready replicas, so MCPServer registration (which needs the
// gateway HTTP API) is correctly skipped - no HTTPClient/NATS required.
func TestMCPGatewayReconcile_CreatesDeploymentAndService(t *testing.T) {
	ctx := context.Background()
	scheme := gatewayScheme(t)
	gw := &kubemootv1alpha1.MCPGateway{
		ObjectMeta: metav1.ObjectMeta{Name: testGateway1, Namespace: testNS1},
		Spec: kubemootv1alpha1.MCPGatewaySpec{
			Implementation: kubemootv1alpha1.ImplementationKubemoot,
			Port:           8080,
		},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(gw).WithStatusSubresource(gw).Build()
	r := &MCPGatewayReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: testGateway1, Namespace: testNS1}}

	res, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if !isFinalizerRequeue(res) {
		t.Error("expected requeue after finalizer addition")
	}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	name := types.NamespacedName{Name: testGateway1, Namespace: testNS1}
	dep := &appsv1.Deployment{}
	if err := cli.Get(ctx, name, dep); err != nil {
		t.Fatalf("expected the gateway Deployment to be created: %v", err)
	}
	if len(dep.OwnerReferences) == 0 || dep.OwnerReferences[0].Name != testGateway1 {
		t.Errorf("expected a gateway owner reference on the Deployment, got %v", dep.OwnerReferences)
	}
	if err := cli.Get(ctx, name, &corev1.Service{}); err != nil {
		t.Fatalf("expected the gateway Service to be created: %v", err)
	}
}

// Reconciling a gateway that no longer exists is a clean no-op (IgnoreNotFound).
func TestMCPGatewayReconcile_NotFoundIsNoOp(t *testing.T) {
	scheme := gatewayScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &MCPGatewayReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: testAbsent, Namespace: testNS1},
	}); err != nil {
		t.Errorf("reconcile of an absent gateway must be a no-op, got %v", err)
	}
}
