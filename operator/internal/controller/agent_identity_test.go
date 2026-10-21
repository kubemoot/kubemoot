/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	identityRole        = "tooler"
	identityChannel     = "general"
	identityHarnessName = "goose"
)

func identityAgent(role string) *kubemootv1alpha1.Agent {
	return &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns", Labels: map[string]string{labelCrew: "c"}},
		Spec: kubemootv1alpha1.AgentSpec{DiscussRole: role, DiscussChannels: []string{identityChannel},
			Harness: &kubemootv1alpha1.HarnessSpec{Mode: kubemootv1alpha1.HarnessModeSidecar, Image: "h:1", Name: identityHarnessName}},
	}
}

func TestAgentIdentityNamesTheConsumerAndTheHarness(t *testing.T) {
	id := agentIdentity(identityAgent(identityRole))
	if id.ConsumerName() != "sidecar-ns-c-a" || id.Harness != identityHarnessName || len(id.DiscussFilters()) != 2 {
		t.Fatalf("%+v", id)
	}
	legacy := identityAgent(identityRole)
	legacy.Spec.Harness = nil
	if agentIdentity(legacy).Harness != "" {
		t.Error("an agent with no harness block has no harness name")
	}
}

func TestACoordinatorCannotRunInSidecarMode(t *testing.T) {
	if err := validateSidecarAgent(identityAgent(identityRole)); err != nil {
		t.Fatalf("a tooler is valid: %v", err)
	}
	err := validateSidecarAgent(identityAgent(roleCoordinator))
	if err == nil || !strings.Contains(err.Error(), "coordinator") {
		t.Fatalf("a coordinator in sidecar mode must be refused: %v", err)
	}
}
