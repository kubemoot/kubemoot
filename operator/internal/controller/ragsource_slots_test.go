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
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	slotServer  = "cpu-vllm"
	slotRAGName = idxCollection
	slotNewName = "docs_new"
)

// slotKey is the label value the test server's Jobs carry.
const slotKey = rbacTestNS + "." + slotServer

func slotEmbeddingModel(server string, batch int32) *kubemootv1alpha1.EmbeddingModel {
	return &kubemootv1alpha1.EmbeddingModel{
		ObjectMeta: metav1.ObjectMeta{Name: FallbackEmbeddingModel, Namespace: rbacTestNS},
		Spec:       kubemootv1alpha1.EmbeddingModelSpec{Model: embedTestModel, BatchSize: batch},
		Status:     kubemootv1alpha1.EmbeddingModelStatus{BoundServer: server},
	}
}

func slotRAGSource() *kubemootv1alpha1.RAGSource {
	return &kubemootv1alpha1.RAGSource{
		ObjectMeta: metav1.ObjectMeta{Name: slotRAGName, Namespace: rbacTestNS},
		Spec: kubemootv1alpha1.RAGSourceSpec{Source: kubemootv1alpha1.SourceConfig{
			Type: kubemootv1alpha1.RAGSourceTypeGit, Git: &kubemootv1alpha1.GitSource{URL: "https://example.com/r"},
		}},
	}
}

func slotJob(name string, done bool) *batchv1.Job {
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: rbacTestNS,
		Labels: map[string]string{labelComponent: componentIndexer, labelEmbeddingServer: slotKey},
	}}
	if done {
		job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	}
	return job
}

func slotReconciler(t *testing.T, limit int32, objs ...client.Object) (*RAGSourceReconciler, client.Client) {
	t.Helper()
	scheme := rbacTestClient(t).Scheme()
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&kubemootv1alpha1.RAGSource{}).Build()
	cache := NewConfigCache()
	cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{
		Defaults: kubemootv1alpha1.DefaultConfig{MaxIndexBuildsPerServer: limit},
	}})
	return &RAGSourceReconciler{Client: c, Scheme: c.Scheme(), ConfigCache: cache}, c
}

func indexerJobCount(t *testing.T, c client.Client) int {
	t.Helper()
	jobs := &batchv1.JobList{}
	if err := c.List(context.Background(), jobs, client.MatchingLabels{labelRAGSource: slotRAGName}); err != nil {
		t.Fatalf("list: %v", err)
	}
	return len(jobs.Items)
}

func TestBuildQueuesWhenTheServerIsAtItsLimit(t *testing.T) {
	rag, em := slotRAGSource(), slotEmbeddingModel(slotServer, 0)
	r, c := slotReconciler(t, 2, rag, em, slotJob("a", false), slotJob("b", false))

	if _, err := r.runIndexingJob(context.Background(), rag, em); err != nil {
		t.Fatalf("run: %v", err)
	}
	if rag.Status.Phase != phaseQueued {
		t.Fatalf("phase = %q, want Queued", rag.Status.Phase)
	}
	if !strings.Contains(rag.Status.Message, "2 of 2") {
		t.Errorf("message should say how full the server is: %q", rag.Status.Message)
	}
	if n := indexerJobCount(t, c); n != 0 {
		t.Errorf("a queued build must not create a job, got %d", n)
	}
	if !r.needsIndexing(rag) {
		t.Error("a Queued RAGSource must be picked up again")
	}
}

func TestQueuedBuildStartsWhenASlotFrees(t *testing.T) {
	rag, em := slotRAGSource(), slotEmbeddingModel(slotServer, 0)
	rag.Status.Phase = phaseQueued
	// one of the two jobs has finished: the state says a slot is free
	r, c := slotReconciler(t, 2, rag, em, slotJob("a", false), slotJob("b", true))

	if _, err := r.runIndexingJob(context.Background(), rag, em); err != nil {
		t.Fatalf("run: %v", err)
	}
	if rag.Status.Phase != phaseIndexing {
		t.Fatalf("phase = %q, want Indexing", rag.Status.Phase)
	}
	if n := indexerJobCount(t, c); n != 1 {
		t.Errorf("want one job started, got %d", n)
	}
}

