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
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

type embeddingPullHarness struct {
	t        *testing.T
	f        *fakeOllama
	r        *EmbeddingModelReconciler
	em       *aiv1alpha1.EmbeddingModel
	provider *aiv1alpha1.ModelProvider
}

func newEmbeddingPullHarness(t *testing.T, f *fakeOllama, free int64) *embeddingPullHarness {
	t.Helper()
	_, provider := absentOllamaModel(f.srv.URL)
	provider.Status.Ready = true
	if free > 0 {
		provider.Status.Storage = &aiv1alpha1.ProviderStorage{FreeBytes: free}
	}
	em := &aiv1alpha1.EmbeddingModel{
		ObjectMeta: metav1.ObjectMeta{Name: "nomic", Namespace: testCrewNamespace, UID: "emb-uid", Finalizers: []string{embeddingModelFinalizer}},
		Spec:       aiv1alpha1.EmbeddingModelSpec{Model: testModelID, ProviderRef: testOllamaGPU},
	}
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).WithObjects(em, provider).WithStatusSubresource(em, &aiv1alpha1.Model{}).Build()
	return &embeddingPullHarness{t: t, f: f, em: em, provider: provider,
		r: &EmbeddingModelReconciler{Client: cl, HTTPClient: f.srv.Client()}}
}

func (h *embeddingPullHarness) reconcile() {
	h.t.Helper()
	if _, err := h.r.reconcileOllamaEmbedding(context.Background(), h.em, h.provider); err != nil {
		h.t.Fatalf("reconcileOllamaEmbedding: %v", err)
	}
}

func (h *embeddingPullHarness) tracked() *pullState {
	return h.r.tracker().get(pullKey(h.em.Spec.ProviderRef, h.em.Spec.Model))
}

func TestEmbeddingPull_StartsInBackgroundAndReportsProgress(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var cancelled atomic.Bool
	h := newEmbeddingPullHarness(t, newFakeOllama(t, blockedPull(halfDone, release, &cancelled)), 0)

	start := time.Now()
	h.reconcile()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("reconcile took %v while the pull runs; it must return at once", elapsed)
	}
	if h.em.Status.State != statePulling || h.em.Status.Ready {
		t.Fatalf("status = %q ready=%v, want Pulling not ready", h.em.Status.State, h.em.Status.Ready)
	}
	eventually(t, "progress", func() bool { _, total := h.tracked().totals(); return total == 1000 })

	h.reconcile()
	p := h.em.Status.Pull
	if p == nil || p.CompletedBytes != 500 || p.TotalBytes != 1000 || p.Percent != 50 {
		t.Fatalf("Pull = %+v, want 500 of 1000 at 50%%", p)
	}
	if !strings.Contains(h.em.Status.Message, "50%") {
		t.Errorf("message = %q, want it to show the percent", h.em.Status.Message)
	}
	if n := atomic.LoadInt32(&h.f.pulls); n != 1 {
		t.Errorf("pulls = %d after two reconciles, want 1", n)
	}
}

func TestEmbeddingPull_CompletionReportsAvailableAndClearsPull(t *testing.T) {
	var f *fakeOllama
	f = newFakeOllama(t, func(w http.ResponseWriter, _ *http.Request) {
		emit(w, halfDone)
		f.present.Store(true)
		emit(w, `{"status":"success"}`)
	})
	h := newEmbeddingPullHarness(t, f, 0)
	h.reconcile()
	eventually(t, "the pull to finish", func() bool { s := h.tracked(); return s != nil && s.outcome().finished })

	h.reconcile() // the pull outcome is consumed
	h.reconcile() // the provider lists the model
	if !h.em.Status.Ready || h.em.Status.State != stateAvailable {
		t.Fatalf("status = %q ready=%v, want Available ready", h.em.Status.State, h.em.Status.Ready)
	}
	if h.em.Status.Pull != nil {
		t.Errorf("Pull = %+v, want it cleared once the model is available", h.em.Status.Pull)
	}
	if h.tracked() != nil {
		t.Error("a finished pull must leave the tracker")
	}
}

