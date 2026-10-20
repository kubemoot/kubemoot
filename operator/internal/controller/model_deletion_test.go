/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	delTestProvider = "gpu-ollama"
	delTestTag      = "qwen3:8b"
)

func deletionModel(name, ns, providerRef, tag string, deleting bool) *aiv1alpha1.Model {
	m := &aiv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(ns + "-" + name)},
		Spec:       aiv1alpha1.ModelSpec{ProviderRef: providerRef, Model: tag},
	}
	if deleting {
		now := metav1.Now()
		m.DeletionTimestamp = &now
		m.Finalizers = []string{modelFinalizer}
	}
	return m
}

func deletionProvider(endpoint string) *aiv1alpha1.ModelProvider {
	return &aiv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: delTestProvider, Namespace: "kubemoot-system"},
		Spec:       aiv1alpha1.ModelProviderSpec{Type: aiv1alpha1.ProviderTypeOllama, Endpoint: endpoint},
		Status:     aiv1alpha1.ModelProviderStatus{Ready: true},
	}
}

// notReady is the provider with its Ollama scaled to zero: the operator has marked it not Ready.
func notReady(p *aiv1alpha1.ModelProvider) *aiv1alpha1.ModelProvider {
	p.Status.Ready = false
	p.Status.Phase = phaseFailed
	return p
}

// deleteServer counts calls to Ollama's delete endpoint and answers with status.
func deleteServer(status int, calls *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/delete" {
			atomic.AddInt32(calls, 1)
		}
		w.WriteHeader(status)
	}))
}

func runDeletion(t *testing.T, srv *httptest.Server, provider *aiv1alpha1.ModelProvider, deleting *aiv1alpha1.Model, others ...client.Object) (*ModelReconciler, error) {
	t.Helper()
	objs := append([]client.Object{deleting}, others...)
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).WithObjects(objs...).Build()
	r := &ModelReconciler{Client: cl, HTTPClient: srv.Client()}
	_, err := r.handleDeletion(context.Background(), deleting, provider)
	return r, err
}

func finalizerGone(t *testing.T, r *ModelReconciler, m *aiv1alpha1.Model) bool {
	t.Helper()
	got := &aiv1alpha1.Model{}
	err := r.Get(context.Background(), client.ObjectKeyFromObject(m), got)
	return err != nil || len(got.Finalizers) == 0
}

func TestHandleDeletion_SharedTagKept(t *testing.T) {
	var calls int32
	srv := deleteServer(http.StatusOK, &calls)
	defer srv.Close()
	m := deletionModel("a", "team-a", delTestProvider, delTestTag, true)
	other := deletionModel("b", "team-b", delTestProvider, delTestTag, false)
	r, err := runDeletion(t, srv, deletionProvider(srv.URL), m, other)
	if err != nil {
		t.Fatalf("handleDeletion: %v", err)
	}
	if calls != 0 {
		t.Errorf("shared tag must not be deleted from the model server; got %d delete calls", calls)
	}
	if !finalizerGone(t, r, m) {
		t.Error("finalizer should be released")
	}
}

func TestHandleDeletion_SoleTagDeleted(t *testing.T) {
	var calls int32
	srv := deleteServer(http.StatusOK, &calls)
	defer srv.Close()
	m := deletionModel("a", "team-a", delTestProvider, delTestTag, true)
	otherTag := deletionModel("b", "team-b", delTestProvider, "other:1b", false)
	otherProvider := deletionModel("c", "team-c", "second-provider", delTestTag, false)
	alsoDeleting := deletionModel("d", "team-d", delTestProvider, delTestTag, true)
	r, err := runDeletion(t, srv, deletionProvider(srv.URL), m, otherTag, otherProvider, alsoDeleting)
	if err != nil {
		t.Fatalf("handleDeletion: %v", err)
	}
	if calls != 1 {
		t.Errorf("sole user of the tag should trigger one delete; got %d", calls)
	}
	if !finalizerGone(t, r, m) {
		t.Error("finalizer should be released")
	}
}