func TestOtherServersAndUnboundModelsAreNotLimited(t *testing.T) {
	jobs := []client.Object{slotJob("a", false), slotJob("b", false)}

	for name, em := range map[string]*kubemootv1alpha1.EmbeddingModel{
		"another server": slotEmbeddingModel("gpu-vllm", 0),
		"no server":      slotEmbeddingModel("", 0),
	} {
		rag := slotRAGSource()
		r, _ := slotReconciler(t, 2, append(jobs, rag, em)...)
		if queued, err := r.queueForSlot(context.Background(), rag, em); queued || err != nil {
			t.Errorf("%s: queued=%v err=%v, want neither", name, queued, err)
		}
	}
}

func TestBuildsOfTheLimitAreCountedByLiveJobsOnly(t *testing.T) {
	failed := slotJob("f", false)
	failed.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
	rag, em := slotRAGSource(), slotEmbeddingModel(slotServer, 0)
	r, _ := slotReconciler(t, 1, rag, em, failed, slotJob("done", true))

	if queued, err := r.queueForSlot(context.Background(), rag, em); queued || err != nil {
		t.Errorf("finished and failed jobs hold no slot: queued=%v err=%v", queued, err)
	}
}

func TestQueuedRebuildKeepsServingTheCurrentIndex(t *testing.T) {
	rag, em := slotRAGSource(), slotEmbeddingModel(slotServer, 0)
	rag.Status.Index = &kubemootv1alpha1.IndexProvenance{Collection: slotRAGName}
	rag.Status.Building = &kubemootv1alpha1.IndexProvenance{Collection: slotNewName}
	r, _ := slotReconciler(t, 1, rag, em, slotJob("a", false))

	if queued, err := r.queueForSlot(context.Background(), rag, em); !queued || err != nil {
		t.Fatalf("queued=%v err=%v, want queued", queued, err)
	}
	if !rag.Status.Ready {
		t.Error("readers stay on the serving index, so the RAGSource stays Ready")
	}
}

func TestQueuedServingSourceStaysReady(t *testing.T) {
	rag, em := slotRAGSource(), slotEmbeddingModel(slotServer, 0)
	rag.Status.Ready = true
	rag.Status.Index = &kubemootv1alpha1.IndexProvenance{Collection: slotRAGName}
	r, _ := slotReconciler(t, 1, rag, em, slotJob("a", false))

	if queued, err := r.queueForSlot(context.Background(), rag, em); !queued || err != nil {
		t.Fatalf("queued=%v err=%v, want queued", queued, err)
	}
	if !rag.Status.Ready {
		t.Error("a scheduled re-index of a serving source must stay Ready")
	}
	cond := meta.FindStatusCondition(rag.Status.Conditions, conditionTypeReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("Ready condition = %v, want True", cond)
	}
}

func TestQueuedNeverServedSourceIsNotReady(t *testing.T) {
	rag, em := slotRAGSource(), slotEmbeddingModel(slotServer, 0)
	r, _ := slotReconciler(t, 1, rag, em, slotJob("a", false))
	if _, err := r.queueForSlot(context.Background(), rag, em); err != nil || rag.Status.Ready {
		t.Errorf("err=%v ready=%v, want not ready", err, rag.Status.Ready)
	}
}

