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
	"reflect"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	idxOtherColl  = "other-collection"
	idxEndpointA  = "http://a"
	idxNS         = "idx-ns"
	idxCollection = "docs"
	idxModel      = "nomic-embed-text"
	idxOldServer  = "ollama"
	idxNewServer  = "vllm"
	idxOldURL     = "http://ollama:11434"
	idxNewURL     = "http://vllm:8000"
)

// indexHarness runs the RAGSource reconciler against a fake cluster.
type indexHarness struct {
	t   *testing.T
	c   client.Client
	r   *RAGSourceReconciler
	rag *kubemootv1alpha1.RAGSource
	em  *kubemootv1alpha1.EmbeddingModel
}

// newIndexHarness is a RAGSource indexed once with the model on the Ollama server,
// whose EmbeddingModel is now bound as the arguments say.
func newIndexHarness(t *testing.T, server, endpoint string, dims int32) *indexHarness {
	t.Helper()
	now := metav1.NewTime(time.Now())
	em := &kubemootv1alpha1.EmbeddingModel{
		ObjectMeta: metav1.ObjectMeta{Name: embedTestName, Namespace: idxNS},
		Spec:       kubemootv1alpha1.EmbeddingModelSpec{Model: idxModel},
		Status: kubemootv1alpha1.EmbeddingModelStatus{
			Ready: true, BoundServer: server, Endpoint: endpoint,
			ModelInfo: &kubemootv1alpha1.EmbeddingModelInfo{Dimensions: dims},
		},
	}
	rag := &kubemootv1alpha1.RAGSource{
		ObjectMeta: metav1.ObjectMeta{Name: "docs-rag", Namespace: idxNS, Generation: 1},
		Spec: kubemootv1alpha1.RAGSourceSpec{
			EmbeddingModelRef: embedTestName,
			Source:            kubemootv1alpha1.SourceConfig{Type: kubemootv1alpha1.RAGSourceTypeURL},
			VectorStore:       kubemootv1alpha1.VectorStoreConfig{Type: FallbackVectorStoreType, Endpoint: "postgres://u@db:5432/v", Collection: idxCollection, Dimensions: 768},
		},
		Status: kubemootv1alpha1.RAGSourceStatus{
			Phase: phaseReady, Ready: true, ObservedGeneration: 1, LastIndexedChecksum: "sha256:old",
			IndexingStats: &kubemootv1alpha1.IndexingStats{LastIndexed: &now, DocumentCount: 5},
			Index: &kubemootv1alpha1.IndexProvenance{
				Collection: idxCollection, Base: idxCollection, Model: idxModel, Server: idxOldServer, Endpoint: idxOldURL, Dimensions: 768,
			},
		},
	}
	scheme := rbacTestClient(t).Scheme()
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(rag, em).
		WithStatusSubresource(rag, &batchv1.Job{}).Build()
	return &indexHarness{t: t, c: c, rag: rag, em: em,
		r: &RAGSourceReconciler{Client: c, Scheme: scheme, ConfigCache: NewConfigCache()}}
}

// reconcile runs one pass of the indexing state machine and reloads the RAGSource.
func (h *indexHarness) reconcile() {
	h.t.Helper()
	if _, err := h.r.reconcileIndexingState(context.Background(), h.rag, h.em); err != nil {
		h.t.Fatalf("reconcile: %v", err)
	}
	h.reload()
}

func (h *indexHarness) reload() {
	h.t.Helper()
	got := &kubemootv1alpha1.RAGSource{}
	if err := h.c.Get(context.Background(), client.ObjectKeyFromObject(h.rag), got); err != nil {
		h.t.Fatal(err)
	}
	h.rag = got
}

func (h *indexHarness) jobs(component string) []batchv1.Job {
	h.t.Helper()
	var list batchv1.JobList
	if err := h.c.List(context.Background(), &list, client.InNamespace(idxNS)); err != nil {
		h.t.Fatal(err)
	}
	var out []batchv1.Job
	for _, j := range list.Items {
		if j.Labels[labelComponent] == component {
			out = append(out, j)
		}
	}
	return out
}

// finishJob marks the named Job complete (with the annotations an indexer writes) or failed.
func (h *indexHarness) finishJob(name string, ok bool) {
	h.t.Helper()
	job := &batchv1.Job{}
	if err := h.c.Get(context.Background(), client.ObjectKey{Namespace: idxNS, Name: name}, job); err != nil {
		h.t.Fatal(err)
	}
	if job.Annotations == nil {
		job.Annotations = map[string]string{}
	}
	for k, v := range map[string]string{annotationJobStatus: jobStatusIndexed, annotationJobChecksum: "sha256:new",
		annotationJobDocuments: "5", annotationJobChunks: "9"} {
		job.Annotations[k] = v
	}
	if err := h.c.Update(context.Background(), job); err != nil {
		h.t.Fatal(err)
	}
	cond := batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}
	if !ok {
		cond = batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Message: "boom"}
	}
	job.Status.Conditions = []batchv1.JobCondition{cond}
	if err := h.c.Status().Update(context.Background(), job); err != nil {
		h.t.Fatal(err)
	}
}