func TestHandleDeletion_DeleteErrorRetried(t *testing.T) {
	var calls int32
	srv := deleteServer(http.StatusInternalServerError, &calls)
	defer srv.Close()
	m := deletionModel("a", "team-a", delTestProvider, delTestTag, true)
	r, err := runDeletion(t, srv, deletionProvider(srv.URL), m)
	if err == nil {
		t.Fatal("a failed model-server delete must be returned so the deletion retries")
	}
	if finalizerGone(t, r, m) {
		t.Error("finalizer must stay while the delete is failing")
	}
}

func TestHandleDeletion_NotFoundOnServerIsDone(t *testing.T) {
	var calls int32
	srv := deleteServer(http.StatusNotFound, &calls)
	defer srv.Close()
	m := deletionModel("a", "team-a", delTestProvider, delTestTag, true)
	r, err := runDeletion(t, srv, deletionProvider(srv.URL), m)
	if err != nil || !finalizerGone(t, r, m) {
		t.Errorf("a model the server no longer has should release the finalizer; err=%v", err)
	}
}

func TestHandleDeletion_ProviderGone(t *testing.T) {
	var calls int32
	srv := deleteServer(http.StatusOK, &calls)
	defer srv.Close()
	m := deletionModel("a", "team-a", delTestProvider, delTestTag, true)
	r, err := runDeletion(t, srv, nil, m)
	if err != nil {
		t.Fatalf("handleDeletion: %v", err)
	}
	if calls != 0 {
		t.Errorf("no provider means no delete call; got %d", calls)
	}
	if !finalizerGone(t, r, m) {
		t.Error("finalizer should be released when the provider is gone")
	}
}

func TestReconcile_DeletingModelWithMissingProviderReleased(t *testing.T) {
	m := deletionModel("a", "team-a", delTestProvider, delTestTag, true)
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).WithObjects(m).Build()
	r := &ModelReconciler{Client: cl}
	if _, err := r.Reconcile(context.Background(), reconcileRequest(m)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !finalizerGone(t, r, m) {
		t.Error("a deleting Model whose provider no longer exists must release its finalizer")
	}
}

// A provider that is not Ready cannot be asked: the finalizer is released without a
// delete call, however the server would answer.
func TestHandleDeletion_ProviderNotReadyReleasesWithoutAskingIt(t *testing.T) {
	var calls int32
	srv := deleteServer(http.StatusInternalServerError, &calls)
	defer srv.Close()
	m := deletionModel("a", "team-a", delTestProvider, delTestTag, true)
	r, err := runDeletion(t, srv, notReady(deletionProvider(srv.URL)), m)
	if err != nil {
		t.Fatalf("a provider that is not Ready must not hold the finalizer: %v", err)
	}
	if calls != 0 {
		t.Errorf("a provider that is not Ready is not asked; got %d delete calls", calls)
	}
	if !finalizerGone(t, r, m) {
		t.Error("finalizer should be released")
	}
}

// A Ready provider is still cleaned up (a failing delete retrying is TestHandleDeletion_DeleteErrorRetried).
func TestHandleDeletion_ReadyProviderStillCleansUp(t *testing.T) {
	var calls int32
	srv := deleteServer(http.StatusOK, &calls)
	defer srv.Close()
	m := deletionModel("a", "team-a", delTestProvider, delTestTag, true)
	r, err := runDeletion(t, srv, deletionProvider(srv.URL), m)
	if err != nil || calls != 1 || !finalizerGone(t, r, m) {
		t.Errorf("a Ready provider is asked once and the finalizer released: err=%v calls=%d", err, calls)
	}
}

// The reconciler path: a deleting Model whose provider exists but is not Ready, with
// an unreachable server, completes in one reconcile.
func TestReconcile_DeletingModelWithProviderNotReadyReleased(t *testing.T) {
	m := deletionModel("a", "team-a", delTestProvider, delTestTag, true)
	m.Status.Conditions = []metav1.Condition{{Type: conditionTypeReady, Status: metav1.ConditionFalse, Reason: "Pending",
		Message: "Waiting for ModelProvider " + delTestProvider + " to be ready"}}
	p := notReady(deletionProvider("http://127.0.0.1:1"))
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).WithObjects(m, p).Build()
	r := &ModelReconciler{Client: cl}
	if _, err := r.Reconcile(context.Background(), reconcileRequest(m)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !finalizerGone(t, r, m) {
		t.Error("a deleting Model whose provider is not Ready must release its finalizer")
	}
}
