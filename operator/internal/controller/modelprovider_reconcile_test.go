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
	"net/http"
	"net/http/httptest"
	"testing"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func reconcileMP(t *testing.T, p *kubemootv1alpha1.ModelProvider) (*kubemootv1alpha1.ModelProvider, ctrl.Result) {
	t.Helper()
	scheme := agentReconcileScheme(t) // kubemoot types; ModelProvider is one
	cli := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(p).WithStatusSubresource(p).Build()
	r := &ModelProviderReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: p.Name, Namespace: p.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile %s: %v", p.Name, err)
	}
	got := &kubemootv1alpha1.ModelProvider{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: p.Name, Namespace: p.Namespace}, got); err != nil {
		t.Fatal(err)
	}
	return got, res
}

// An Ollama provider whose /api/version answers 200 reconciles to Ready. The
// scheduler is disabled (default ConfigCache), so the HTTP capacity probing is
// skipped. Covers Reconcile's ollama dispatch, reconcileOllama connectivity, and
// updateStatus.
func TestModelProviderReconcile_OllamaReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"0.3.0"}`))
	}))
	defer srv.Close()
	got, _ := reconcileMP(t, &kubemootv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "ollama-gpu", Namespace: "ns1"},
		Spec:       kubemootv1alpha1.ModelProviderSpec{Type: kubemootv1alpha1.ProviderTypeOllama, Endpoint: srv.URL},
	})
	if !got.Status.Ready {
		t.Errorf("expected a reachable Ollama provider to be Ready, status=%+v", got.Status)
	}
}

// An Ollama provider with no endpoint fails fast (Endpoint is required).
func TestModelProviderReconcile_OllamaEmptyEndpoint(t *testing.T) {
	got, _ := reconcileMP(t, &kubemootv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "ollama-x", Namespace: "ns1"},
		Spec:       kubemootv1alpha1.ModelProviderSpec{Type: kubemootv1alpha1.ProviderTypeOllama},
	})
	if got.Status.Ready {
		t.Error("an Ollama provider with no endpoint must not be Ready")
	}
}

// A cloud provider with no SecretRef fails (SecretRef is required). Covers
// Reconcile's cloud dispatch + reconcileCloudProvider guard.
func TestModelProviderReconcile_CloudMissingSecret(t *testing.T) {
	got, _ := reconcileMP(t, &kubemootv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "anthropic", Namespace: "ns1"},
		Spec:       kubemootv1alpha1.ModelProviderSpec{Type: kubemootv1alpha1.ProviderTypeAnthropic},
	})
	if got.Status.Ready {
		t.Error("a cloud provider with no SecretRef must not be Ready")
	}
}

// An unknown provider type is reported as Failed (the default switch branch).
func TestModelProviderReconcile_UnknownType(t *testing.T) {
	got, _ := reconcileMP(t, &kubemootv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "weird", Namespace: "ns1"},
		Spec:       kubemootv1alpha1.ModelProviderSpec{Type: kubemootv1alpha1.ProviderType("bogus")},
	})
	if got.Status.Ready {
		t.Error("an unknown provider type must not be Ready")
	}
}

// Reconciling an absent ModelProvider is a clean no-op.
func TestModelProviderReconcile_NotFoundIsNoOp(t *testing.T) {
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &ModelProviderReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "absent", Namespace: "ns1"},
	}); err != nil {
		t.Errorf("absent provider reconcile must be a no-op, got %v", err)
	}
}