func envOf(job batchv1.Job, name string) string {
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

func (h *indexHarness) queryEnv(name string) string {
	h.t.Helper()
	dep := h.r.buildQueryServiceDeployment(h.rag, h.em, "q")
	for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

func TestUnchangedBindingBuildsNoSecondIndex(t *testing.T) {
	h := newIndexHarness(t, idxOldServer, idxOldURL, 768)

	h.reconcile()

	if len(h.jobs(componentIndexer)) != 0 || h.rag.Status.Building != nil {
		t.Errorf("jobs = %d building = %+v, want no rebuild for an unchanged binding", len(h.jobs(componentIndexer)), h.rag.Status.Building)
	}
}

func TestMovingTheEndpointOfTheSameServerDoesNotRebuild(t *testing.T) {
	h := newIndexHarness(t, idxOldServer, "http://ollama-new:11434", 768)

	h.reconcile()

	if len(h.jobs(componentIndexer)) != 0 || h.rag.Status.Index.Endpoint != "http://ollama-new:11434" {
		t.Errorf("jobs = %d index = %+v, want the endpoint followed without a rebuild", len(h.jobs(componentIndexer)), h.rag.Status.Index)
	}
}

func TestChangedBindingBuildsTheNewIndexBesideTheOldOne(t *testing.T) {
	h := newIndexHarness(t, idxNewServer, idxNewURL, 768)

	h.reconcile()

	jobs := h.jobs(componentIndexer)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d, want one build", len(jobs))
	}
	coll := envOf(jobs[0], envVectorstoreCollection)
	if coll == idxCollection || !strings.HasPrefix(coll, idxCollection+"_") {
		t.Errorf("job collection = %q, want a new collection named from %q", coll, idxCollection)
	}
	if h.rag.Status.Index.Collection != idxCollection || h.rag.Status.Building == nil || h.rag.Status.Building.Collection != coll {
		t.Errorf("status index = %+v building = %+v, want the old index kept and the new one recorded as building", h.rag.Status.Index, h.rag.Status.Building)
	}
	if !h.rag.Status.Ready {
		t.Error("readers keep using the old index, so the RAGSource stays Ready")
	}
}

func TestRebuildIsHandedTheNewServerAndAFullIndex(t *testing.T) {
	h := newIndexHarness(t, idxNewServer, idxNewURL, 768)

	h.reconcile()

	job := h.jobs(componentIndexer)[0]
	if envOf(job, "KUBEMOOT_EMBEDDING_ENDPOINT") != idxNewURL || envOf(job, "KUBEMOOT_FORCE_REINDEX") != valueTrue ||
		envOf(job, "KUBEMOOT_LAST_CHECKSUM") != "" {
		t.Errorf("job env = %+v, want the new server, a full index and no last checksum", job.Spec.Template.Spec.Containers[0].Env)
	}
}

func TestReadersStayOnTheOldIndexUntilTheNewOneIsComplete(t *testing.T) {
	h := newIndexHarness(t, idxNewServer, idxNewURL, 768)
	h.reconcile()
	h.reconcile() // the build is still running

	if len(h.jobs(componentIndexer)) != 1 {
		t.Fatalf("jobs = %d, want the one running build left alone", len(h.jobs(componentIndexer)))
	}
	if got := h.queryEnv(envVectorstoreCollection); got != idxCollection {
		t.Errorf("query service collection = %q, want %q while the new index builds", got, idxCollection)
	}
	if got := h.queryEnv("KUBEMOOT_EMBEDDING_ENDPOINT"); got != idxOldURL {
		t.Errorf("query service embeds through %q, want the server that made the old vectors (%s)", got, idxOldURL)
	}
}

func TestCompleteBuildSwitchesReadersAndRetiresTheOldIndex(t *testing.T) {
	h := newIndexHarness(t, idxNewServer, idxNewURL, 768)
	h.reconcile()
	built := h.rag.Status.Building.Collection
	h.finishJob(h.jobs(componentIndexer)[0].Name, true)

	h.reconcile()

	if h.rag.Status.Index.Collection != built || h.rag.Status.Index.Server != idxNewServer || h.rag.Status.Building != nil {
		t.Fatalf("index = %+v building = %+v, want the new index serving", h.rag.Status.Index, h.rag.Status.Building)
	}
	if len(h.rag.Status.RetiredCollections) != 1 || h.rag.Status.RetiredCollections[0] != idxCollection {
		t.Errorf("retired = %v, want [%s]", h.rag.Status.RetiredCollections, idxCollection)
	}
	if got := h.queryEnv(envVectorstoreCollection); got != built {
		t.Errorf("query service collection = %q, want the new one %q", got, built)
	}
	if got := h.queryEnv("KUBEMOOT_EMBEDDING_ENDPOINT"); got != idxNewURL {
		t.Errorf("query service endpoint = %q, want %q", got, idxNewURL)
	}
}

func TestFailedBuildKeepsTheOldIndexServingAndTriesAgain(t *testing.T) {
	h := newIndexHarness(t, idxNewServer, idxNewURL, 768)
	h.reconcile()
	h.finishJob(h.jobs(componentIndexer)[0].Name, false)

	h.reconcile()

	if h.rag.Status.Index.Collection != idxCollection || h.rag.Status.Building == nil || !h.rag.Status.Ready {
		t.Fatalf("index = %+v building = %+v ready = %v, want the old index still serving", h.rag.Status.Index, h.rag.Status.Building, h.rag.Status.Ready)
	}
	if !strings.Contains(h.rag.Status.Message, "failed") || !strings.Contains(h.rag.Status.Message, idxCollection) {
		t.Errorf("message = %q, want the failure and the collection still served", h.rag.Status.Message)
	}
	if len(h.rag.Status.RetiredCollections) != 0 {
		t.Errorf("retired = %v, want the old index untouched", h.rag.Status.RetiredCollections)
	}
	time.Sleep(1100 * time.Millisecond) // job names carry the second they were made in
	h.reconcile()
	if len(h.jobs(componentIndexer)) != 2 {
		t.Errorf("jobs = %d, want a new build after the failed one", len(h.jobs(componentIndexer)))
	}
}

func TestBuildThatMovedOnAgainIsDiscarded(t *testing.T) {
	h := newIndexHarness(t, idxNewServer, idxNewURL, 768)
	h.reconcile()
	abandoned := h.rag.Status.Building.Collection
	h.em.Status.BoundServer = "third"

	h.reconcile()

	if h.rag.Status.Building != nil || h.rag.Status.Index.Collection != idxCollection {
		t.Fatalf("index = %+v building = %+v, want the half-built index dropped and the old one kept", h.rag.Status.Index, h.rag.Status.Building)
	}
	if len(h.rag.Status.RetiredCollections) != 1 || h.rag.Status.RetiredCollections[0] != abandoned {
		t.Errorf("retired = %v, want [%s]", h.rag.Status.RetiredCollections, abandoned)
	}
}

func TestChangedVectorSizeBuildsANewIndex(t *testing.T) {
	h := newIndexHarness(t, idxOldServer, idxOldURL, 1024)

	h.reconcile()

	jobs := h.jobs(componentIndexer)
	if len(jobs) != 1 || envOf(jobs[0], envVectorstoreDimensions) != "1024" {
		t.Errorf("jobs = %d, want one build with 1024 dimensions", len(jobs))
	}
}

func TestIndexedBeforeTheStatusRecordedItAdoptsTheCurrentBinding(t *testing.T) {
	h := newIndexHarness(t, idxOldServer, idxOldURL, 768)
	h.rag.Status.Index = nil

	h.reconcile()

	if h.rag.Status.Index == nil || h.rag.Status.Index.Server != idxOldServer || h.rag.Status.Index.Collection != idxCollection {
		t.Errorf("index = %+v, want the existing collection adopted", h.rag.Status.Index)
	}
	if len(h.jobs(componentIndexer)) != 0 {
		t.Error("an index made before the status recorded it is not rebuilt")
	}
}

func TestNewSpecCollectionStartsOverWithoutRetiringTheOldOne(t *testing.T) {
	h := newIndexHarness(t, idxOldServer, idxOldURL, 768)
	h.rag.Spec.VectorStore.Collection = idxOtherColl
	h.rag.Generation = 2

	h.reconcile()

	jobs := h.jobs(componentIndexer)
	if len(jobs) != 1 || envOf(jobs[0], envVectorstoreCollection) != idxOtherColl || len(h.rag.Status.RetiredCollections) != 0 {
		t.Errorf("jobs = %d retired = %v, want one job into the new collection and nothing retired", len(jobs), h.rag.Status.RetiredCollections)
	}
}

func TestFirstIndexIsRecordedAndServedAfterItCompletes(t *testing.T) {
	h := newIndexHarness(t, idxNewServer, idxNewURL, 768)
	h.rag.Status = kubemootv1alpha1.RAGSourceStatus{}

	h.reconcile()
	jobs := h.jobs(componentIndexer)
	if len(jobs) != 1 || envOf(jobs[0], envVectorstoreCollection) != idxCollection || h.rag.Status.Ready {
		t.Fatalf("jobs = %d ready = %v, want one first build into %q", len(jobs), h.rag.Status.Ready, idxCollection)
	}
	h.finishJob(jobs[0].Name, true)
	h.reconcile()

	if h.rag.Status.Index == nil || h.rag.Status.Index.Server != idxNewServer || h.rag.Status.Building != nil || len(h.rag.Status.RetiredCollections) != 0 {
		t.Errorf("index = %+v building = %+v retired = %v", h.rag.Status.Index, h.rag.Status.Building, h.rag.Status.RetiredCollections)
	}
}

// readerDeployment is a query service Deployment reading the collection, with its
// rollout complete or not.
func readerDeployment(rag *kubemootv1alpha1.RAGSource, collection string, rolledOut bool) *appsv1.Deployment {
	one := int32(1)
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: queryServiceDeploymentName(rag), Namespace: rag.Namespace, Generation: 1},
		Spec: appsv1.DeploymentSpec{Replicas: &one, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "q", Env: []corev1.EnvVar{{Name: envVectorstoreCollection, Value: collection}}}},
		}}},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1},
	}
	if !rolledOut {
		d.Status.Replicas = 2 // an old pod still runs beside the new one
	}
	return d
}

