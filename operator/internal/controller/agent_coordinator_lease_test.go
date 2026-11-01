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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// leasesOffRealms are the realms in which coordinator leases do not count, by what is missing.
func leasesOffRealms() map[string]*kubemootv1alpha1.CoordinatorConfig {
	return map[string]*kubemootv1alpha1.CoordinatorConfig{
		"no coordinator block":  nil,
		"lease off":             {EventLog: true, Resume: true},
		"lease without resume":  {EventLog: true, Lease: true},
		"lease without the log": {Resume: true, Lease: true},
	}
}

// leasesOn is a realm with the log, resume and leases all on.
func leasesOn() *kubemootv1alpha1.CoordinatorConfig {
	return &kubemootv1alpha1.CoordinatorConfig{EventLog: true, Resume: true, Lease: true}
}

func TestCoordinatorLeaseEnv_AbsentUnlessTheRealmTurnsItOnWithResumeAndTheLog(t *testing.T) {
	for name, config := range leasesOffRealms() {
		if got := coordinatorEnvValues(t, envCoordinatorLease, config); len(got) != 0 {
			t.Errorf("%s: want no variable, got %v", name, got)
		}
	}
}

func TestCoordinatorLeaseEnv_SetOnTheCoordinatorWhenTheRealmTurnsItOn(t *testing.T) {
	got := coordinatorEnvValues(t, envCoordinatorLease, leasesOn())
	if len(got) != 1 || got[0] != valueTrue {
		t.Fatalf("want KUBEMOOT_COORDINATOR_LEASE=true once, got %v", got)
	}
}

func TestCoordinatorLeaseEnv_TheAgentsOwnValueComesLastAndWins(t *testing.T) {
	got := coordinatorEnvValues(t, envCoordinatorLease, leasesOn(),
		corev1.EnvVar{Name: envCoordinatorLease, Value: valueFalse})
	if len(got) != 2 || got[len(got)-1] != valueFalse {
		t.Fatalf("the Agent's own env must follow the realm's so it wins, got %v", got)
	}
}

func TestConfigCache_CoordinatorLease(t *testing.T) {
	cache := NewConfigCache()
	if cache.GetCoordinatorLease() {
		t.Fatal("an empty cache must report leases off")
	}
	cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{Coordinator: leasesOn()}})
	if !cache.GetCoordinatorLease() {
		t.Fatal("lease with resume and the log must be reported")
	}
	cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{
		Coordinator: &kubemootv1alpha1.CoordinatorConfig{Lease: true}}})
	if cache.GetCoordinatorLease() {
		t.Fatal("lease without resume and the log must not be reported")
	}
}

// TestCoordinatorRollsWithRollingUpdateWhenLeasesAreOn verifies that the realm's coordinator
// lease setting (with resume and the log) lets a coordinator roll with RollingUpdate, that a
// realm without it keeps Recreate, and that a changed strategy reconciles the live Deployment.
func TestCoordinatorRollsWithRollingUpdateWhenLeasesAreOn(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	pick := &modelPick{ModelID: testModelID, Endpoint: testOllamaURL}
	coord := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: testHomelabCoordinator, Namespace: testCrewNamespace},
		Spec:       kubemootv1alpha1.AgentSpec{DiscussRole: testRoleCoordinator},
	}
	build := func(coordinator *kubemootv1alpha1.CoordinatorConfig) *appsv1.Deployment {
		cache := NewConfigCache()
		cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{Coordinator: coordinator}})
		r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), ConfigCache: cache}
		return r.buildDeployment(context.Background(), coord, pick, pick, "coordinator-policy", "h")
	}

	withLeases := build(leasesOn())
	if withLeases.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType {
		t.Errorf("leases on: got %q, want RollingUpdate", withLeases.Spec.Strategy.Type)
	}
	for name, config := range leasesOffRealms() {
		if got := build(config).Spec.Strategy.Type; got != appsv1.RecreateDeploymentStrategyType {
			t.Errorf("%s: got %q, want Recreate", name, got)
		}
	}

	recreating := build(nil)
	if !deploymentNeedsUpdate(recreating, withLeases, withLeases.Annotations[deploymentHashAnnotation]) {
		t.Error("a coordinator Deployment whose strategy changed must be updated")
	}
}

// TestCoordinatorReplicasFollowTheAgentWhenLeasesAreOn verifies, with leases on, that the operator neither
// raises nor caps a coordinator's replicas: the Agent states them, one by default.
func TestCoordinatorReplicasFollowTheAgentWhenLeasesAreOn(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	pick := &modelPick{ModelID: testModelID, Endpoint: testOllamaURL}
	cache := NewConfigCache()
	cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{Coordinator: leasesOn()}})
	r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), ConfigCache: cache}

	one := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: testHomelabCoordinator, Namespace: testCrewNamespace},
		Spec:       kubemootv1alpha1.AgentSpec{DiscussRole: testRoleCoordinator},
	}
	if got := *r.buildDeployment(context.Background(), one, pick, pick, "p", "h").Spec.Replicas; got != 1 {
		t.Errorf("default replicas: got %d, want 1", got)
	}
	three := one.DeepCopy()
	three.Spec.Deployment = &kubemootv1alpha1.DeploymentConfig{Replicas: 3}
	if got := *r.buildDeployment(context.Background(), three, pick, pick, "p", "h").Spec.Replicas; got != 3 {
		t.Errorf("replicas 3: got %d, want 3", got)
	}
}
