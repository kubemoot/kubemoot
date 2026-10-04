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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func skillCR(name, crew string, order int32) *kubemootv1alpha1.Skill {
	return &kubemootv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", Labels: map[string]string{crewLabelKey: crew}},
		Spec:       kubemootv1alpha1.SkillSpec{Description: name + " desc", Content: name + " body", Order: order},
	}
}

// Reconcile adds the finalizer on the first pass (requeue), then on the second
// pass syncs the crew skills ConfigMap and stamps the coordinator pool hash.
func TestSkillReconcile_AddsFinalizerThenSyncs(t *testing.T) {
	scheme := gatewayScheme(t)
	skill := skillCR(testAlpha, testCrewA, 1)
	coord := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: testCoord, Namespace: "ns", Labels: map[string]string{crewLabelKey: testCrewA}},
		Spec:       kubemootv1alpha1.AgentSpec{DiscussRole: roleCoordinator},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(skill, coord).Build()
	r := &SkillReconciler{Client: cli, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: testAlpha, Namespace: "ns"}}

	res, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if !isFinalizerRequeue(res) {
		t.Error("first reconcile should requeue after adding the finalizer")
	}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	cm := &corev1.ConfigMap{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: "crew-crew-a-skills", Namespace: "ns"}, cm); err != nil {
		t.Fatalf("skills ConfigMap not created: %v", err)
	}
	if cm.Data["alpha.txt"] != "alpha body" {
		t.Errorf("skill body not in ConfigMap: %q", cm.Data["alpha.txt"])
	}
	// The coordinator should now carry a pool-hash annotation.
	got := &kubemootv1alpha1.Agent{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: testCoord, Namespace: "ns"}, got); err != nil {
		t.Fatalf("get coordinator: %v", err)
	}
	if got.Annotations["kubemoot.ai/skill-pool-hash"] == "" {
		t.Error("enqueueCoordinators should stamp a pool hash on the coordinator")
	}
}

// marshalResumePayload returns a plain agent array when there are no skills,
// and a heterogeneous RawMessage array (agents + skills) when skills exist.
func TestMarshalResumePayload(t *testing.T) {
	agents := []AgentResume{{Name: testK8sAgent, Role: "specialist"}}
	// No skills -> plain agent array.
	b, err := marshalResumePayload(agents, nil)
	if err != nil {
		t.Fatalf("marshal (no skills): %v", err)
	}
	if len(b) == 0 || b[0] != '[' {
		t.Errorf("expected a JSON array, got %s", b)
	}
	// With skills -> combined array of agents + skills.
	skills := []SkillResume{{Name: "deploy", Kind: testSkill, Order: 1}}
	b2, err := marshalResumePayload(agents, skills)
	if err != nil {
		t.Fatalf("marshal (with skills): %v", err)
	}
	if len(b2) <= len(b) {
		t.Error("the combined payload should be larger than the agents-only one")
	}
}

func TestBuildSkillConfigMapData(t *testing.T) {
	items := []kubemootv1alpha1.Skill{*skillCR("a", "c", 1), *skillCR("b", "c", 2)}
	data := buildSkillConfigMapData(items)
	if data["a.txt"] != "a body" || data["b.txt"] != "b body" {
		t.Errorf("per-skill keys wrong: %v", data)
	}
	if data["skills-index.txt"] == "" {
		t.Error("expected a skills-index.txt summary")
	}
}

func TestConfigMapDataEqual(t *testing.T) {
	a := map[string]string{"k": "v"}
	if !configMapDataEqual(a, map[string]string{"k": "v"}) {
		t.Error("identical maps should be equal")
	}
	if configMapDataEqual(a, map[string]string{"k": "w"}) {
		t.Error("a differing value should not be equal")
	}
	if configMapDataEqual(a, map[string]string{}) {
		t.Error("differing length should not be equal")
	}
}

func TestComputePoolHash(t *testing.T) {
	if h := computePoolHash(nil); h != "0" {
		t.Errorf("empty pool hash should be \"0\", got %q", h)
	}
	items := []kubemootv1alpha1.Skill{*skillCR("a", "c", 1), *skillCR("b", "c", 2)}
	h1 := computePoolHash(items)
	if len(h1) != 16 {
		t.Errorf("pool hash should be 16 hex chars, got %q", h1)
	}
	// Hash is order-independent (sorted by name internally).
	rev := []kubemootv1alpha1.Skill{items[1], items[0]}
	if computePoolHash(rev) != h1 {
		t.Error("pool hash should be independent of input order")
	}
}
