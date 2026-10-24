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
	"strings"
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// unsupportedServerTypes are ModelServer types the API server rejects.
var unsupportedServerTypes = []string{"openai", "anthropic", "bogus"}

const (
	mpEmbedName  = "embed"
	mpEmbedModel = "nomic-embed-text"
)

func reconcileMP(t *testing.T, p *kubemootv1alpha1.ModelServer) *kubemootv1alpha1.ModelServer {
	t.Helper()
	scheme := agentReconcileScheme(t) // kubemoot types; ModelServer is one
	cli := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(p).WithStatusSubresource(p).Build()
	r := &ModelServerReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: p.Name, Namespace: p.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile %s: %v", p.Name, err)
	}
	got := &kubemootv1alpha1.ModelServer{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: p.Name, Namespace: p.Namespace}, got); err != nil {
		t.Fatal(err)
	}
	return got
}

// An Ollama server whose /api/version answers 200 reconciles to Ready. Covers
// Reconcile's ollama dispatch, reconcileOllama connectivity, and updateStatus.
func TestModelServerReconcile_OllamaReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"0.3.0"}`))
	}))
	defer srv.Close()
	got := reconcileMP(t, &kubemootv1alpha1.ModelServer{
		ObjectMeta: metav1.ObjectMeta{Name: testOllamaGPU, Namespace: testNS1},
		Spec:       kubemootv1alpha1.ModelServerSpec{Type: kubemootv1alpha1.ServerTypeOllama, Endpoint: srv.URL},
	})
	if !got.Status.Ready {
		t.Errorf("expected a reachable Ollama server to be Ready, status=%+v", got.Status)
	}
}

// An Ollama server with no endpoint fails fast (Endpoint is required).
func TestModelServerReconcile_OllamaEmptyEndpoint(t *testing.T) {
	got := reconcileMP(t, &kubemootv1alpha1.ModelServer{
		ObjectMeta: metav1.ObjectMeta{Name: "ollama-x", Namespace: testNS1},
		Spec:       kubemootv1alpha1.ModelServerSpec{Type: kubemootv1alpha1.ServerTypeOllama},
	})
	if got.Status.Ready {
		t.Error("an Ollama server with no endpoint must not be Ready")
	}
}

// A non-ollama type (an object stored before the schema tightened) is
// reported Ready=False with reason Unsupported, never as configured.
func TestModelServerReconcile_UnsupportedTypes(t *testing.T) {
	for _, typ := range unsupportedServerTypes {
		t.Run(typ, func(t *testing.T) {
			got := reconcileMP(t, &kubemootv1alpha1.ModelServer{
				ObjectMeta: metav1.ObjectMeta{Name: typ, Namespace: testNS1},
				Spec: kubemootv1alpha1.ModelServerSpec{
					Type:      kubemootv1alpha1.ServerType(typ),
					SecretRef: "key",
				},
			})
			if got.Status.Ready {
				t.Fatal("an unsupported server type must not be Ready")
			}
			cond := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "Unsupported" {
				t.Fatalf("want Ready=False reason Unsupported, got %+v", cond)
			}
			if cond.Message != kubemootv1alpha1.UnsupportedServerTypeMessage {
				t.Errorf("unexpected message %q", cond.Message)
			}
		})
	}
}

// Reconciling an absent ModelServer is a clean no-op.
func TestModelServerReconcile_NotFoundIsNoOp(t *testing.T) {
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &ModelServerReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: testAbsent, Namespace: testNS1},
	}); err != nil {
		t.Errorf("absent server reconcile must be a no-op, got %v", err)
	}
}

// An EmbeddingModel on a server with an unsupported type (stored before the
// schema tightened, and marked ready) reports the same message as the
// server, never Ready.
func TestEmbeddingModelReconcile_UnsupportedServerType(t *testing.T) {
	scheme := agentReconcileScheme(t)
	server := &kubemootv1alpha1.ModelServer{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: testNS1},
		Spec:       kubemootv1alpha1.ModelServerSpec{Type: kubemootv1alpha1.ServerType(unsupportedServerTypes[0])},
		Status:     kubemootv1alpha1.ModelServerStatus{Ready: true},
	}
	em := &kubemootv1alpha1.EmbeddingModel{
		ObjectMeta: metav1.ObjectMeta{Name: mpEmbedName, Namespace: testNS1, Finalizers: []string{embeddingModelFinalizer}},
		Spec:       kubemootv1alpha1.EmbeddingModelSpec{ServerRef: "legacy", Model: mpEmbedModel},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(server, em).WithStatusSubresource(server, em).Build()
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
	if got.Status.Ready || !strings.Contains(got.Status.Message, "does not serve") {
		t.Errorf("want not Ready because the server does not serve the model, got ready=%v message=%q", got.Status.Ready, got.Status.Message)
	}
}

// With the scheduler setting off (the chart's default, and the quickstart's), an Ollama
// server still reports the models it holds, because agents place every call against
// that list: a server with no list is never a candidate and every call is refused.
func TestModelServerReconcile_OllamaReportsItsModelsWithTheSchedulerOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case testPathAPITags:
			_, _ = w.Write([]byte(`{"models":[{"name":"qwen2.5:1.5b","size":986061892}]}`))
		case testPathAPIPS:
			_, _ = w.Write([]byte(`{"models":[]}`))
		default:
			_, _ = w.Write([]byte(`{"version":"0.34.4"}`))
		}
	}))
	defer srv.Close()
	got := reconcileMP(t, &kubemootv1alpha1.ModelServer{
		ObjectMeta: metav1.ObjectMeta{Name: testOllamaGPU, Namespace: testNS1},
		Spec:       kubemootv1alpha1.ModelServerSpec{Type: kubemootv1alpha1.ServerTypeOllama, Endpoint: srv.URL},
	})
	if got.Status.Capacity == nil {
		t.Fatal("capacity was not discovered with the scheduler setting off")
	}
	found := false
	for _, m := range got.Status.Capacity.AvailableModels {
		if m.Name == "qwen2.5:1.5b" {
			found = true
		}
	}
	if !found {
		t.Errorf("available models = %+v, want qwen2.5:1.5b", got.Status.Capacity.AvailableModels)
	}
}

// A server that holds no models reports an empty list, not an error.
func TestModelServerReconcile_OllamaWithNoModelsStaysReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case testPathAPITags, testPathAPIPS:
			_, _ = w.Write([]byte(`{"models":[]}`))
		default:
			_, _ = w.Write([]byte(`{"version":"0.34.4"}`))
		}
	}))
	defer srv.Close()
	got := reconcileMP(t, &kubemootv1alpha1.ModelServer{
		ObjectMeta: metav1.ObjectMeta{Name: testOllamaGPU, Namespace: testNS1},
		Spec:       kubemootv1alpha1.ModelServerSpec{Type: kubemootv1alpha1.ServerTypeOllama, Endpoint: srv.URL},
	})
	if !got.Status.Ready {
		t.Errorf("a reachable server with no models must stay Ready, status=%+v", got.Status)
	}
	if got.Status.Capacity == nil || len(got.Status.Capacity.AvailableModels) != 0 {
		t.Errorf("want an empty available list, got %+v", got.Status.Capacity)
	}
}
