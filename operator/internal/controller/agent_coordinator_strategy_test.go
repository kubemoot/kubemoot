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

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestCoordinatorUsesRecreateStrategy verifies that a coordinator agent's
// Deployment rolls with Recreate, so the old and new pod never overlap and
// fight over the crew's single durable request consumer (the 409 Consumer
// Deleted churn). A non-coordinator agent keeps the default strategy.
func TestCoordinatorUsesRecreateStrategy(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &AgentReconciler{Client: cli}
	pick := &modelPick{ModelID: "qwen3:8b", Endpoint: "http://ollama:11434"}

	coord := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "homelab-coordinator", Namespace: "crew-x"},
		Spec:       kubemootv1alpha1.AgentSpec{DiscussRole: "coordinator"},
	}
	d := r.buildDeployment(context.Background(), coord, pick, pick, "coordinator-policy", "h")
	if d.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Errorf("coordinator strategy: got %q, want Recreate", d.Spec.Strategy.Type)
	}

	tooler := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "k8s-nodes", Namespace: "crew-x"},
		Spec:       kubemootv1alpha1.AgentSpec{DiscussRole: "tooler"},
	}
	dt := r.buildDeployment(context.Background(), tooler, pick, pick, "tooler-policy", "h")
	if dt.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType {
		t.Error("non-coordinator must not use Recreate strategy; expected default RollingUpdate")
	}
}
