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

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func resumeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	return s
}

// TestSecretExists guards the gate that defers resume RAGSource creation until the
// vectorStore secret is present in the crew namespace (so indexer/query pods never
// wedge in CreateContainerConfigError).
func TestSecretExists(t *testing.T) {
	scheme := resumeScheme(t)
	present := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db-creds", Namespace: "crew-x"}}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(present).Build()

	if !secretExists(context.Background(), cli, "db-creds", "crew-x") {
		t.Error("expected secretExists true for present secret")
	}
	if secretExists(context.Background(), cli, "db-creds", "other-ns") {
		t.Error("expected secretExists false in a namespace without the secret")
	}
	if secretExists(context.Background(), cli, "missing", "crew-x") {
		t.Error("expected secretExists false for absent secret")
	}
}

func mkResumeAgent(ns, name, crew, role string, opts func(*kubemootv1alpha1.Agent)) *kubemootv1alpha1.Agent {
	a := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{crewLabelKey: crew},
		},
		Spec: kubemootv1alpha1.AgentSpec{DiscussRole: role, Description: name + " desc"},
	}
	if opts != nil {
		opts(a)
	}
	return a
}

// TestBuildAgentResume maps an Agent spec into the resume the indexer embeds and
// the triage selector reads, including the role default and the summary
// annotation.
func TestBuildAgentResume(t *testing.T) {
	a := mkResumeAgent("crew-x", "k8s-nodes", "homelab-pilot", "tooler", func(a *kubemootv1alpha1.Agent) {
		a.Spec.DiscussKeywords = []string{"node", "kubelet"}
		a.Spec.EnabledTools = []string{"nodes_list", "node_describe"}
		a.Spec.DiscussChannels = []string{"kubernetes"}
		a.Annotations = map[string]string{triageSummaryAnno: "node health and capacity"}
	})
	bare := mkResumeAgent("crew-x", "x", "homelab-pilot", "", nil)
	cli := fake.NewClientBuilder().WithScheme(resumeScheme(t)).WithObjects(a, bare).Build()
	rec := &AgentReconciler{Client: cli}
	r := rec.buildAgentResume(context.Background(), a)
	if r.Name != "k8s-nodes" || r.Role != "tooler" || r.Description != "k8s-nodes desc" {
		t.Errorf("unexpected base fields: %+v", r)
	}
	if len(r.Keywords) != 2 || len(r.Tools) != 2 || len(r.Channels) != 1 {
		t.Errorf("unexpected slices: %+v", r)
	}
	if r.Summary != "node health and capacity" {
		t.Errorf("summary = %q, want the annotation value", r.Summary)
	}

	// Empty role defaults to tooler.
	if got := rec.buildAgentResume(context.Background(), bare); got.Role != "tooler" {
		t.Errorf("empty role default = %q, want tooler", got.Role)
	}
}

// TestBuildAgentResumeSummaryFromSpec: spec.triageSummary, the field crew authors
// write, is the resume summary; the annotation is used only when the spec is empty.
func TestBuildAgentResumeSummaryFromSpec(t *testing.T) {
	both := mkResumeAgent("crew-x", "docs-reader", "platform-ops", "analyst", func(a *kubemootv1alpha1.Agent) {
		a.Spec.TriageSummary = "explains what a crew status means"
		a.Annotations = map[string]string{triageSummaryAnno: "older annotation"}
	})
	cli := fake.NewClientBuilder().WithScheme(resumeScheme(t)).WithObjects(both).Build()
	rec := &AgentReconciler{Client: cli}
	if got := rec.buildAgentResume(context.Background(), both).Summary; got != "explains what a crew status means" {
		t.Errorf("summary = %q, want spec.triageSummary", got)
	}
	both.Spec.TriageSummary = ""
	if got := rec.buildAgentResume(context.Background(), both).Summary; got != "older annotation" {
		t.Errorf("summary = %q, want the annotation when the spec is empty", got)
	}
}

// TestCompileCrewResumes lists only the crew's toolers — the coordinator
// (the selector, not a candidate) and other crews are excluded — sorted by name.
func TestCompileCrewResumes(t *testing.T) {
	scheme := resumeScheme(t)
	coord := mkResumeAgent("crew-x", "homelab-coordinator", "homelab-pilot", "coordinator", nil)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		coord,
		mkResumeAgent("crew-x", "k8s-nodes", "homelab-pilot", "tooler", nil),
		mkResumeAgent("crew-x", "gpu-now", "homelab-pilot", "tooler", nil),
		mkResumeAgent("crew-x", "other-agent", "some-other-crew", "tooler", nil),
	).Build()
	r := &AgentReconciler{Client: cli}

	resumes := r.compileCrewResumes(context.Background(), coord, "homelab-pilot")
	if len(resumes) != 2 {
		t.Fatalf("got %d resumes, want 2 (coordinator + other-crew excluded): %+v", len(resumes), resumes)
	}
	if resumes[0].Name != "gpu-now" || resumes[1].Name != "k8s-nodes" {
		t.Errorf("resumes not sorted by name: %q, %q", resumes[0].Name, resumes[1].Name)
	}
}

// TestDiscoverRAGSourceDefaults copies vectorStore + embeddingModel config from a
// real (non-resume) RAGSource and skips nats-kv resume sources.
func TestDiscoverRAGSourceDefaults(t *testing.T) {
	scheme := resumeScheme(t)
	gitRag := &kubemootv1alpha1.RAGSource{
		ObjectMeta: metav1.ObjectMeta{Name: "k8s-docs", Namespace: "crew-x"},
		Spec: kubemootv1alpha1.RAGSourceSpec{
			Source:            kubemootv1alpha1.SourceConfig{Type: kubemootv1alpha1.RAGSourceTypeGit},
			VectorStore:       kubemootv1alpha1.VectorStoreConfig{Type: "pgvector", Endpoint: "postgres://pg:5432", Dimensions: 384},
			EmbeddingModelRef: "nomic-embed",
		},
	}
	natsRag := &kubemootv1alpha1.RAGSource{
		ObjectMeta: metav1.ObjectMeta{Name: "crew-other-resumes", Namespace: "crew-x"},
		Spec: kubemootv1alpha1.RAGSourceSpec{
			Source:            kubemootv1alpha1.SourceConfig{Type: kubemootv1alpha1.RAGSourceTypeNatsKV},
			VectorStore:       kubemootv1alpha1.VectorStoreConfig{Type: "pgvector", Endpoint: "should-be-skipped", Dimensions: 384},
			EmbeddingModelRef: "nomic-embed",
		},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gitRag, natsRag).Build()
	r := &AgentReconciler{Client: cli}

	vs, embRef, ns := r.discoverRAGSourceDefaults(context.Background(), "crew-x")
	if vs == nil {
		t.Fatal("expected a vectorStore from the git RAGSource, got nil")
	}
	if vs.Endpoint != "postgres://pg:5432" {
		t.Errorf("endpoint = %q, want the git source's (nats-kv must be skipped)", vs.Endpoint)
	}
	if embRef != "nomic-embed" || ns != "crew-x" {
		t.Errorf("embRef/ns = %q/%q, want nomic-embed/crew-x", embRef, ns)
	}
}
