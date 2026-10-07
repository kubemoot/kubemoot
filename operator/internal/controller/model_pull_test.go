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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// absentOllamaModel is a Model with no status yet and its Ollama provider at endpoint.
func absentOllamaModel(endpoint string) (*aiv1alpha1.Model, *aiv1alpha1.ModelProvider) {
	model := &aiv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "qwen-8b", Namespace: testCrewNamespace},
		Spec:       aiv1alpha1.ModelSpec{Model: testModelID, ProviderRef: testOllamaGPU},
	}
	provider := &aiv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: testOllamaGPU, Namespace: testKubemoot},
		Spec:       aiv1alpha1.ModelProviderSpec{Type: aiv1alpha1.ProviderTypeOllama, Endpoint: endpoint},
	}
	return model, provider
}

const pullTestWait = 5 * time.Second

// fakeOllama is an Ollama server whose /api/pull behavior each test supplies.
type fakeOllama struct {
	srv     *httptest.Server
	pulls   int32
	deletes int32
	present atomic.Bool
	pullFn  func(w http.ResponseWriter, r *http.Request)
}

func newFakeOllama(t *testing.T, pullFn func(w http.ResponseWriter, r *http.Request)) *fakeOllama {
	t.Helper()
	f := &fakeOllama{pullFn: pullFn}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case testPathAPITags:
			if f.present.Load() {
				_, _ = w.Write([]byte(`{"models":[{"name":"` + testModelID + `","size":2048,"digest":"sha256:feed"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"models":[]}`))
		case testPathAPIPull:
			atomic.AddInt32(&f.pulls, 1)
			f.pullFn(w, r)
		case "/api/delete":
			atomic.AddInt32(&f.deletes, 1)
			f.present.Store(false)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// emit writes one stream line and flushes it to the client.
func emit(w http.ResponseWriter, line string) {
	_, _ = w.Write([]byte(line + "\n"))
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
}

// blockedPull sends one progress line, then holds the stream open until release
// closes or the client goes away (recorded on cancelled).
func blockedPull(progress string, release <-chan struct{}, cancelled *atomic.Bool) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		emit(w, progress)
		select {
		case <-release:
		case <-r.Context().Done():
			cancelled.Store(true)
		}
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(pullTestWait)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

type pullHarness struct {
	t        *testing.T
	f        *fakeOllama
	r        *ModelReconciler
	model    *aiv1alpha1.Model
	provider *aiv1alpha1.ModelProvider
}

func newPullHarness(t *testing.T, f *fakeOllama, free int64) *pullHarness {
	t.Helper()
	model, provider := absentOllamaModel(f.srv.URL)
	model.Finalizers = []string{modelFinalizer}
	if free > 0 {
		provider.Status.Storage = &aiv1alpha1.ProviderStorage{FreeBytes: free}
	}
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).WithObjects(model, provider).WithStatusSubresource(model).Build()
	return &pullHarness{t: t, f: f, model: model, provider: provider,
		r: &ModelReconciler{Client: cl, HTTPClient: f.srv.Client()}}
}

func (h *pullHarness) reconcile() time.Duration {
	h.t.Helper()
	res, err := h.r.reconcileOllamaModel(context.Background(), h.model, h.provider)
	if err != nil {
		h.t.Fatalf("reconcileOllamaModel: %v", err)
	}
	return res.RequeueAfter
}

func (h *pullHarness) tracked() *pullState {
	return h.r.pulls.get(pullKey(h.model.Spec.ProviderRef, h.model.Spec.Model))
}

func (h *pullHarness) waitFinished() {
	h.t.Helper()
	eventually(h.t, "the pull to finish", func() bool {
		s := h.tracked()
		if s == nil {
			return false
		}
		return s.outcome().finished
	})
}

const halfDone = `{"status":"pulling abc","digest":"sha256:abc","total":1000,"completed":500}`

func TestPull_StartsAndReturnsAtOnce(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var cancelled atomic.Bool
	h := newPullHarness(t, newFakeOllama(t, blockedPull(halfDone, release, &cancelled)), 0)

	start := time.Now()
	requeue := h.reconcile()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("reconcile took %v while the pull is still running; it must return at once", elapsed)
	}
	if h.model.Status.State != statePulling || h.model.Status.Ready {
		t.Errorf("status = %q ready=%v, want Pulling not ready", h.model.Status.State, h.model.Status.Ready)
	}
	if requeue <= 0 {
		t.Error("a Pulling model must requeue to read progress")
	}
	eventually(t, "the pull request", func() bool { return atomic.LoadInt32(&h.f.pulls) == 1 })
}

