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
	"strings"
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestRAGSourceEnvRendering verifies Agent.spec.ragSources reaches the runtime as the
// indexed env list SmallRye maps to kubemoot.rag-sources[i] (double underscore after
// the index), with the query endpoint each RAGSource reports or will report.
const (
	ragEnvTestNS = "team-rag"
	ragTextbook  = "textbook"
	ragSyllabus  = "syllabus"
	ragLow       = "notes-a"
	ragLowToo    = "notes-b"
)

func TestRAGSourceEnvRendering(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	reported := &kubemootv1alpha1.RAGSource{
		ObjectMeta: metav1.ObjectMeta{Name: ragTextbook, Namespace: ragEnvTestNS},
		Status:     kubemootv1alpha1.RAGSourceStatus{QueryEndpoint: "http://textbook-query.team-rag:8000"},
	}
	customPort := &kubemootv1alpha1.RAGSource{
		ObjectMeta: metav1.ObjectMeta{Name: ragSyllabus, Namespace: ragEnvTestNS},
		Spec:       kubemootv1alpha1.RAGSourceSpec{QueryService: &kubemootv1alpha1.QueryServiceConfig{Port: 9000}},
	}
	off := false
	disabled := &kubemootv1alpha1.RAGSource{
		ObjectMeta: metav1.ObjectMeta{Name: "offline", Namespace: ragEnvTestNS},
		Spec:       kubemootv1alpha1.RAGSourceSpec{QueryService: &kubemootv1alpha1.QueryServiceConfig{Enabled: &off}},
	}
	objs := []client.Object{reported, customPort, disabled}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	r := &AgentReconciler{Client: cli}
	pick := &modelPick{ModelID: "rag-test-model", Endpoint: "http://ollama.rag-test:11434"}

	agent := func(refs ...kubemootv1alpha1.RAGSourceRef) *kubemootv1alpha1.Agent {
		return &kubemootv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: "writer", Namespace: ragEnvTestNS},
			Spec:       kubemootv1alpha1.AgentSpec{RAGSources: refs},
		}
	}
	render := func(a *kubemootv1alpha1.Agent) []corev1.EnvVar {
		return r.buildEnvVars(context.Background(), a, pick, pick, 8080)
	}
	want := func(t *testing.T, env []corev1.EnvVar, name, value string) {
		t.Helper()
		if v, ok := envValue(env, name); !ok || v != value {
			t.Fatalf("want %s=%q, got %q present=%v", name, value, v, ok)
		}
	}
	absent := func(t *testing.T, env []corev1.EnvVar, name string) {
		t.Helper()
		if v, ok := envValue(env, name); ok {
			t.Fatalf("want %s absent, got %q", name, v)
		}
	}

	t.Run("reported query endpoint is used", func(t *testing.T) {
		env := render(agent(kubemootv1alpha1.RAGSourceRef{Name: ragTextbook, TopK: 3}))
		want(t, env, "KUBEMOOT_RAG_SOURCES_0__NAME", ragTextbook)
		want(t, env, "KUBEMOOT_RAG_SOURCES_0__ENDPOINT", "http://textbook-query.team-rag:8000")
		want(t, env, "KUBEMOOT_RAG_SOURCES_0__TOP_K", "3")
	})

	t.Run("unreconciled RAGSource gets the endpoint its controller will create", func(t *testing.T) {
		env := render(agent(kubemootv1alpha1.RAGSourceRef{Name: ragSyllabus}))
		want(t, env, "KUBEMOOT_RAG_SOURCES_0__ENDPOINT", "http://syllabus-query.team-rag:9000")
	})

	t.Run("RAGSource not yet applied is still wired at the default port", func(t *testing.T) {
		env := render(agent(kubemootv1alpha1.RAGSourceRef{Name: "later", TopK: 2}))
		want(t, env, "KUBEMOOT_RAG_SOURCES_0__NAME", "later")
		want(t, env, "KUBEMOOT_RAG_SOURCES_0__ENDPOINT", "http://later-query.team-rag:8000")
	})

	t.Run("topK 0 emits no TOP_K so the runtime default applies", func(t *testing.T) {
		env := render(agent(kubemootv1alpha1.RAGSourceRef{Name: ragTextbook}))
		absent(t, env, "KUBEMOOT_RAG_SOURCES_0__TOP_K")
	})

	t.Run("higher priority comes first; equals keep their declared order", func(t *testing.T) {
		env := render(agent(
			kubemootv1alpha1.RAGSourceRef{Name: ragLow, TopK: 1},
			kubemootv1alpha1.RAGSourceRef{Name: ragSyllabus, Priority: 5},
			kubemootv1alpha1.RAGSourceRef{Name: ragLowToo, TopK: 1},
		))
		want(t, env, "KUBEMOOT_RAG_SOURCES_0__NAME", ragSyllabus)
		want(t, env, "KUBEMOOT_RAG_SOURCES_1__NAME", ragLow)
		want(t, env, "KUBEMOOT_RAG_SOURCES_2__NAME", ragLowToo)
	})

	t.Run("a RAGSource with its query service off is left out without a gap", func(t *testing.T) {
		env := render(agent(
			kubemootv1alpha1.RAGSourceRef{Name: "offline", TopK: 3},
			kubemootv1alpha1.RAGSourceRef{Name: ragTextbook, TopK: 3},
		))
		want(t, env, "KUBEMOOT_RAG_SOURCES_0__NAME", ragTextbook)
		absent(t, env, "KUBEMOOT_RAG_SOURCES_1__NAME")
	})

	t.Run("two references keep their order as index 0 and 1", func(t *testing.T) {
		env := render(agent(
			kubemootv1alpha1.RAGSourceRef{Name: ragTextbook, TopK: 3},
			kubemootv1alpha1.RAGSourceRef{Name: ragSyllabus, TopK: 1},
		))
		want(t, env, "KUBEMOOT_RAG_SOURCES_0__NAME", ragTextbook)
		want(t, env, "KUBEMOOT_RAG_SOURCES_1__NAME", ragSyllabus)
		want(t, env, "KUBEMOOT_RAG_SOURCES_1__TOP_K", "1")
	})

	t.Run("no references emit no RAG env", func(t *testing.T) {
		for _, e := range render(agent()) {
			if strings.HasPrefix(e.Name, "KUBEMOOT_RAG_SOURCES") {
				t.Fatalf("unexpected %s", e.Name)
			}
		}
	})

	t.Run("the single-underscore spelling SmallRye ignores is never emitted", func(t *testing.T) {
		env := render(agent(kubemootv1alpha1.RAGSourceRef{Name: ragTextbook, TopK: 3}))
		absent(t, env, "KUBEMOOT_RAG_SOURCES_0_NAME")
	})
}

func TestQueryServiceEndpointHelpers(t *testing.T) {
	if got := queryServicePort(&kubemootv1alpha1.RAGSource{}); got != defaultQueryServicePort {
		t.Fatalf("default port: got %d", got)
	}
	rs := &kubemootv1alpha1.RAGSource{Spec: kubemootv1alpha1.RAGSourceSpec{QueryService: &kubemootv1alpha1.QueryServiceConfig{Port: 8123}}}
	if got := queryServicePort(rs); got != 8123 {
		t.Fatalf("custom port: got %d", got)
	}
	if got := ragQueryEndpoint("docs", "ns", 8000); got != "http://docs-query.ns:8000" {
		t.Fatalf("endpoint: got %q", got)
	}
}
