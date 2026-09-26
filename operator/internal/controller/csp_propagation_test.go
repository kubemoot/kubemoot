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

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func mkAgent(name, ns, crew string) *kubemootv1alpha1.Agent {
	return &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{"kubemoot.ai/crew": crew},
		},
	}
}

// TestCSPEnqueueScopesToCrew: a CSP change enqueues only its crew's agents (so
// an edited triage prefer block re-binds the right agents), a non-CSP object
// enqueues nothing, and empty crewRef matches all agents in the namespace.
func TestCSPEnqueueScopesToCrew(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		mkAgent("coordinator", "crew-x", "x"),
		mkAgent("k8s", "crew-x", "x"),
		mkAgent("other", "crew-x", "y"),
	).Build()

	got := mapCSPToAgentRequests(context.Background(), cli, &kubemootv1alpha1.CrewSchedulingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "x-scheduling", Namespace: "crew-x"},
		Spec:       kubemootv1alpha1.CrewSchedulingPolicySpec{CrewRef: "x"},
	})
	names := make([]string, 0, len(got))
	for _, r := range got {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "coordinator" || names[1] != "k8s" {
		t.Errorf("expected [coordinator k8s] (crew x only), got %v", names)
	}

	// Non-CSP object → no requests.
	if reqs := mapCSPToAgentRequests(context.Background(), cli, mkAgent("a", "crew-x", "x")); reqs != nil {
		t.Errorf("non-CSP object should enqueue nothing, got %v", reqs)
	}

	// Empty crewRef → all agents in the namespace.
	all := mapCSPToAgentRequests(context.Background(), cli, &kubemootv1alpha1.CrewSchedulingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "all", Namespace: "crew-x"},
		Spec:       kubemootv1alpha1.CrewSchedulingPolicySpec{CrewRef: ""},
	})
	if len(all) != 3 {
		t.Errorf("empty crewRef should enqueue all 3 agents, got %d", len(all))
	}
}
