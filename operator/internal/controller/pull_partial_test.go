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
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	partialProvider = "gpu"
	partialKey      = "gpu/m"
)

// startBlockedPull starts a tracked pull that has downloaded 500 of 1000 bytes and
// then holds the stream open until the returned release func runs.
func startBlockedPull(t *testing.T, tracker *PullTracker) (cancelled *atomic.Bool, release func()) {
	t.Helper()
	gate := make(chan struct{})
	cancelled = &atomic.Bool{}
	f := newFakeOllama(t, blockedPull(halfDone, gate, cancelled))
	startPull(tracker, partialKey, "uid", pullStream{provider: partialProvider, endpoint: f.srv.URL, tag: testModelID, client: f.srv.Client()})
	eventually(t, "progress", func() bool { completed, _ := tracker.get(partialKey).totals(); return completed == 500 })
	return cancelled, func() { close(gate) }
}

func TestPartialBytes_RunningPullCounts(t *testing.T) {
	var tracker PullTracker
	_, release := startBlockedPull(t, &tracker)
	defer release()
	if got := tracker.PartialBytes(partialProvider); got != 500 {
		t.Errorf("PartialBytes = %d for a pull with 500 bytes down, want 500", got)
	}
	if got := tracker.PartialBytes(testMismatchName); got != 0 {
		t.Errorf("PartialBytes(other provider) = %d, want 0", got)
	}
}

func TestPartialBytes_AbandonedPullLeavesItsBytes(t *testing.T) {
	var tracker PullTracker
	cancelled, release := startBlockedPull(t, &tracker)
	defer release()
	tracker.abandon(partialKey)
	eventually(t, "the pull to be cancelled", cancelled.Load)
	if tracker.get(partialKey) != nil {
		t.Error("an abandoned pull must leave the tracker")
	}
	if got := tracker.PartialBytes(partialProvider); got != 500 {
		t.Errorf("PartialBytes = %d after a cancel, want the 500 bytes left on disk", got)
	}
}

func TestPartialBytes_ForgetAndRestartClearThem(t *testing.T) {
	var tracker PullTracker
	_, release := startBlockedPull(t, &tracker)
	defer release()
	tracker.abandon(partialKey)
	tracker.forget(partialKey)
	if got := tracker.PartialBytes(partialProvider); got != 0 {
		t.Errorf("PartialBytes = %d after forget, want 0", got)
	}

	tracker.leftovers = map[string]leftover{partialKey: {provider: partialProvider, bytes: 700}}
	tracker.ServerRestarted(partialProvider, "pod-a/0")
	if got := tracker.PartialBytes(partialProvider); got != 700 {
		t.Errorf("PartialBytes = %d on first sight of the server, want 700 kept", got)
	}
	tracker.ServerRestarted(partialProvider, "pod-a/0")
	if got := tracker.PartialBytes(partialProvider); got != 700 {
		t.Errorf("PartialBytes = %d for the same server instance, want 700 kept", got)
	}
	tracker.ServerRestarted(partialProvider, "pod-a/1")
	if got := tracker.PartialBytes(partialProvider); got != 0 {
		t.Errorf("PartialBytes = %d after the server restarted, want 0 (Ollama prunes partials on start)", got)
	}
}

func TestPartialBytes_ResumedPullTakesOverTheLeftover(t *testing.T) {
	var tracker PullTracker
	cancelled, release := startBlockedPull(t, &tracker)
	defer release()
	tracker.abandon(partialKey)
	eventually(t, "the pull to be cancelled", cancelled.Load)

	_, release2 := startBlockedPull(t, &tracker)
	defer release2()
	if got := tracker.PartialBytes(partialProvider); got != 500 {
		t.Errorf("PartialBytes = %d with the leftover and the resumed pull, want 500 counted once", got)
	}
}

func TestPartialBytes_FinishedPullLeavesNothing(t *testing.T) {
	var tracker PullTracker
	f := newFakeOllama(t, func(w http.ResponseWriter, _ *http.Request) {
		emit(w, halfDone)
		emit(w, `{"status":"success"}`)
	})
	startPull(&tracker, partialKey, "uid", pullStream{provider: partialProvider, endpoint: f.srv.URL, tag: testModelID, client: f.srv.Client()})
	eventually(t, "the pull to finish", func() bool { return tracker.get(partialKey).outcome().finished })
	tracker.abandon(partialKey)
	if got := tracker.PartialBytes(partialProvider); got != 0 {
		t.Errorf("PartialBytes = %d for a pull that succeeded, want 0", got)
	}
}

func TestPartialBytes_ModelDeletedMidPullCountsTowardStorage(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var cancelled atomic.Bool
	h := newPullHarness(t, newFakeOllama(t, blockedPull(halfDone, release, &cancelled)), 0)
	h.reconcile()
	eventually(t, "progress", func() bool { completed, _ := h.tracked().totals(); return completed == 500 })

	now := metav1.Now()
	h.model.DeletionTimestamp = &now
	if _, err := h.r.handleDeletion(context.Background(), h.model, h.provider); err != nil {
		t.Fatalf("handleDeletion: %v", err)
	}
	eventually(t, "the pull to be cancelled", cancelled.Load)
	if got := h.r.tracker().PartialBytes(h.provider.Name); got != 500 {
		t.Errorf("PartialBytes = %d after the Model was deleted mid-pull, want the 500 bytes left behind", got)
	}
}