func TestQueuedRetryReDerivesTheFullIndexAfterDataLoss(t *testing.T) {
	query := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"stats":{"document_count":0}}`))
	}))
	defer query.Close()
	rag, em := slotRAGSource(), slotEmbeddingModel(slotServer, 0)
	now := metav1.NewTime(time.Now())
	rag.Status = kubemootv1alpha1.RAGSourceStatus{
		Phase: phaseQueued, QueryEndpoint: query.URL, LastIndexedChecksum: testChecksum,
		IndexingStats: &kubemootv1alpha1.IndexingStats{LastIndexed: &now},
	}
	r, _ := slotReconciler(t, 1, rag, em, slotJob("a", false), slotJob("b", true))
	r.HTTPClient = query.Client()
	if !r.mustIndexEverything(context.Background(), rag) {
		t.Error("a queued retry must force a full index when the store is empty")
	}
	rag.Status.Phase = phaseReady
	if r.mustIndexEverything(context.Background(), rag) {
		t.Error("a healthy scheduled build keeps skipping unchanged content")
	}
}

func TestFinishedJobEnqueuesQueuedSourcesOnItsServer(t *testing.T) {
	queued, em := slotRAGSource(), slotEmbeddingModel(slotServer, 0)
	queued.Status.Phase = phaseQueued
	queued.Spec.EmbeddingModelRef = FallbackEmbeddingModel
	other := slotRAGSource()
	other.Name = "idle"
	other.Status.Phase = phaseReady
	other.Spec.EmbeddingModelRef = FallbackEmbeddingModel
	otherServer := slotRAGSource()
	otherServer.Name = "on-gpu-server"
	otherServer.Spec.EmbeddingModelRef = "other-model"
	otherServer.Status.Phase = phaseQueued
	em2 := slotEmbeddingModel("gpu-vllm", 0)
	em2.Name = "other-model"
	r, _ := slotReconciler(t, 1, queued, other, otherServer, em, em2)

	reqs := r.queuedRAGSourcesOfJob(context.Background(), slotJob("a", true))
	if len(reqs) != 1 || reqs[0].Name != slotRAGName {
		t.Fatalf("requests = %v, want only %s", reqs, slotRAGName)
	}
	if got := r.queuedRAGSourcesOfJob(context.Background(), &batchv1.Job{}); got != nil {
		t.Errorf("a Job with no server label maps to nothing, got %v", got)
	}
}

func TestIndexSlotFreedPredicate(t *testing.T) {
	p := indexSlotFreedPredicate()
	if p.Update(event.UpdateEvent{ObjectNew: slotJob("a", false)}) {
		t.Error("a running Job frees nothing")
	}
	if !p.Update(event.UpdateEvent{ObjectNew: slotJob("a", true)}) {
		t.Error("a completed Job frees a slot")
	}
	if p.Create(event.CreateEvent{Object: slotJob("a", true)}) {
		t.Error("a created Job frees nothing")
	}
}

func TestMaxIndexBuildsPerServerConfig(t *testing.T) {
	cache := NewConfigCache()
	if got := cache.GetMaxIndexBuildsPerServer(); got != 2 {
		t.Errorf("default = %d, want 2", got)
	}
	for _, bad := range []int32{0, -3} {
		cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{
			Defaults: kubemootv1alpha1.DefaultConfig{MaxIndexBuildsPerServer: bad},
		}})
		if got := cache.GetMaxIndexBuildsPerServer(); got != 2 {
			t.Errorf("value %d should fall back to 2, got %d", bad, got)
		}
	}
	cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{
		Defaults: kubemootv1alpha1.DefaultConfig{MaxIndexBuildsPerServer: 5},
	}})
	if got := cache.GetMaxIndexBuildsPerServer(); got != 5 {
		t.Errorf("configured = %d, want 5", got)
	}
}

func TestEmbeddingServerKeyIsAValidLabelValue(t *testing.T) {
	if got := embeddingServerKey(slotEmbeddingModel("", 0)); got != "" {
		t.Errorf("unbound key = %q, want empty", got)
	}
	short := embeddingServerKey(slotEmbeddingModel(slotServer, 0))
	if short != rbacTestNS+"."+slotServer {
		t.Errorf("short key = %q", short)
	}
	long := embeddingServerKey(slotEmbeddingModel(strings.Repeat("s", 100), 0))
	if len(long) != maxLabelValue {
		t.Errorf("long key length = %d, want %d", len(long), maxLabelValue)
	}
	other := embeddingServerKey(slotEmbeddingModel(strings.Repeat("s", 99)+"t", 0))
	if long == other {
		t.Error("different long names must not share a key")
	}
}

func TestIndexingJobCarriesServerLabelAndBatchSize(t *testing.T) {
	r := &RAGSourceReconciler{Client: rbacTestClient(t), ConfigCache: NewConfigCache()}
	job := r.buildIndexingJob(slotRAGSource(), slotEmbeddingModel(slotServer, 16), "docs-indexer-1")
	if job.Labels[labelEmbeddingServer] != rbacTestNS+"."+slotServer {
		t.Errorf("server label = %q", job.Labels[labelEmbeddingServer])
	}
	if got := slotJobEnv(job, "KUBEMOOT_EMBEDDING_BATCH_SIZE"); got != "16" {
		t.Errorf("batch size env = %q, want 16", got)
	}

	unset := r.buildIndexingJob(slotRAGSource(), slotEmbeddingModel("", 0), "docs-indexer-2")
	if _, ok := unset.Labels[labelEmbeddingServer]; ok {
		t.Error("an unbound model must not carry a server label")
	}
	if got := slotJobEnv(unset, "KUBEMOOT_EMBEDDING_BATCH_SIZE"); got != "" {
		t.Errorf("an unset batch size must leave the indexer default, got %q", got)
	}
}

func slotJobEnv(job *batchv1.Job, name string) string {
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}