func TestPull_ProgressRecordedWithoutDuplicatePull(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var cancelled atomic.Bool
	h := newPullHarness(t, newFakeOllama(t, blockedPull(halfDone, release, &cancelled)), 0)
	h.reconcile()
	eventually(t, "progress", func() bool {
		s := h.tracked()
		_, total := s.totals()
		return total == 1000
	})

	h.reconcile()
	p := h.model.Status.Pull
	if p == nil || p.CompletedBytes != 500 || p.TotalBytes != 1000 || p.Percent != 50 {
		t.Fatalf("Pull = %+v, want 500 of 1000 at 50%%", p)
	}
	if !strings.Contains(h.model.Status.Message, "50%") {
		t.Errorf("message = %q, want it to show the percent", h.model.Status.Message)
	}
	if n := atomic.LoadInt32(&h.f.pulls); n != 1 {
		t.Errorf("pulls = %d after a second reconcile, want 1", n)
	}
}

func TestPull_TwoModelsWithSameTagShareOnePull(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var cancelled atomic.Bool
	h := newPullHarness(t, newFakeOllama(t, blockedPull(halfDone, release, &cancelled)), 0)
	h.reconcile()
	second, _ := absentOllamaModel(h.f.srv.URL)
	second.Name = "qwen-8b-copy"
	if err := h.r.Create(context.Background(), second); err != nil {
		t.Fatalf("create second Model: %v", err)
	}
	h.model = second
	h.reconcile()
	if n := atomic.LoadInt32(&h.f.pulls); n != 1 {
		t.Errorf("pulls = %d for two Models with one tag, want 1", n)
	}
}

func TestPull_CompletionReportsAvailable(t *testing.T) {
	var f *fakeOllama
	f = newFakeOllama(t, func(w http.ResponseWriter, _ *http.Request) {
		emit(w, halfDone)
		f.present.Store(true)
		emit(w, `{"status":"success"}`)
	})
	h := newPullHarness(t, f, 0)
	h.reconcile()
	h.waitFinished()

	h.reconcile()
	if !h.model.Status.Ready || h.model.Status.State != stateAvailable {
		t.Fatalf("status = %q ready=%v, want Available ready", h.model.Status.State, h.model.Status.Ready)
	}
	if h.model.Status.Pull != nil {
		t.Errorf("Pull = %+v, want it cleared once the model is available", h.model.Status.Pull)
	}
	if h.tracked() != nil {
		t.Error("a finished pull must leave the tracker")
	}
	cond := meta.FindStatusCondition(h.model.Status.Conditions, conditionTypeReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("Ready condition = %+v, want True", cond)
	}
}

func TestPull_SuccessWithoutListingStaysPulling(t *testing.T) {
	h := newPullHarness(t, newFakeOllama(t, func(w http.ResponseWriter, _ *http.Request) {
		emit(w, `{"status":"success"}`)
	}), 0)
	h.reconcile()
	h.waitFinished()
	requeue := h.reconcile()
	if h.model.Status.State != statePulling || requeue < 10*time.Second {
		t.Errorf("state = %q requeue = %v, want Pulling polled on the Pulling interval", h.model.Status.State, requeue)
	}
}

func TestPull_ErrorLineFailsThenRetries(t *testing.T) {
	h := newPullHarness(t, newFakeOllama(t, func(w http.ResponseWriter, _ *http.Request) {
		emit(w, `{"error":"pull model manifest: file does not exist"}`)
	}), 0)
	h.reconcile()
	h.waitFinished()

	h.reconcile()
	if h.model.Status.State != stateError || !strings.Contains(h.model.Status.Message, "file does not exist") {
		t.Fatalf("status = %q %q, want Error naming the cause", h.model.Status.State, h.model.Status.Message)
	}
	h.reconcile()
	eventually(t, "the retry", func() bool { return atomic.LoadInt32(&h.f.pulls) == 2 })
}

func TestPull_HTTPFailureNamesStatus(t *testing.T) {
	h := newPullHarness(t, newFakeOllama(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}), 0)
	h.reconcile()
	h.waitFinished()
	h.reconcile()
	if h.model.Status.State != stateError || !strings.Contains(h.model.Status.Message, "status 502") {
		t.Errorf("status = %q %q, want Error with the HTTP status", h.model.Status.State, h.model.Status.Message)
	}
}

