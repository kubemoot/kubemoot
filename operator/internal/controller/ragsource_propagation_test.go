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
	"sort"
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

const ragPropNS = "team-prop"

func mkAgentWithRAG(name string, refs ...string) *kubemootv1alpha1.Agent {
	a := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ragPropNS}}
	for _, r := range refs {
		a.Spec.RAGSources = append(a.Spec.RAGSources, kubemootv1alpha1.RAGSourceRef{Name: r})
	}
	return a
}

func mkRAGSource(name, endpoint string) *kubemootv1alpha1.RAGSource {
	return &kubemootv1alpha1.RAGSource{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ragPropNS},
		Status:     kubemootv1alpha1.RAGSourceStatus{QueryEndpoint: endpoint},
	}
}

// TestRAGSourceEnqueueScopesToReferencingAgents: a RAGSource change enqueues only the
// agents whose ragSources name it; other objects enqueue nothing.
func TestRAGSourceEnqueueScopesToReferencingAgents(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		mkAgentWithRAG("material-writer", "course-book", "syllabus-notes"),
		mkAgentWithRAG("material-checker", "course-book"),
		mkAgentWithRAG(testRoleCoordinator),
	).Build()

	got := mapRAGSourceToAgentRequests(context.Background(), cli, mkRAGSource("course-book", ""))
	names := make([]string, 0, len(got))
	for _, r := range got {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "material-checker" || names[1] != "material-writer" {
		t.Errorf("want [material-checker material-writer], got %v", names)
	}
	if reqs := mapRAGSourceToAgentRequests(context.Background(), cli, mkRAGSource("unreferenced", "")); len(reqs) != 0 {
		t.Errorf("unreferenced RAGSource should enqueue nothing, got %v", reqs)
	}
	if reqs := mapRAGSourceToAgentRequests(context.Background(), cli, mkAgentWithRAG("x")); reqs != nil {
		t.Errorf("non-RAGSource object should enqueue nothing, got %v", reqs)
	}
}

// TestRAGSourcePredicatePassesOnlyEndpointChanges: the RAGSource controller updates
// status often while indexing; only a changed query endpoint or query service spec
// reaches the agents.
func TestRAGSourcePredicatePassesOnlyEndpointChanges(t *testing.T) {
	p := ragSourceEndpointChanged()
	before := mkRAGSource("course-book", "")

	indexing := before.DeepCopy()
	indexing.Status.Phase = crewPhasePending
	indexing.Status.Message = "Indexing job in progress"
	if p.Update(event.UpdateEvent{ObjectOld: before, ObjectNew: indexing}) {
		t.Error("a phase or message change should not pass")
	}

	reported := before.DeepCopy()
	reported.Status.QueryEndpoint = "http://course-book-query.team-prop:8000"
	if !p.Update(event.UpdateEvent{ObjectOld: before, ObjectNew: reported}) {
		t.Error("a new query endpoint should pass")
	}

	off := false
	switchedOff := before.DeepCopy()
	switchedOff.Spec.QueryService = &kubemootv1alpha1.QueryServiceConfig{Enabled: &off}
	if !p.Update(event.UpdateEvent{ObjectOld: before, ObjectNew: switchedOff}) {
		t.Error("a query service change should pass")
	}

	if !p.Create(event.CreateEvent{Object: before}) || !p.Delete(event.DeleteEvent{Object: before}) {
		t.Error("creates and deletes should pass")
	}
	if p.Update(event.UpdateEvent{ObjectOld: mkAgentWithRAG("a"), ObjectNew: mkAgentWithRAG("a")}) {
		t.Error("non-RAGSource updates should not pass")
	}
}

func TestQueryServiceEnabled(t *testing.T) {
	on, off := true, false
	cases := map[string]struct {
		qs   *kubemootv1alpha1.QueryServiceConfig
		want bool
	}{
		"unset spec":     {nil, true},
		"unset enabled":  {&kubemootv1alpha1.QueryServiceConfig{}, true},
		"explicitly on":  {&kubemootv1alpha1.QueryServiceConfig{Enabled: &on}, true},
		"explicitly off": {&kubemootv1alpha1.QueryServiceConfig{Enabled: &off}, false},
	}
	for name, c := range cases {
		rs := &kubemootv1alpha1.RAGSource{Spec: kubemootv1alpha1.RAGSourceSpec{QueryService: c.qs}}
		if got := queryServiceEnabled(rs); got != c.want {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
}
