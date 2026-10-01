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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const testFinalizer = "kubemoot.ai/test-finalizer"

// patchOnlyClient serves one stored MCPGateway with an explicit false on a
// default-true field, fails the test on any full Update, and counts patches.
func patchOnlyClient(t *testing.T) (client.Client, *int) {
	t.Helper()
	gw := &kubemootv1alpha1.MCPGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "ns"},
		Spec:       kubemootv1alpha1.MCPGatewaySpec{AdminUI: ptr.To(false), Port: 9090},
	}
	patches := 0
	cli := fake.NewClientBuilder().WithScheme(gatewayScheme(t)).WithObjects(gw).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
				t.Error("finalizer helpers must not send a full Update")
				return nil
			},
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
				patches++
				return c.Patch(ctx, obj, p, opts...)
			},
		}).Build()
	return cli, &patches
}

func getGateway(t *testing.T, cli client.Client) *kubemootv1alpha1.MCPGateway {
	t.Helper()
	gw := &kubemootv1alpha1.MCPGateway{}
	if err := cli.Get(context.Background(), client.ObjectKey{Name: "gw", Namespace: "ns"}, gw); err != nil {
		t.Fatal(err)
	}
	return gw
}

// Adding a finalizer patches metadata only: the stored spec, including the
// explicit false, is unchanged, and adding it again sends nothing.
func TestAddFinalizerLeavesSpecAlone(t *testing.T) {
	ctx := context.Background()
	cli, patches := patchOnlyClient(t)
	gw := getGateway(t, cli)
	for range 2 {
		if err := addFinalizer(ctx, cli, gw, testFinalizer); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	if *patches != 1 {
		t.Errorf("adding a present finalizer must send nothing: %d patches", *patches)
	}
	stored := getGateway(t, cli)
	if len(stored.Finalizers) != 1 || stored.Finalizers[0] != testFinalizer {
		t.Errorf("finalizers = %v, want [%s]", stored.Finalizers, testFinalizer)
	}
	if stored.Spec.AdminUIEnabled() || stored.Spec.Port != 9090 {
		t.Errorf("spec changed by a finalizer patch: %+v", stored.Spec)
	}
}

func TestRemoveFinalizer(t *testing.T) {
	ctx := context.Background()
	cli, _ := patchOnlyClient(t)
	gw := getGateway(t, cli)
	if err := addFinalizer(ctx, cli, gw, testFinalizer); err != nil {
		t.Fatal(err)
	}
	if err := removeFinalizer(ctx, cli, gw, testFinalizer); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := getGateway(t, cli).Finalizers; len(got) != 0 {
		t.Errorf("finalizer not removed: %v", got)
	}
}

// A stale copy loses to a concurrent writer instead of overwriting its finalizers.
func TestFinalizerPatchUsesOptimisticLock(t *testing.T) {
	ctx := context.Background()
	gw := &kubemootv1alpha1.MCPGateway{ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "ns"}}
	cli := fake.NewClientBuilder().WithScheme(gatewayScheme(t)).WithObjects(gw).Build()

	stale := &kubemootv1alpha1.MCPGateway{}
	if err := cli.Get(ctx, client.ObjectKeyFromObject(gw), stale); err != nil {
		t.Fatal(err)
	}
	fresh := stale.DeepCopy()
	if err := addFinalizer(ctx, cli, fresh, "kubemoot.ai/other"); err != nil {
		t.Fatal(err)
	}
	if err := addFinalizer(ctx, cli, stale, testFinalizer); err == nil {
		t.Error("a patch from a stale copy must conflict")
	}
}