func TestPull_StreamEndingEarlyFails(t *testing.T) {
	h := newPullHarness(t, newFakeOllama(t, func(w http.ResponseWriter, _ *http.Request) {
		emit(w, halfDone)
	}), 0)
	h.reconcile()
	h.waitFinished()
	h.reconcile()
	if h.model.Status.State != stateError || !strings.Contains(h.model.Status.Message, "ended before") {
		t.Errorf("status = %q %q, want Error for an incomplete stream", h.model.Status.State, h.model.Status.Message)
	}
}

func TestPull_DoesNotFitFailsWithClearMessage(t *testing.T) {
	var cancelled atomic.Bool
	release := make(chan struct{})
	defer close(release)
	big := `{"status":"pulling big","digest":"sha256:big","total":5000,"completed":1000}`
	h := newPullHarness(t, newFakeOllama(t, blockedPull(big, release, &cancelled)), 2000)
	h.reconcile()
	h.waitFinished()

	h.reconcile()
	msg := h.model.Status.Message
	if h.model.Status.State != stateError || h.model.Status.Ready {
		t.Fatalf("status = %q ready=%v, want Error", h.model.Status.State, h.model.Status.Ready)
	}
	for _, want := range []string{"not enough free disk", testModelID, "2.0 KB more"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "500") {
		t.Errorf("message %q should not be a raw HTTP 500", msg)
	}
	eventually(t, "the pull stream to be cancelled", cancelled.Load)
}

