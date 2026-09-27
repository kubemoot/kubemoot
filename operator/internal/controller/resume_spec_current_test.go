package controller

import (
	"testing"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	"github.com/javajon/kubemoot/operator/internal/crewscope"
)

// A resume RAGSource is current only when its content hash, KV key, and
// collection all match the crew's scope. Moving to namespaced keys changed the
// key and collection without changing the content, and the RAGSource must
// still be rewritten or its indexer reads a key that no longer exists.
func TestResumeSpecCurrent(t *testing.T) {
	scope := crewscope.Scope{Namespace: "crew-grade", Crew: "grade"}
	vs := &kubemootv1alpha1.VectorStoreConfig{Type: FallbackVectorStoreType}
	current := buildResumeRAGSourceSpec(scope, "h1", vs, "embed")
	if !resumeSpecCurrent(current, "h1", scope) {
		t.Fatal("a spec built for this scope and hash is current")
	}
	if resumeSpecCurrent(current, "h2", scope) {
		t.Error("a different content hash is not current")
	}
	legacyKey := current
	kv := *current.Source.NatsKV
	kv.Key = crewscope.LegacyResumeKey(scope.Crew)
	legacyKey.Source.NatsKV = &kv
	if resumeSpecCurrent(legacyKey, "h1", scope) {
		t.Error("the same hash under the unscoped key is not current")
	}
	legacyCollection := current
	legacyCollection.VectorStore.Collection = "crew_grade_resumes"
	if resumeSpecCurrent(legacyCollection, "h1", scope) {
		t.Error("the same hash into the unscoped collection is not current")
	}
	noKV := current
	noKV.Source.NatsKV = nil
	if resumeSpecCurrent(noKV, "h1", scope) {
		t.Error("a spec without a NATS KV source is not current")
	}
}
