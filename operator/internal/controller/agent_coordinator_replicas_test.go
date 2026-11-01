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
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// replicasFixture is a reconciler in a realm with the given coordinator block, and an Agent in it.
func replicasFixture(t *testing.T, realm *kubemootv1alpha1.CoordinatorConfig, role string, replicas int32) (*AgentReconciler, *kubemootv1alpha1.Agent) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	cache := NewConfigCache()
	cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{Coordinator: realm}})
	agent := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "coordinator", Namespace: testCrewNamespace, Generation: 3},
		Spec: kubemootv1alpha1.AgentSpec{DiscussRole: role,
			Deployment: &kubemootv1alpha1.DeploymentConfig{Replicas: replicas}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent).
		WithStatusSubresource(&kubemootv1alpha1.Agent{}).Build()
	return &AgentReconciler{Client: c, ConfigCache: cache}, agent
}

func deploymentFor(r *AgentReconciler, agent *kubemootv1alpha1.Agent) *appsv1.Deployment {
	pick := &modelPick{ModelID: testModelID, Endpoint: testOllamaURL}
	return r.buildDeployment(context.Background(), agent, pick, pick, "policy", "h")
}

func storedCondition(t *testing.T, r *AgentReconciler, agent *kubemootv1alpha1.Agent) *metav1.Condition {
	t.Helper()
	stored := &kubemootv1alpha1.Agent{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: agent.Namespace, Name: agent.Name}, stored); err != nil {
		t.Fatalf("get agent: %v", err)
	}
	return meta.FindStatusCondition(stored.Status.Conditions, ConditionReplicasLimited)
}

func TestCoordinatorWithLeasesOffRunsOneReplicaWithRecreateWhateverTheSpecAsks(t *testing.T) {
	for name, realm := range leasesOffRealms() {
		r, agent := replicasFixture(t, realm, roleCoordinator, 3)

		d := deploymentFor(r, agent)

		if *d.Spec.Replicas != 1 {
			t.Errorf("%s: replicas %d, want 1", name, *d.Spec.Replicas)
		}
		if d.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
			t.Errorf("%s: strategy %q, want Recreate", name, d.Spec.Strategy.Type)
		}
	}
}

// sidecarCoordinator is a coordinator Agent in sidecar harness mode asking for 3 replicas.
func sidecarCoordinator(realm *kubemootv1alpha1.CoordinatorConfig) (*AgentReconciler, *kubemootv1alpha1.Agent) {
	cache := NewConfigCache()
	cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{Coordinator: realm}})
	agent := sidecarAgent()
	agent.Spec.DiscussRole = roleCoordinator
	agent.Spec.Deployment = &kubemootv1alpha1.DeploymentConfig{Replicas: 3}
	return &AgentReconciler{ConfigCache: cache}, agent
}

func TestSidecarCoordinatorIsHeldToOneReplicaAndRecreateWhenLeasesAreOff(t *testing.T) {
	for name, realm := range leasesOffRealms() {
		r, agent := sidecarCoordinator(realm)

		d, err := r.buildSidecarDeployment(context.Background(), agent, harnessPick(testModelID, "rig0"),
			harnessPick("qwen3:4b", "rig1"), "policy", "h1")

		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if *d.Spec.Replicas != 1 || d.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
			t.Errorf("%s: replicas %d strategy %q, want 1 and Recreate", name, *d.Spec.Replicas, d.Spec.Strategy.Type)
		}
	}
}

func TestSidecarCoordinatorKeepsItsReplicasAndRollsWithRollingUpdateWhenLeasesAreOn(t *testing.T) {
	r, agent := sidecarCoordinator(leasesOn())

	d, err := r.buildSidecarDeployment(context.Background(), agent, harnessPick(testModelID, "rig0"),
		harnessPick("qwen3:4b", "rig1"), "policy", "h1")

	if err != nil {
		t.Fatal(err)
	}
	if *d.Spec.Replicas != 3 || d.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType {
		t.Errorf("replicas %d strategy %q, want 3 and RollingUpdate", *d.Spec.Replicas, d.Spec.Strategy.Type)
	}
}

func TestCoordinatorWithLeasesOffRecordsWhyItsReplicasWereLimited(t *testing.T) {
	r, agent := replicasFixture(t, nil, roleCoordinator, 3)

	if err := r.syncReplicasLimitedCondition(context.Background(), agent); err != nil {
		t.Fatalf("sync: %v", err)
	}

	cond := storedCondition(t, r, agent)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != reasonLeasesOff {
		t.Fatalf("want a true %s condition, got %+v", ConditionReplicasLimited, cond)
	}
	if !strings.Contains(cond.Message, "3") || !strings.Contains(cond.Message, "leases are off") {
		t.Errorf("the message must say what was asked and why it was limited: %q", cond.Message)
	}
	if cond.ObservedGeneration != 3 {
		t.Errorf("observed generation %d, want 3", cond.ObservedGeneration)
	}
}

func TestCoordinatorWithLeasesOnKeepsItsReplicasAndRollingUpdate(t *testing.T) {
	r, agent := replicasFixture(t, leasesOn(), roleCoordinator, 3)

	d := deploymentFor(r, agent)

	if *d.Spec.Replicas != 3 {
		t.Errorf("replicas %d, want 3", *d.Spec.Replicas)
	}
	if d.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType {
		t.Errorf("strategy %q, want RollingUpdate", d.Spec.Strategy.Type)
	}
	if err := r.syncReplicasLimitedCondition(context.Background(), agent); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if cond := storedCondition(t, r, agent); cond != nil {
		t.Errorf("nothing was limited, so no condition: %+v", cond)
	}
}

func TestReplicasLimitedConditionClearsWhenLeasesAreTurnedOnOrTheSpecIsHonored(t *testing.T) {
	r, agent := replicasFixture(t, nil, roleCoordinator, 3)
	if err := r.syncReplicasLimitedCondition(context.Background(), agent); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if storedCondition(t, r, agent) == nil {
		t.Fatal("the condition was not set")
	}

	r.ConfigCache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{Coordinator: leasesOn()}})
	if err := r.syncReplicasLimitedCondition(context.Background(), agent); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if cond := storedCondition(t, r, agent); cond != nil {
		t.Errorf("leases on: the condition must go, got %+v", cond)
	}
}

func TestACoordinatorThatAsksForOneReplicaOrNoneIsNotReportedAsLimited(t *testing.T) {
	for _, asked := range []int32{0, 1} {
		r, agent := replicasFixture(t, nil, roleCoordinator, asked)
		if err := r.syncReplicasLimitedCondition(context.Background(), agent); err != nil {
			t.Fatalf("sync: %v", err)
		}
		if cond := storedCondition(t, r, agent); cond != nil {
			t.Errorf("asked %d: nothing was limited, got %+v", asked, cond)
		}
		if got := *deploymentFor(r, agent).Spec.Replicas; got != 1 {
			t.Errorf("asked %d: replicas %d, want 1", asked, got)
		}
	}
}

func TestOtherAgentsKeepTheirReplicasWhateverTheLeaseSetting(t *testing.T) {
	r, agent := replicasFixture(t, nil, testRoleTooler, 3)

	if got := *deploymentFor(r, agent).Spec.Replicas; got != 3 {
		t.Errorf("a tooler asked for 3 and got %d", got)
	}
	if err := r.syncReplicasLimitedCondition(context.Background(), agent); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if cond := storedCondition(t, r, agent); cond != nil {
		t.Errorf("a tooler is never limited: %+v", cond)
	}
}