func TestEmbeddingPull_FailureReportsError(t *testing.T) {
	h := newEmbeddingPullHarness(t, newFakeOllama(t, func(w http.ResponseWriter, _ *http.Request) {
		emit(w, `{"error":"pull model manifest: file does not exist"}`)
	}), 0)
	h.reconcile()
	eventually(t, "the pull to fail", func() bool { s := h.tracked(); return s != nil && s.outcome().finished })

	h.reconcile()
	if h.em.Status.State != stateError || !strings.Contains(h.em.Status.Message, "file does not exist") {
		t.Errorf("status = %q %q, want Error naming the registry failure", h.em.Status.State, h.em.Status.Message)
	}
	if h.em.Status.Pull != nil {
		t.Errorf("Pull = %+v, want it cleared on error", h.em.Status.Pull)
	}
}

func TestEmbeddingPull_RefusesWhatDoesNotFit(t *testing.T) {
	huge := `{"status":"pulling big","digest":"sha256:big","total":5000,"completed":0}`
	release := make(chan struct{})
	defer close(release)
	var cancelled atomic.Bool
	h := newEmbeddingPullHarness(t, newFakeOllama(t, blockedPull(huge, release, &cancelled)), 1000)
	h.reconcile()
	eventually(t, "the pull to fail", func() bool { s := h.tracked(); return s != nil && s.outcome().finished })

	h.reconcile()
	if h.em.Status.State != stateError || !strings.Contains(h.em.Status.Message, "not enough free disk") {
		t.Errorf("status = %q %q, want Error with not enough free disk", h.em.Status.State, h.em.Status.Message)
	}
}

func TestEmbeddingPull_ModelAlreadyPresentDoesNotPull(t *testing.T) {
	f := newFakeOllama(t, func(http.ResponseWriter, *http.Request) {})
	f.present.Store(true)
	h := newEmbeddingPullHarness(t, f, 0)
	h.reconcile()
	if n := atomic.LoadInt32(&f.pulls); n != 0 {
		t.Errorf("pulls = %d for a model the provider lists, want 0", n)
	}
	if !h.em.Status.Ready {
		t.Errorf("status = %q, want Available", h.em.Status.State)
	}
}

func TestEmbeddingPull_SharesOnePullWithAModelOfTheSameTag(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var cancelled atomic.Bool
	f := newFakeOllama(t, blockedPull(halfDone, release, &cancelled))
	h := newEmbeddingPullHarness(t, f, 0)
	model, _ := absentOllamaModel(f.srv.URL)
	model.Finalizers = []string{modelFinalizer}
	if err := h.r.Create(context.Background(), model); err != nil {
		t.Fatalf("create Model: %v", err)
	}
	mr := &ModelReconciler{Client: h.r.Client, HTTPClient: f.srv.Client(), Pulls: h.r.tracker()}

	h.reconcile()
	eventually(t, "the pull request", func() bool { return atomic.LoadInt32(&f.pulls) == 1 })
	if _, err := mr.reconcileOllamaModel(context.Background(), model, h.provider); err != nil {
		t.Fatalf("reconcile Model: %v", err)
	}
	if n := atomic.LoadInt32(&f.pulls); n != 1 {
		t.Errorf("pulls = %d for a Model and an EmbeddingModel with one tag, want 1", n)
	}
}

func TestEmbeddingPull_DeletionCancelsAnUnwantedPull(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var cancelled atomic.Bool
	h := newEmbeddingPullHarness(t, newFakeOllama(t, blockedPull(halfDone, release, &cancelled)), 0)
	h.reconcile()
	eventually(t, "progress", func() bool { completed, _ := h.tracked().totals(); return completed == 500 })

	now := metav1.Now()
	h.em.DeletionTimestamp = &now
	if _, err := h.r.handleDeletion(context.Background(), h.em); err != nil {
		t.Fatalf("handleDeletion: %v", err)
	}
	eventually(t, "the in-flight pull to be cancelled", cancelled.Load)
	if h.tracked() != nil {
		t.Error("the cancelled pull must leave the tracker")
	}
	if got := h.r.tracker().PartialBytes(h.provider.Name); got != 500 {
		t.Errorf("PartialBytes = %d, want the 500 bytes the cancelled pull left", got)
	}
	got := &aiv1alpha1.EmbeddingModel{}
	if err := h.r.Get(context.Background(), client.ObjectKeyFromObject(h.em), got); err == nil && len(got.Finalizers) != 0 {
		t.Error("the finalizer must be released")
	}
}