func TestPartialBytes_SharedTagKeepsNoLeftoverWhileWanted(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var cancelled atomic.Bool
	h := newPullHarness(t, newFakeOllama(t, blockedPull(halfDone, release, &cancelled)), 0)
	em := &aiv1alpha1.EmbeddingModel{
		ObjectMeta: metav1.ObjectMeta{Name: "emb", Namespace: testCrewNamespace},
		Spec:       aiv1alpha1.EmbeddingModelSpec{Model: testModelID, ProviderRef: testOllamaGPU},
	}
	if err := h.r.Create(context.Background(), em); err != nil {
		t.Fatalf("create EmbeddingModel: %v", err)
	}
	h.reconcile()
	eventually(t, "the pull request", func() bool { return atomic.LoadInt32(&h.f.pulls) == 1 })

	now := metav1.Now()
	h.model.DeletionTimestamp = &now
	if _, err := h.r.handleDeletion(context.Background(), h.model, h.provider); err != nil {
		t.Fatalf("handleDeletion: %v", err)
	}
	if h.tracked() == nil || cancelled.Load() {
		t.Error("a pull an EmbeddingModel still wants must keep running")
	}
	if n := atomic.LoadInt32(&h.f.deletes); n != 0 {
		t.Errorf("deletes = %d, want none while an EmbeddingModel uses the tag", n)
	}
}

func TestDiscoverStorage_PartialDownloadsReduceFreeDisk(t *testing.T) {
	srv := tagsServer(`{"models":[{"name":"a:1","size":1073741824}]}`)
	defer srv.Close()
	pod := ollamaPod([]corev1.Volume{claimVolume()}, []corev1.VolumeMount{{Name: storageData, MountPath: storageMount}})
	pod.UID = "pod-1"
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).WithObjects(pod, claim("10Gi"), endpointSlice()).WithStatusSubresource(pod).Build()
	tracker := &PullTracker{leftovers: map[string]leftover{partialKey: {provider: "gpu", bytes: 2 * gib}}}
	r := &ModelProviderReconciler{Client: cl, Pulls: tracker}
	provider := storageProvider(srv)
	provider.Spec.Endpoint = "http://ollama." + storageNS + ":11434"
	httpClient := &http.Client{Transport: redirectTo(srv)}

	r.discoverStorage(context.Background(), provider, httpClient)
	got := provider.Status.Storage
	if got == nil || got.PartialBytes != 2*gib || got.ModelBytes != gib || got.FreeBytes != 7*gib {
		t.Fatalf("storage = %+v, want 2Gi partial, 1Gi models, 7Gi free", got)
	}

	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: candProvider, RestartCount: 1}}
	if err := cl.Status().Update(context.Background(), pod); err != nil {
		t.Fatalf("update pod: %v", err)
	}
	r.discoverStorage(context.Background(), provider, httpClient)
	got = provider.Status.Storage
	if got.PartialBytes != 0 || got.FreeBytes != 9*gib {
		t.Errorf("storage = %+v after an Ollama restart, want the partial files gone and 9Gi free", got)
	}
}

func TestPartialBytes_SucceededPullIsNotCountedTwice(t *testing.T) {
	var tracker PullTracker
	f := newFakeOllama(t, func(w http.ResponseWriter, _ *http.Request) {
		emit(w, halfDone)
		emit(w, `{"status":"success"}`)
	})
	startPull(&tracker, partialKey, "uid", pullStream{provider: partialProvider, endpoint: f.srv.URL, tag: testModelID, client: f.srv.Client()})
	eventually(t, "the pull to finish", func() bool { return tracker.get(partialKey).outcome().finished })
	if got := tracker.PartialBytes(partialProvider); got != 0 {
		t.Errorf("PartialBytes = %d for a finished pull the provider already lists, want 0", got)
	}
}

func TestPullOwners_SurvivorCanReleaseASharedPull(t *testing.T) {
	var tracker PullTracker
	cancelled, release := startBlockedPull(t, &tracker)
	defer release()
	tracker.get(partialKey).addOwner("second")
	if len(tracker.ownedBy("second")) != 1 || len(tracker.ownedBy("uid")) != 1 {
		t.Fatal("both owners must see the shared pull")
	}
	tracker.dropUnwanted("second", "gpu/other", map[string]bool{})
	eventually(t, "the pull to be cancelled", cancelled.Load)
	if tracker.get(partialKey) != nil {
		t.Error("the pull must leave the tracker once an owner stops wanting the key")
	}
}

func TestServerInstance_ChangesWithPodAndRestarts(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "a"}}
	base := serverInstance(pod)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{RestartCount: 2}}
	restarted := serverInstance(pod)
	other := serverInstance(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "b"}})
	if base == restarted || base == other || restarted == other {
		t.Errorf("instances %q %q %q must all differ", base, restarted, other)
	}
}

func TestIsSameObject_KindNamespaceAndName(t *testing.T) {
	model := &aiv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"}}
	cases := []struct {
		name string
		a, b client.Object
		want bool
	}{
		{"same model", model, model.DeepCopy(), true},
		{"other name", model, &aiv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "ns"}}, false},
		{"other namespace", model, &aiv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: testMismatchName}}, false},
		{"other kind", model, &aiv1alpha1.EmbeddingModel{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"}}, false},
	}
	for _, c := range cases {
		if got := isSameObject(c.a, c.b); got != c.want {
			t.Errorf("%s: isSameObject = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTrackerOrNew_CreatesOnceAndKeepsASharedOne(t *testing.T) {
	var slot *PullTracker
	first := trackerOrNew(&slot)
	if first == nil || trackerOrNew(&slot) != first {
		t.Error("an empty slot must get one tracker that later calls return")
	}
	shared := &PullTracker{}
	slot = shared
	if trackerOrNew(&slot) != shared {
		t.Error("a tracker that was set must be kept")
	}
}
