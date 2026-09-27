package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

const testChecksum = "sha256:abc"

// checksumEnv is the last-indexed checksum a job was handed, or "" when none.
func checksumEnv(job *batchv1.Job) string {
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "KUBEMOOT_LAST_CHECKSUM" {
			return e.Value
		}
	}
	return ""
}

// When the query service reports an empty collection, the re-index must write
// the documents again. Handing the job the last checksum lets it skip on
// unchanged content and leaves the collection empty, so the data-loss check
// fires again and the indexer loops (seen after resume collections moved to
// namespaced names).
func TestDataLossForcesFullIndex(t *testing.T) {
	query := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"stats":{"document_count":0}}`))
	}))
	defer query.Close()

	now := metav1.NewTime(time.Now())
	em := &kubemootv1alpha1.EmbeddingModel{
		ObjectMeta: metav1.ObjectMeta{Name: FallbackEmbeddingModel, Namespace: rbacTestNS},
		Spec:       kubemootv1alpha1.EmbeddingModelSpec{Model: "nomic-embed-text"},
	}
	rag := &kubemootv1alpha1.RAGSource{
		ObjectMeta: metav1.ObjectMeta{Name: "resumes", Namespace: rbacTestNS, Generation: 3},
		Spec: kubemootv1alpha1.RAGSourceSpec{Source: kubemootv1alpha1.SourceConfig{
			Type:   kubemootv1alpha1.RAGSourceTypeNatsKV,
			NatsKV: &kubemootv1alpha1.NatsKVSource{Bucket: resumeKVBucket, Key: "ns.crew", ContentHash: "h"},
		}},
		Status: kubemootv1alpha1.RAGSourceStatus{
			Phase:               crewPhaseReady,
			ObservedGeneration:  3,
			QueryEndpoint:       query.URL,
			LastIndexedChecksum: "sha256:unchanged",
			IndexingStats:       &kubemootv1alpha1.IndexingStats{LastIndexed: &now, DocumentCount: 22},
		},
	}
	scheme := rbacTestClient(t).Scheme()
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(rag, em).WithStatusSubresource(rag).Build()
	r := &RAGSourceReconciler{Client: c, Scheme: c.Scheme(), ConfigCache: NewConfigCache(), HTTPClient: query.Client()}

	if _, err := r.reconcileIndexingState(context.Background(), rag, em); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	jobs := &batchv1.JobList{}
	if err := c.List(context.Background(), jobs); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("want one indexing job after data loss, got %d", len(jobs.Items))
	}
	if got := checksumEnv(&jobs.Items[0]); got != "" {
		t.Errorf("a re-index after data loss must not be handed the last checksum, got %q", got)
	}
}

// A job for an unchanged, healthy source still gets the checksum, so scheduled
// runs keep skipping content that has not changed.
func TestUnchangedSourceKeepsChecksum(t *testing.T) {
	r := &RAGSourceReconciler{Client: rbacTestClient(t), ConfigCache: NewConfigCache()}
	em := &kubemootv1alpha1.EmbeddingModel{ObjectMeta: metav1.ObjectMeta{Name: FallbackEmbeddingModel, Namespace: rbacTestNS}}
	rag := &kubemootv1alpha1.RAGSource{
		ObjectMeta: metav1.ObjectMeta{Name: "course", Namespace: rbacTestNS},
		Spec: kubemootv1alpha1.RAGSourceSpec{Source: kubemootv1alpha1.SourceConfig{
			Type: kubemootv1alpha1.RAGSourceTypeGit, Git: &kubemootv1alpha1.GitSource{URL: "https://example.com/r"},
		}},
		Status: kubemootv1alpha1.RAGSourceStatus{LastIndexedChecksum: testChecksum},
	}
	if got := checksumEnv(r.buildIndexingJob(rag, em, "course-indexer-1")); got != testChecksum {
		t.Fatalf("unchanged source job checksum = %q, want %q", got, testChecksum)
	}
	forceFullIndex(rag)
	if got := checksumEnv(r.buildIndexingJob(rag, em, "course-indexer-2")); got != "" {
		t.Fatalf("after forceFullIndex the job checksum = %q, want none", got)
	}
}