func TestEmbeddingPull_DeletionKeepsAPullAModelStillWants(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var cancelled atomic.Bool
	f := newFakeOllama(t, blockedPull(halfDone, release, &cancelled))
	h := newEmbeddingPullHarness(t, f, 0)
	model, _ := absentOllamaModel(f.srv.URL)
	if err := h.r.Create(context.Background(), model); err != nil {
		t.Fatalf("create Model: %v", err)
	}
	h.reconcile()
	eventually(t, "the pull request", func() bool { return atomic.LoadInt32(&f.pulls) == 1 })

	now := metav1.Now()
	h.em.DeletionTimestamp = &now
	if _, err := h.r.handleDeletion(context.Background(), h.em); err != nil {
		t.Fatalf("handleDeletion: %v", err)
	}
	if h.tracked() == nil || cancelled.Load() {
		t.Error("a pull a Model still wants must keep running")
	}
}

func TestEmbeddingPull_OldStatusJSONStillReads(t *testing.T) {
	old := `{"state":"Available","ready":true,"endpoint":"http://x","message":"ok"}`
	var status aiv1alpha1.EmbeddingModelStatus
	if err := json.Unmarshal([]byte(old), &status); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if status.State != stateAvailable || !status.Ready || status.Pull != nil {
		t.Errorf("status = %+v, want the old fields read and no pull", status)
	}
}

func TestEmbeddingPull_BaseNameMatchDoesNotCancelAPullOfAnotherTag(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var cancelled atomic.Bool
	f := newFakeOllama(t, blockedPull(halfDone, release, &cancelled))
	f.present.Store(true) // the provider lists testModelID
	h := newEmbeddingPullHarness(t, f, 0)
	base := strings.Split(testModelID, ":")[0]
	h.em.Spec.Model = base + ":other-tag"
	key := pullKey(h.em.Spec.ProviderRef, h.em.Spec.Model)
	startPull(h.r.tracker(), key, h.em.UID, newPullStream(h.provider, h.em.Spec.Model, f.srv.Client()))
	eventually(t, "the pull request", func() bool { return atomic.LoadInt32(&f.pulls) == 1 })

	h.reconcile() // lists only testModelID: a base-name match, not the exact tag
	if h.r.tracker().get(key) == nil || cancelled.Load() {
		t.Error("a base-name match must not cancel the pull of the exact tag")
	}
}

func TestEmbeddingPull_ChangingTheTagCancelsTheOldPull(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var cancelled atomic.Bool
	h := newEmbeddingPullHarness(t, newFakeOllama(t, blockedPull(halfDone, release, &cancelled)), 0)
	h.reconcile()
	eventually(t, "the pull request", func() bool { return atomic.LoadInt32(&h.f.pulls) == 1 })

	h.em.Spec.Model = otherPullTag
	stored := &aiv1alpha1.EmbeddingModel{}
	if err := h.r.Get(context.Background(), client.ObjectKeyFromObject(h.em), stored); err != nil {
		t.Fatalf("get stored EmbeddingModel: %v", err)
	}
	stored.Spec.Model = otherPullTag
	if err := h.r.Update(context.Background(), stored); err != nil {
		t.Fatalf("update stored EmbeddingModel: %v", err)
	}
	h.em.ResourceVersion = stored.ResourceVersion
	h.em.UID = "emb-uid" // the fake client resets the UID on a status write
	h.reconcile()
	eventually(t, "the old pull to be cancelled", cancelled.Load)
	if h.r.tracker().get(pullKey(h.em.Spec.ProviderRef, testModelID)) != nil {
		t.Error("the pull for the old tag must leave the tracker")
	}
}