func TestPull_ServerOutOfSpaceBecomesClearMessage(t *testing.T) {
	h := newPullHarness(t, newFakeOllama(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"write /root/.ollama/models/blobs/x: no space left on device"}`))
	}), 4096)
	h.reconcile()
	h.waitFinished()
	h.reconcile()
	msg := h.model.Status.Message
	if !strings.Contains(msg, "not enough free disk") || !strings.Contains(msg, "4.0 KB free") {
		t.Errorf("message = %q, want the disk message with the free space", msg)
	}
}

func TestPull_NoSpaceRetriesOnlyWhenFreeDiskChanges(t *testing.T) {
	h := newPullHarness(t, newFakeOllama(t, func(w http.ResponseWriter, _ *http.Request) {
		emit(w, `{"error":"no space left on device"}`)
	}), 100)
	h.reconcile()
	h.waitFinished()
	h.reconcile()
	h.reconcile()
	if n := atomic.LoadInt32(&h.f.pulls); n != 1 {
		t.Fatalf("pulls = %d while the free disk is unchanged, want 1", n)
	}
	h.provider.Status.Storage.FreeBytes = 5000
	h.reconcile()
	eventually(t, "the retry after the disk grew", func() bool { return atomic.LoadInt32(&h.f.pulls) == 2 })
}

func TestPull_UnknownFreeDiskDoesNotBlockPull(t *testing.T) {
	var cancelled atomic.Bool
	release := make(chan struct{})
	defer close(release)
	huge := `{"status":"pulling big","digest":"sha256:big","total":99999999999,"completed":0}`
	h := newPullHarness(t, newFakeOllama(t, blockedPull(huge, release, &cancelled)), 0)
	h.reconcile()
	eventually(t, "progress", func() bool { _, total := h.tracked().totals(); return total > 0 })
	if h.tracked().outcome().finished {
		t.Error("with no known free disk the pull must not be refused")
	}
}

func TestPull_RestartResumesPullingModel(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var cancelled atomic.Bool
	h := newPullHarness(t, newFakeOllama(t, blockedPull(halfDone, release, &cancelled)), 0)
	h.model.Status.State = statePulling
	h.model.Status.Pull = &aiv1alpha1.PullProgress{CompletedBytes: 500, TotalBytes: 1000, Percent: 50}
	h.r.pulls = pullTracker{}

	h.reconcile()
	eventually(t, "the resumed pull", func() bool { return atomic.LoadInt32(&h.f.pulls) == 1 })
	if h.model.Status.State != statePulling {
		t.Errorf("state = %q, want Pulling", h.model.Status.State)
	}
}

func TestPull_DeletionCancelsRunningPullAndReleases(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var cancelled atomic.Bool
	h := newPullHarness(t, newFakeOllama(t, blockedPull(halfDone, release, &cancelled)), 0)
	h.reconcile()
	eventually(t, "the pull request", func() bool { return atomic.LoadInt32(&h.f.pulls) == 1 })

	now := metav1.Now()
	h.model.DeletionTimestamp = &now
	if _, err := h.r.handleDeletion(context.Background(), h.model, h.provider); err != nil {
		t.Fatalf("handleDeletion: %v", err)
	}
	eventually(t, "the in-flight pull to be cancelled", cancelled.Load)
	if h.tracked() != nil {
		t.Error("the cancelled pull must leave the tracker")
	}
	if n := atomic.LoadInt32(&h.f.deletes); n != 1 {
		t.Errorf("deletes = %d, want the partial model removed once", n)
	}
	got := &aiv1alpha1.Model{}
	err := h.r.Get(context.Background(), client.ObjectKeyFromObject(h.model), got)
	if err == nil && len(got.Finalizers) != 0 {
		t.Error("the finalizer must be released")
	}
}

func TestPull_DeletionKeepsPullSharedWithAnotherModel(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var cancelled atomic.Bool
	h := newPullHarness(t, newFakeOllama(t, blockedPull(halfDone, release, &cancelled)), 0)
	other, _ := absentOllamaModel(h.f.srv.URL)
	other.Name = "second-model"
	if err := h.r.Create(context.Background(), other); err != nil {
		t.Fatalf("create other Model: %v", err)
	}
	h.reconcile()
	eventually(t, "the pull request", func() bool { return atomic.LoadInt32(&h.f.pulls) == 1 })

	now := metav1.Now()
	h.model.DeletionTimestamp = &now
	if _, err := h.r.handleDeletion(context.Background(), h.model, h.provider); err != nil {
		t.Fatalf("handleDeletion: %v", err)
	}
	if h.tracked() == nil || cancelled.Load() {
		t.Error("a pull another Model still needs must keep running")
	}
	if n := atomic.LoadInt32(&h.f.deletes); n != 0 {
		t.Errorf("deletes = %d, want none while another Model uses the tag", n)
	}
}

func TestPull_ReconcileOfDeletingModelIsNotBlockedByPull(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var cancelled atomic.Bool
	h := newPullHarness(t, newFakeOllama(t, blockedPull(halfDone, release, &cancelled)), 0)
	h.reconcile()
	if err := h.r.Delete(context.Background(), h.model); err != nil {
		t.Fatalf("delete Model: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := h.r.Reconcile(context.Background(), reconcileRequest(h.model))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	case <-time.After(pullTestWait):
		t.Fatal("deleting a Model blocked behind a running pull")
	}
}

func TestPullProgress_Percent(t *testing.T) {
	cases := []struct {
		completed, total int64
		want             int32
	}{{0, 0, 0}, {0, 100, 0}, {50, 100, 50}, {100, 100, 100}, {1, 3, 33}}
	for _, c := range cases {
		if got := pullProgress(c.completed, c.total).Percent; got != c.want {
			t.Errorf("pullProgress(%d, %d).Percent = %d, want %d", c.completed, c.total, got, c.want)
		}
	}
}

func TestPull_ConcurrentStartsShareOnePull(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var cancelled atomic.Bool
	f := newFakeOllama(t, blockedPull(halfDone, release, &cancelled))
	var tracker pullTracker
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			startPull(&tracker, "p/m", "uid", pullStream{endpoint: f.srv.URL, tag: testModelID, client: f.srv.Client()})
		}()
	}
	wg.Wait()
	eventually(t, "the pull request", func() bool { return atomic.LoadInt32(&f.pulls) >= 1 })
	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt32(&f.pulls); n != 1 {
		t.Errorf("pulls = %d from 16 concurrent starts, want 1", n)
	}
}

func TestPull_ChangingTheTagCancelsTheOldPull(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var cancelled atomic.Bool
	h := newPullHarness(t, newFakeOllama(t, blockedPull(halfDone, release, &cancelled)), 0)
	h.model.UID = "uid-1"
	h.reconcile()
	eventually(t, "the pull request", func() bool { return atomic.LoadInt32(&h.f.pulls) == 1 })

	h.model.Spec.Model = "other:1b"
	stored := &aiv1alpha1.Model{}
	if err := h.r.Get(context.Background(), client.ObjectKeyFromObject(h.model), stored); err != nil {
		t.Fatalf("get stored Model: %v", err)
	}
	stored.Spec.Model = "other:1b"
	if err := h.r.Update(context.Background(), stored); err != nil {
		t.Fatalf("update stored Model: %v", err)
	}
	h.model.ResourceVersion = stored.ResourceVersion
	h.model.UID = "uid-1" // the fake client resets the UID on a status write
	h.reconcile()
	eventually(t, "the old pull to be cancelled", cancelled.Load)
	if h.r.pulls.get(pullKey(h.model.Spec.ProviderRef, testModelID)) != nil {
		t.Error("the pull for the old tag must leave the tracker")
	}
}