func retiredHarness(t *testing.T, dep func(*kubemootv1alpha1.RAGSource) *appsv1.Deployment) *indexHarness {
	t.Helper()
	h := newIndexHarness(t, idxNewServer, idxNewURL, 768)
	h.rag.Status.Index = &kubemootv1alpha1.IndexProvenance{
		Collection: "docs_new", Base: idxCollection, Model: idxModel, Server: idxNewServer, Endpoint: idxNewURL, Dimensions: 768}
	h.rag.Status.RetiredCollections = []string{idxCollection}
	if err := h.c.Status().Update(context.Background(), h.rag); err != nil {
		t.Fatal(err)
	}
	if err := h.c.Create(context.Background(), dep(h.rag)); err != nil {
		t.Fatal(err)
	}
	h.reload()
	return h
}

func TestOldIndexIsKeptWhileReadersStillUseIt(t *testing.T) {
	h := retiredHarness(t, func(r *kubemootv1alpha1.RAGSource) *appsv1.Deployment {
		return readerDeployment(r, idxCollection, true)
	})

	h.reconcile()

	if len(h.jobs(componentIndexCleanup)) != 0 || len(h.rag.Status.RetiredCollections) != 1 {
		t.Errorf("cleanup jobs = %d retired = %v, want nothing deleted while readers use %s", len(h.jobs(componentIndexCleanup)), h.rag.Status.RetiredCollections, idxCollection)
	}
}

