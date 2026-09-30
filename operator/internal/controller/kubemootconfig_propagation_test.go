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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// newPropagationClient builds a controller-runtime fake client preloaded
// with the v1alpha1 scheme and the given seed objects.
func newPropagationClient(t *testing.T, seed ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(seed...).Build()
}

// TestMapKubemootConfigToRequests_EnqueuesEveryAgent verifies the core
// propagation path: a KubemootConfig change on the singleton triggers
// reconcile requests for every Agent CR in the cluster. Pre-fix, an
// image bump in the singleton would update ConfigCache but no Agent
// would re-reconcile, leaving Deployments on the old image.
func TestMapKubemootConfigToRequests_EnqueuesEveryAgent(t *testing.T) {
	a1 := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "a1", Namespace: "ns1"}}
	a2 := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "a2", Namespace: "ns2"}}
	a3 := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "a3", Namespace: "ns2"}}
	cli := newPropagationClient(t, a1, a2, a3)

	cfg := &kubemootv1alpha1.KubemootConfig{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultKubemootConfigName},
	}
	got := mapKubemootConfigToRequests(context.Background(), cli,
		func() client.ObjectList { return &kubemootv1alpha1.AgentList{} },
		"agent", cfg)

	if len(got) != 3 {
		t.Fatalf("expected 3 reconcile.Requests, got %d: %v", len(got), got)
	}
	wantNamespaces := map[string]string{"a1": "ns1", "a2": "ns2", "a3": "ns2"}
	for _, r := range got {
		ns, ok := wantNamespaces[r.Name]
		if !ok {
			t.Errorf("unexpected request name %q", r.Name)
			continue
		}
		if r.Namespace != ns {
			t.Errorf("request for %s wanted namespace %s, got %s", r.Name, ns, r.Namespace)
		}
	}
}

// TestMapKubemootConfigToRequests_IgnoresNonSingleton guards against
// accidental fleet-wide reconciles if a user creates a KubemootConfig
// with a non-default name (which shouldn't be done but must not blow
// up the cluster if it is).
func TestMapKubemootConfigToRequests_IgnoresNonSingleton(t *testing.T) {
	a := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"}}
	cli := newPropagationClient(t, a)

	stranger := &kubemootv1alpha1.KubemootConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "experimental"},
	}
	got := mapKubemootConfigToRequests(context.Background(), cli,
		func() client.ObjectList { return &kubemootv1alpha1.AgentList{} },
		"agent", stranger)

	if len(got) != 0 {
		t.Errorf("non-singleton KubemootConfig should not enqueue requests, got %d", len(got))
	}
}

// TestMapKubemootConfigToRequests_IgnoresWrongObjectType guards against
// the handler being inadvertently wired to watch a different object
// type. A typo at registration shouldn't trigger reconciles for
// unrelated objects.
func TestMapKubemootConfigToRequests_IgnoresWrongObjectType(t *testing.T) {
	a := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"}}
	cli := newPropagationClient(t, a)

	// A different CR type — should be ignored entirely.
	wrong := &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultKubemootConfigName, Namespace: "ns"},
	}
	got := mapKubemootConfigToRequests(context.Background(), cli,
		func() client.ObjectList { return &kubemootv1alpha1.AgentList{} },
		"agent", wrong)

	if len(got) != 0 {
		t.Errorf("non-KubemootConfig object should not enqueue requests, got %d", len(got))
	}
}

// TestMapKubemootConfigToRequests_EmptyListNoCrash verifies the
// graceful path when no CRs exist yet (fresh cluster, before any Agent
// is created). Returns zero requests, no panic.
func TestMapKubemootConfigToRequests_EmptyListNoCrash(t *testing.T) {
	cli := newPropagationClient(t)

	cfg := &kubemootv1alpha1.KubemootConfig{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultKubemootConfigName},
	}
	got := mapKubemootConfigToRequests(context.Background(), cli,
		func() client.ObjectList { return &kubemootv1alpha1.AgentList{} },
		"agent", cfg)

	if len(got) != 0 {
		t.Errorf("expected 0 requests for empty cluster, got %d", len(got))
	}
}

// TestMapKubemootConfigToRequests_WorksForEveryConsumerListType is a
// smoke check that the same mapping works with every List type the six
// consuming controllers wire up. Catches any List that isn't a proper
// meta.List (which would fail ExtractList silently otherwise).
func TestMapKubemootConfigToRequests_WorksForEveryConsumerListType(t *testing.T) {
	cases := []struct {
		name    string
		seed    client.Object
		factory func() client.ObjectList
	}{
		{"agent", &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"}},
			func() client.ObjectList { return &kubemootv1alpha1.AgentList{} }},
		{"mcpserver", &kubemootv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"}},
			func() client.ObjectList { return &kubemootv1alpha1.MCPServerList{} }},
		{"mcpgateway", &kubemootv1alpha1.MCPGateway{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"}},
			func() client.ObjectList { return &kubemootv1alpha1.MCPGatewayList{} }},
		{"ragsource", &kubemootv1alpha1.RAGSource{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"}},
			func() client.ObjectList { return &kubemootv1alpha1.RAGSourceList{} }},
		{"crewfitness", &kubemootv1alpha1.CrewFitness{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"}},
			func() client.ObjectList { return &kubemootv1alpha1.CrewFitnessList{} }},
		{"crew", &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"}},
			func() client.ObjectList { return &kubemootv1alpha1.CrewList{} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cli := newPropagationClient(t, c.seed)
			cfg := &kubemootv1alpha1.KubemootConfig{
				ObjectMeta: metav1.ObjectMeta{Name: DefaultKubemootConfigName},
			}
			got := mapKubemootConfigToRequests(context.Background(), cli,
				c.factory, c.name, cfg)
			if len(got) != 1 {
				t.Errorf("%s: expected 1 request for seeded CR, got %d", c.name, len(got))
			}
		})
	}
}
