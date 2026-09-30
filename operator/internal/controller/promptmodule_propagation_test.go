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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func mkAgentWithPrompts(name string, refs ...string) *kubemootv1alpha1.Agent {
	return &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "crew-x"},
		Spec:       kubemootv1alpha1.AgentSpec{PromptRefs: refs},
	}
}

// TestPromptModuleEnqueueScopesToReferencingAgents: a PromptModule change
// enqueues only the agents whose promptRefs include it, a non-PromptModule
// object enqueues nothing.
func TestPromptModuleEnqueueScopesToReferencingAgents(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		mkAgentWithPrompts("k8s-advisor", "analyst-review-protocol", "discussion-protocol"),
		mkAgentWithPrompts("proxmox-advisor", "analyst-review-protocol"),
		mkAgentWithPrompts("coordinator", "synthesis-prompt"),
	).Build()

	got := mapPromptModuleToAgentRequests(context.Background(), cli, &kubemootv1alpha1.PromptModule{
		ObjectMeta: metav1.ObjectMeta{Name: "analyst-review-protocol", Namespace: "crew-x"},
	})
	names := make([]string, 0, len(got))
	for _, r := range got {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "k8s-advisor" || names[1] != "proxmox-advisor" {
		t.Errorf("expected [k8s-advisor proxmox-advisor] (refs analyst-review-protocol), got %v", names)
	}

	// A PromptModule no agent references → no requests.
	if reqs := mapPromptModuleToAgentRequests(context.Background(), cli, &kubemootv1alpha1.PromptModule{
		ObjectMeta: metav1.ObjectMeta{Name: "unreferenced", Namespace: "crew-x"},
	}); len(reqs) != 0 {
		t.Errorf("unreferenced PromptModule should enqueue nothing, got %v", reqs)
	}

	// Non-PromptModule object → nil.
	if reqs := mapPromptModuleToAgentRequests(context.Background(), cli, mkAgentWithPrompts("a", "crew-x")); reqs != nil {
		t.Errorf("non-PromptModule object should enqueue nothing, got %v", reqs)
	}
}

// TestComputeDeploymentHashIncludesPromptHash: a change to the prompt-hash pod
// template annotation changes the deployment hash, so a PromptModule-only edit
// triggers a Deployment update and rolls the pod.
func TestComputeDeploymentHashIncludesPromptHash(t *testing.T) {
	mk := func(promptHash string) *appsv1.Deployment {
		return &appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{promptHashAnnotation: promptHash},
					},
				},
			},
		}
	}
	a := computeDeploymentHash(mk("aaaa"))
	b := computeDeploymentHash(mk("bbbb"))
	if a == b {
		t.Errorf("deployment hash should differ when prompt hash differs, both %q", a)
	}
	if a2 := computeDeploymentHash(mk("aaaa")); a != a2 {
		t.Errorf("deployment hash should be stable for same prompt hash, %q vs %q", a, a2)
	}
}

// TestHashStringDeterministic: hashString is stable and distinguishes inputs.
func TestHashStringDeterministic(t *testing.T) {
	first := hashString("x")
	if first != hashString("x") {
		t.Error("hashString must be deterministic")
	}
	if first == hashString("y") {
		t.Error("hashString must distinguish different inputs")
	}
}