func TestOldIndexIsKeptWhileTheReaderRolloutIsUnfinished(t *testing.T) {
	h := retiredHarness(t, func(r *kubemootv1alpha1.RAGSource) *appsv1.Deployment { return readerDeployment(r, "docs_new", false) })

	h.reconcile()

	if len(h.jobs(componentIndexCleanup)) != 0 {
		t.Error("an old pod still reads the old collection, so it must not be deleted yet")
	}
}

func TestOldIndexIsDeletedOnceReadersHaveMoved(t *testing.T) {
	h := retiredHarness(t, func(r *kubemootv1alpha1.RAGSource) *appsv1.Deployment { return readerDeployment(r, "docs_new", true) })

	h.reconcile()

	jobs := h.jobs(componentIndexCleanup)
	if len(jobs) != 1 || envOf(jobs[0], "KUBEMOOT_DROP_COLLECTIONS") != idxCollection || envOf(jobs[0], envVectorstoreCollection) != "docs_new" {
		t.Fatalf("cleanup jobs = %+v, want one deleting %s while connected to docs_new", jobs, idxCollection)
	}
	if len(h.jobs(componentIndexer)) != 0 {
		t.Error("the cleanup Job must not look like an indexing job")
	}
	h.finishJob(jobs[0].Name, true)
	h.reconcile()
	if len(h.rag.Status.RetiredCollections) != 0 {
		t.Errorf("retired = %v, want it cleared once the collection is deleted", h.rag.Status.RetiredCollections)
	}
}

