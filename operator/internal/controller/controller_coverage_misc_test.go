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
	"testing"
	"time"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/crewscope"
	"github.com/xuri/excelize/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGenerateRunID(t *testing.T) {
	long := &kubemootv1alpha1.CrewFitnessSuite{ObjectMeta: metav1.ObjectMeta{UID: types.UID("0123456789abcdef")}}
	if got := generateRunID(long); got != "01234567" {
		t.Errorf("generateRunID truncates to 8 chars, got %q", got)
	}
	short := &kubemootv1alpha1.CrewFitnessSuite{ObjectMeta: metav1.ObjectMeta{UID: types.UID(testABC)}}
	if got := generateRunID(short); got != testABC {
		t.Errorf("a short UID is returned whole, got %q", got)
	}
}

func TestSetSuiteError(t *testing.T) {
	scheme := agentReconcileScheme(t)
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: testSuite, Namespace: "ns"},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(suite).WithStatusSubresource(suite).Build()
	r := &CrewFitnessSuiteReconciler{Client: cli}
	if _, err := r.setSuiteError(context.Background(), suite, testBoom); err != nil {
		t.Fatalf("setSuiteError: %v", err)
	}
	got := &kubemootv1alpha1.CrewFitnessSuite{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: testSuite, Namespace: "ns"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != kubemootv1alpha1.CrewFitnessSuitePhaseError || got.Status.Error != testBoom {
		t.Errorf("status not stamped: phase=%q error=%q", got.Status.Phase, got.Status.Error)
	}
	if got.Status.CompletedAt == nil {
		t.Error("setSuiteError should stamp CompletedAt")
	}
}

func TestControllerSetOwnerReference(t *testing.T) {
	scheme := agentReconcileScheme(t)
	owner := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: testOwner, Namespace: "ns", UID: types.UID("u1")},
	}
	child := &kubemootv1alpha1.CrewFitness{
		ObjectMeta: metav1.ObjectMeta{Name: "child", Namespace: "ns"},
	}
	if err := controllerSetOwnerReference(owner, child, scheme); err != nil {
		t.Fatalf("controllerSetOwnerReference: %v", err)
	}
	refs := child.GetOwnerReferences()
	if len(refs) != 1 || refs[0].Name != testOwner {
		t.Errorf("expected one owner ref to 'owner', got %+v", refs)
	}
}

func TestBuildResumeRAGSourceSpec(t *testing.T) {
	vs := &kubemootv1alpha1.VectorStoreConfig{
		Type:       kubemootv1alpha1.VectorStoreQdrant,
		Endpoint:   "http://qdrant:6333",
		Dimensions: 768,
	}
	spec := buildResumeRAGSourceSpec(crewscope.Scope{Namespace: testNamespaceA, Crew: testCrewPilot}, "hash123", vs, "embed-model")
	if spec.Source.Type != kubemootv1alpha1.RAGSourceTypeNatsKV {
		t.Errorf("source type = %q, want NatsKV", spec.Source.Type)
	}
	if spec.Source.NatsKV == nil || spec.Source.NatsKV.Key != "team-a.pilot" || spec.Source.NatsKV.ContentHash != "hash123" {
		t.Errorf("NatsKV source not wired: %+v", spec.Source.NatsKV)
	}
	assertResumeStoreAndChunking(t, spec)
}

// assertResumeStoreAndChunking checks the vector store and the atomic chunking of a resume RAGSource spec.
func assertResumeStoreAndChunking(t *testing.T, spec kubemootv1alpha1.RAGSourceSpec) {
	t.Helper()
	if spec.VectorStore.Collection != "crew_team_a_pilot_resumes" || spec.VectorStore.Endpoint != "http://qdrant:6333" {
		t.Errorf("vector store not carried through: %+v", spec.VectorStore)
	}
	if spec.EmbeddingModelRef != "embed-model" {
		t.Errorf("embedding model ref = %q", spec.EmbeddingModelRef)
	}
	// A resume must stay atomic: one large chunk, no overlap.
	if spec.Chunking == nil || spec.Chunking.ChunkSize != 20000 || spec.Chunking.ChunkOverlap != 0 {
		t.Errorf("chunking should keep the resume atomic: %+v", spec.Chunking)
	}
}

func TestCreateOrUpdateResumeRAGSource(t *testing.T) {
	scheme := agentReconcileScheme(t)
	coord := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: testCoord, Namespace: "ns", UID: types.UID("c1")},
	}
	spec := kubemootv1alpha1.RAGSourceSpec{EmbeddingModelRef: "embed-v1"}

	// Create path: no RAGSource exists yet.
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(coord).Build()
	r := &AgentReconciler{Client: cli, Scheme: scheme}
	if !r.createOrUpdateResumeRAGSource(context.Background(), coord, "coord-resume", testCrewA, spec) {
		t.Fatal("create path should report success")
	}
	got := &kubemootv1alpha1.RAGSource{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: "coord-resume", Namespace: "ns"}, got); err != nil {
		t.Fatalf("RAGSource was not created: %v", err)
	}
	if got.Spec.EmbeddingModelRef != "embed-v1" {
		t.Errorf("spec not applied on create: %q", got.Spec.EmbeddingModelRef)
	}
	if len(got.OwnerReferences) != 1 || got.OwnerReferences[0].Name != testCoord {
		t.Errorf("create should owner-reference the coordinator, got %+v", got.OwnerReferences)
	}

	// Update path: the RAGSource now exists; a new spec is written in place.
	spec2 := kubemootv1alpha1.RAGSourceSpec{EmbeddingModelRef: "embed-v2"}
	if !r.createOrUpdateResumeRAGSource(context.Background(), coord, "coord-resume", testCrewA, spec2) {
		t.Fatal("update path should report success")
	}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: "coord-resume", Namespace: "ns"}, got); err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.Spec.EmbeddingModelRef != "embed-v2" {
		t.Errorf("update did not replace the spec in place: %q", got.Spec.EmbeddingModelRef)
	}
}

func TestOverviewWriterPutDate(t *testing.T) {
	f := excelize.NewFile()
	if _, err := f.NewSheet(sheetOverview); err != nil {
		t.Fatalf("new sheet: %v", err)
	}
	o := &overviewWriter{f: f, row: 3, dateStyle: 0}
	row, err := o.putDate("Started", time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatalf("putDate: %v", err)
	}
	if row != 3 {
		t.Errorf("putDate should report the row it landed on (3), got %d", row)
	}
	if o.row != 4 {
		t.Errorf("putDate should advance the cursor to 4, got %d", o.row)
	}
}
