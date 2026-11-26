/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const (
	brokerURL               = "http://liaison.kubemoot.svc:8081"
	accountGatewayContainer = "gateway"
)

func connectedAccountCrew(t *testing.T) *kubemootv1alpha1.Crew {
	t.Helper()
	crew := crewWithHops(hopAccess(validHopKey(t), &kubemootv1alpha1.AskerRule{ConnectedAccount: "library"}))
	crew.Spec.ConnectedAccounts = []kubemootv1alpha1.ConnectedAccount{{Name: "library"}}
	return crew
}

func gatewayPod() *corev1.PodSpec {
	return &corev1.PodSpec{Containers: []corev1.Container{{Name: accountGatewayContainer}}}
}

func TestAConnectedAccountCrewsGatewayGetsTheBrokerAndAScopedToken(t *testing.T) {
	crew := connectedAccountCrew(t)
	pod := gatewayPod()
	applyAccountBroker(pod, crew, brokerURL)
	env := pod.Containers[0].Env
	if v, _ := envValue(env, identity.EnvAccountBrokerURL); v != brokerURL {
		t.Errorf("broker address: %q", v)
	}
	if v, _ := envValue(env, identity.EnvAccountBrokerTokenFile); v != accountBrokerMountDir+"/token" {
		t.Errorf("token file: %q", v)
	}
	if len(pod.Volumes) != 1 || pod.Volumes[0].Projected == nil {
		t.Fatalf("volumes: %+v", pod.Volumes)
	}
	src := pod.Volumes[0].Projected.Sources
	if len(src) != 1 || src[0].ServiceAccountToken.Audience != identity.AccountBrokerAudience {
		t.Fatalf("the projected token must be for the broker alone: %+v", src)
	}
	if m := pod.Containers[0].VolumeMounts; len(m) != 1 || !m[0].ReadOnly || m[0].MountPath != accountBrokerMountDir {
		t.Errorf("mount: %+v", m)
	}
}

func TestOtherCrewsAndInstallsWithoutABrokerGetNothing(t *testing.T) {
	other := crewWithHops(hopAccess(validHopKey(t), anySignedIn))
	cases := map[string]struct {
		crew *kubemootv1alpha1.Crew
		url  string
	}{
		"another rule kind":   {other, brokerURL},
		"no broker installed": {connectedAccountCrew(t), ""},
		"blank address":       {connectedAccountCrew(t), "  "},
		"a crew with no hops": {crewWithAccess(nil), brokerURL},
	}
	for name, c := range cases {
		pod := gatewayPod()
		applyAccountBroker(pod, c.crew, c.url)
		if len(pod.Volumes) != 0 || len(pod.Containers[0].Env) != 0 || len(pod.Containers[0].VolumeMounts) != 0 {
			t.Errorf("%s: the pod was changed: %+v", name, pod)
		}
	}
	applyAccountBroker(&corev1.PodSpec{}, connectedAccountCrew(t), brokerURL) // no container: no panic
}

func TestAConnectedAccountCrewWhoseHopsCannotBeAppliedGetsNoBroker(t *testing.T) {
	crew := connectedAccountCrew(t)
	crew.Spec.ConnectedAccounts = nil // the rule names an account the crew does not declare
	if usesConnectedAccount(crew) {
		t.Fatal("an invalid rule must not be applied")
	}
}