func TestFailedCleanupIsRemovedAndRetriedLater(t *testing.T) {
	h := retiredHarness(t, func(r *kubemootv1alpha1.RAGSource) *appsv1.Deployment { return readerDeployment(r, "docs_new", true) })
	h.reconcile()
	h.finishJob(h.jobs(componentIndexCleanup)[0].Name, false)

	h.reconcile()

	if len(h.jobs(componentIndexCleanup)) != 0 || len(h.rag.Status.RetiredCollections) != 1 {
		t.Errorf("cleanup jobs = %d retired = %v, want the failed Job removed and the collection still queued", len(h.jobs(componentIndexCleanup)), h.rag.Status.RetiredCollections)
	}
}

func TestIndexNamesAndBindingComparison(t *testing.T) {
	a := kubemootv1alpha1.IndexProvenance{Model: "m", Server: "s", Dimensions: 768, Endpoint: idxEndpointA}
	b := a
	b.Endpoint = "http://b"
	if !sameProducer(a, b) {
		t.Error("a moved endpoint is the same producer")
	}
	for _, change := range []func(*kubemootv1alpha1.IndexProvenance){
		func(p *kubemootv1alpha1.IndexProvenance) { p.Model = testMismatchName },
		func(p *kubemootv1alpha1.IndexProvenance) { p.Server = testMismatchName },
		func(p *kubemootv1alpha1.IndexProvenance) { p.Dimensions = 1024 },
	} {
		c := a
		change(&c)
		if sameProducer(a, c) {
			t.Errorf("%+v and %+v must differ", a, c)
		}
	}
	at := time.Unix(1_800_000_000, 0)
	n1 := buildCollectionName("docs", a, at)
	if n1 == buildCollectionName("docs", a, at.Add(time.Second)) {
		t.Error("two builds must not share a collection name")
	}
	if !strings.HasPrefix(n1, "docs_") || strings.ContainsAny(n1, "-. ") {
		t.Errorf("collection name %q must be a safe table suffix", n1)
	}
}

func TestQueryServiceGetsAMovedEndpointInTheSameReconcile(t *testing.T) {
	h := newIndexHarness(t, idxOldServer, "http://ollama-new:11434", 768)
	ctx := context.Background()

	if err := h.r.ensureQueryService(ctx, h.rag, h.em); err != nil {
		t.Fatal(err)
	}
	h.reconcile()

	var dep appsv1.Deployment
	if err := h.c.Get(ctx, client.ObjectKey{Namespace: idxNS, Name: queryServiceDeploymentName(h.rag)}, &dep); err != nil {
		t.Fatal(err)
	}
	got := ""
	for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "KUBEMOOT_EMBEDDING_ENDPOINT" {
			got = e.Value
		}
	}
	if got != "http://ollama-new:11434" {
		t.Errorf("query service endpoint = %q, want the moved server's endpoint on the first reconcile", got)
	}
}

func TestOldIndexIsKeptWhileAnIndexerPodStillRuns(t *testing.T) {
	h := retiredHarness(t, func(r *kubemootv1alpha1.RAGSource) *appsv1.Deployment { return readerDeployment(r, "docs_new", true) })
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "indexer-pod", Namespace: idxNS,
		Labels: map[string]string{labelRAGSource: h.rag.Name, labelComponent: componentIndexer}}}
	if err := h.c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}

	h.reconcile()
	if len(h.jobs(componentIndexCleanup)) != 0 {
		t.Fatal("an indexer pod could recreate the table, so nothing may be deleted yet")
	}

	if err := h.c.Delete(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	h.reconcile()
	if len(h.jobs(componentIndexCleanup)) != 1 {
		t.Errorf("cleanup jobs = %d, want one once no indexer pod remains", len(h.jobs(componentIndexCleanup)))
	}
}

func TestCleanupAndIndexingJobsShareTheHardenedPodSpec(t *testing.T) {
	h := newIndexHarness(t, idxOldServer, idxOldURL, 768)
	h.rag.Status.RetiredCollections = []string{idxCollection}
	cleanup := h.r.buildCleanupJob(h.rag, "c").Spec.Template.Spec
	indexing := h.r.buildIndexingJob(h.rag, h.em, "i").Spec.Template.Spec

	if !reflect.DeepEqual(cleanup.SecurityContext, indexing.SecurityContext) ||
		!reflect.DeepEqual(cleanup.Containers[0].SecurityContext, indexing.Containers[0].SecurityContext) {
		t.Error("the two jobs must run with the same pod and container security context")
	}
	sc := cleanup.Containers[0].SecurityContext
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation || sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot ||
		sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 {
		t.Errorf("container security context = %+v, want hardened", sc)
	}
}
