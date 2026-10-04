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

// envValue returns the value of the named env var, or ("", false) if absent.
func envValue(env []corev1.EnvVar, name string) (string, bool) {
	for _, e := range env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// TestThinkEnvRendering verifies the generic per-agent thinking control:
// Agent.Spec.Think maps to KUBEMOOT_MODEL_THINK only when explicitly set, so
// an unset Think leaves the model/family default rather than forcing a value.
func TestThinkEnvRendering(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &AgentReconciler{Client: cli}
	pick := &modelPick{ModelID: testModelID, Endpoint: testOllamaURL}

	mkAgent := func(think *bool) *kubemootv1alpha1.Agent {
		return &kubemootv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: testCrewNamespace},
			Spec:       kubemootv1alpha1.AgentSpec{Think: think},
		}
	}

	tru, fls := true, false

	t.Run("think=false renders KUBEMOOT_MODEL_THINK=false", func(t *testing.T) {
		env := r.buildEnvVars(context.Background(), mkAgent(&fls), pick, pick, 8080)
		v, ok := envValue(env, "KUBEMOOT_MODEL_THINK")
		if !ok || v != testFalse {
			t.Fatalf("want KUBEMOOT_MODEL_THINK=false, got %q present=%v", v, ok)
		}
	})

	t.Run("think=true renders KUBEMOOT_MODEL_THINK=true", func(t *testing.T) {
		env := r.buildEnvVars(context.Background(), mkAgent(&tru), pick, pick, 8080)
		v, ok := envValue(env, "KUBEMOOT_MODEL_THINK")
		if !ok || v != testTrue {
			t.Fatalf("want KUBEMOOT_MODEL_THINK=true, got %q present=%v", v, ok)
		}
	})

	t.Run("think unset omits KUBEMOOT_MODEL_THINK", func(t *testing.T) {
		env := r.buildEnvVars(context.Background(), mkAgent(nil), pick, pick, 8080)
		if v, ok := envValue(env, "KUBEMOOT_MODEL_THINK"); ok {
			t.Fatalf("want KUBEMOOT_MODEL_THINK absent, got %q", v)
		}
	})
}

// TestCrewHasAnalysts verifies the namespace-scoped analyst detection that
// drives whether the coordinator runs the REVIEW phase.
func TestCrewHasAnalysts(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	agent := func(name, ns, role string) *kubemootv1alpha1.Agent {
		return &kubemootv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       kubemootv1alpha1.AgentSpec{DiscussRole: role},
		}
	}

	t.Run("true when an analyst is present in the namespace", func(t *testing.T) {
		cli := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(agent("c", testCrewNamespace, testRoleCoordinator), agent(testK8s, testCrewNamespace, ""), agent("k8s-analyst", testCrewNamespace, "analyst")).
			Build()
		r := &AgentReconciler{Client: cli}
		if !r.crewHasAnalysts(context.Background(), testCrewNamespace) {
			t.Fatal("want true when an analyst exists in the namespace")
		}
	})

	t.Run("false when no analyst in the namespace", func(t *testing.T) {
		cli := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(agent("c", testCrewNamespace, testRoleCoordinator), agent(testK8s, testCrewNamespace, "")).
			Build()
		r := &AgentReconciler{Client: cli}
		if r.crewHasAnalysts(context.Background(), testCrewNamespace) {
			t.Fatal("want false when no analyst exists")
		}
	})

	t.Run("scoped to the namespace (analyst in another crew does not count)", func(t *testing.T) {
		cli := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(agent("c", testCrewNamespace, testRoleCoordinator), agent("t", "crew-y", "analyst")).
			Build()
		r := &AgentReconciler{Client: cli}
		if r.crewHasAnalysts(context.Background(), testCrewNamespace) {
			t.Fatal("a analyst in crew-y must not count for crew-x")
		}
	})
}

// TestCoordinatorHasAnalystsEnv verifies the coordinator gets
// KUBEMOOT_DISCUSS_HAS_ANALYSTS only when its crew declares an analyst.
func TestCoordinatorHasAnalystsEnv(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	pick := &modelPick{ModelID: testModel32B, Endpoint: testOllamaURL}
	coord := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: testRoleCoordinator, Namespace: testCrewNamespace,
			Labels: map[string]string{"kubemoot.ai/crew": testCrewName}},
		Spec: kubemootv1alpha1.AgentSpec{DiscussRole: testRoleCoordinator},
	}
	analyst := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "k8s-analyst", Namespace: testCrewNamespace},
		Spec:       kubemootv1alpha1.AgentSpec{DiscussRole: "analyst"},
	}

	t.Run("present with an analyst in the crew", func(t *testing.T) {
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(coord, analyst).Build()
		r := &AgentReconciler{Client: cli}
		env := r.buildEnvVars(context.Background(), coord, pick, pick, 8080)
		if v, ok := envValue(env, "KUBEMOOT_DISCUSS_HAS_ANALYSTS"); !ok || v != testTrue {
			t.Fatalf("want KUBEMOOT_DISCUSS_HAS_ANALYSTS=true, got %q present=%v", v, ok)
		}
	})

	t.Run("absent with no analyst in the crew", func(t *testing.T) {
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(coord).Build()
		r := &AgentReconciler{Client: cli}
		env := r.buildEnvVars(context.Background(), coord, pick, pick, 8080)
		if v, ok := envValue(env, "KUBEMOOT_DISCUSS_HAS_ANALYSTS"); ok {
			t.Fatalf("want KUBEMOOT_DISCUSS_HAS_ANALYSTS absent, got %q", v)
		}
	})
}

// TestCrewVersionEnvRendering verifies the crew chart version (provenance) flows
// from the crew chart's kubemoot.ai/crew-version label into KUBEMOOT_CREW_VERSION,
// and is omitted for hand-applied crews that carry no chart version.
func TestCrewVersionEnvRendering(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &AgentReconciler{Client: cli}
	pick := &modelPick{ModelID: testModelID, Endpoint: testOllamaURL}

	t.Run("crew-version label renders KUBEMOOT_CREW_VERSION", func(t *testing.T) {
		agent := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{
			Name: "a", Namespace: testCrewNamespace,
			Labels: map[string]string{crewVersionLabel: testVersion142},
		}}
		env := r.buildEnvVars(context.Background(), agent, pick, pick, 8080)
		if v, ok := envValue(env, "KUBEMOOT_CREW_VERSION"); !ok || v != testVersion142 {
			t.Fatalf("want KUBEMOOT_CREW_VERSION=1.4.2, got %q present=%v", v, ok)
		}
	})

	t.Run("no label omits KUBEMOOT_CREW_VERSION", func(t *testing.T) {
		agent := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: testCrewNamespace}}
		env := r.buildEnvVars(context.Background(), agent, pick, pick, 8080)
		if _, ok := envValue(env, "KUBEMOOT_CREW_VERSION"); ok {
			t.Fatalf("KUBEMOOT_CREW_VERSION should be absent for a crew with no chart version")
		}
	})
}
