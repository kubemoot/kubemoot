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

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// unsupportedProviderTypes are ModelProvider types the API server rejects.
var unsupportedProviderTypes = []string{"openai", "anthropic", "bogus"}

const (
	mpEmbedName  = "embed"
	mpEmbedModel = "nomic-embed-text"
)

func reconcileMP(t *testing.T, p *kubemootv1alpha1.ModelProvider) *kubemootv1alpha1.ModelProvider {
	t.Helper()
	scheme := agentReconcileScheme(t) // kubemoot types; ModelProvider is one
	cli := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(p).WithStatusSubresource(p).Build()
	r := &ModelProviderReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: p.Name, Namespace: p.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile %s: %v", p.Name, err)
	}
	got := &kubemootv1alpha1.ModelProvider{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: p.Name, Namespace: p.Namespace}, got); err != nil {
		t.Fatal(err)
	}
	return got
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
	got := reconcileMP(t, &kubemootv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: testOllamaGPU, Namespace: testNS1},
		Spec:       kubemootv1alpha1.ModelProviderSpec{Type: kubemootv1alpha1.ProviderTypeOllama, Endpoint: srv.URL},
	})
	if !got.Status.Ready {
		t.Errorf("expected a reachable Ollama provider to be Ready, status=%+v", got.Status)
	}
}

// An Ollama provider with no endpoint fails fast (Endpoint is required).
func TestModelProviderReconcile_OllamaEmptyEndpoint(t *testing.T) {
	got := reconcileMP(t, &kubemootv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "ollama-x", Namespace: testNS1},
		Spec:       kubemootv1alpha1.ModelProviderSpec{Type: kubemootv1alpha1.ProviderTypeOllama},
	})
	if got.Status.Ready {
		t.Error("an Ollama provider with no endpoint must not be Ready")
	}
}

// A non-ollama type (an object stored before the schema tightened) is
// reported Ready=False with reason Unsupported, never as configured.
func TestModelProviderReconcile_UnsupportedTypes(t *testing.T) {
	for _, typ := range unsupportedProviderTypes {
		t.Run(typ, func(t *testing.T) {
			got := reconcileMP(t, &kubemootv1alpha1.ModelProvider{
				ObjectMeta: metav1.ObjectMeta{Name: typ, Namespace: testNS1},
				Spec: kubemootv1alpha1.ModelProviderSpec{
					Type:      kubemootv1alpha1.ProviderType(typ),
					SecretRef: "key",
				},
			})
			if got.Status.Ready {
				t.Fatal("an unsupported provider type must not be Ready")
			}
			cond := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "Unsupported" {
				t.Fatalf("want Ready=False reason Unsupported, got %+v", cond)
			}
			if cond.Message != kubemootv1alpha1.UnsupportedProviderTypeMessage {
				t.Errorf("unexpected message %q", cond.Message)
			}
		})
	}
}

// Reconciling an absent ModelProvider is a clean no-op.
func TestModelProviderReconcile_NotFoundIsNoOp(t *testing.T) {
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &ModelProviderReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: testAbsent, Namespace: testNS1},
	}); err != nil {
		t.Errorf("absent provider reconcile must be a no-op, got %v", err)
	}
}

// An EmbeddingModel on a provider with an unsupported type (stored before the
// schema tightened, and marked ready) reports the same message as the
// provider, never Ready.
func TestEmbeddingModelReconcile_UnsupportedProviderType(t *testing.T) {
	scheme := agentReconcileScheme(t)
	provider := &kubemootv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: testNS1},
		Spec:       kubemootv1alpha1.ModelProviderSpec{Type: kubemootv1alpha1.ProviderType(unsupportedProviderTypes[0])},
		Status:     kubemootv1alpha1.ModelProviderStatus{Ready: true},
	}
	em := &kubemootv1alpha1.EmbeddingModel{
		ObjectMeta: metav1.ObjectMeta{Name: mpEmbedName, Namespace: testNS1, Finalizers: []string{embeddingModelFinalizer}},
		Spec:       kubemootv1alpha1.EmbeddingModelSpec{ProviderRef: "legacy", Model: mpEmbedModel},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(provider, em).WithStatusSubresource(provider, em).Build()
	r := &EmbeddingModelReconciler{Client: cli, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: em.Name, Namespace: em.Namespace},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &kubemootv1alpha1.EmbeddingModel{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: em.Name, Namespace: em.Namespace}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Ready || got.Status.Message != kubemootv1alpha1.UnsupportedProviderTypeMessage {
		t.Errorf("want not Ready with the unsupported-type message, got ready=%v message=%q", got.Status.Ready, got.Status.Message)
	}
}
