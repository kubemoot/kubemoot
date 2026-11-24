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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

func crewWithIdentity(access bool, identityConfig *kubemootv1alpha1.DiscussionIdentity) *kubemootv1alpha1.Crew {
	d := &kubemootv1alpha1.DiscussionConfig{Identity: identityConfig}
	if access {
		d.Access = &kubemootv1alpha1.DiscussionAccess{}
	}
	return &kubemootv1alpha1.Crew{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
		Spec:       kubemootv1alpha1.CrewSpec{Discussion: d},
	}
}

func TestACrewTellsItsAgentsWhoIsAskingOnlyWhenItSaysSo(t *testing.T) {
	expose := &kubemootv1alpha1.DiscussionIdentity{ExposeToAgents: true}
	cases := map[string]struct {
		crew *kubemootv1alpha1.Crew
		want bool
	}{
		"exposed with access":      {crewWithIdentity(true, expose), true},
		"identity left off":        {crewWithIdentity(true, &kubemootv1alpha1.DiscussionIdentity{}), false},
		"no identity block":        {crewWithIdentity(true, nil), false},
		"exposed but no access":    {crewWithIdentity(false, expose), false},
		"no discussion at all":     {&kubemootv1alpha1.Crew{}, false},
		"no crew in the namespace": {nil, false},
	}
	for name, c := range cases {
		if got := exposesCaller(c.crew); got != c.want {
			t.Errorf("%s: exposesCaller %v, want %v", name, got, c.want)
		}
		if got := len(gatewayIdentityEnv(c.crew)) == 1; got != c.want {
			t.Errorf("%s: gateway env %v, want exposed=%v", name, gatewayIdentityEnv(c.crew), c.want)
		}
		if got := len(agentIdentityEnv(c.crew)) == 1; got != c.want {
			t.Errorf("%s: agent env %v, want exposed=%v", name, agentIdentityEnv(c.crew), c.want)
		}
	}
}

func TestTheExposureVariablesAreTheSharedNamesSetToTrue(t *testing.T) {
	crew := crewWithIdentity(true, &kubemootv1alpha1.DiscussionIdentity{ExposeToAgents: true})
	if env := gatewayIdentityEnv(crew); env[0].Name != identity.EnvExposeLabel || env[0].Value != identity.EnvTrue {
		t.Errorf("gateway %v", env)
	}
	if env := agentIdentityEnv(crew); env[0].Name != identity.EnvExposeCaller || env[0].Value != identity.EnvTrue {
		t.Errorf("agent %v", env)
	}
}

func gatewayContainerEnv(t *testing.T, crew *kubemootv1alpha1.Crew) []corev1.EnvVar {
	t.Helper()
	r := &CrewReconciler{ConfigCache: NewConfigCache()}
	containers := r.buildDeployment(crew).Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("the gateway deployment has %d containers, want 1", len(containers))
	}
	return containers[0].Env
}

func TestTheGatewayDeploymentStampsLabelsOnlyForACrewThatExposesTheCaller(t *testing.T) {
	on := crewWithIdentity(true, &kubemootv1alpha1.DiscussionIdentity{ExposeToAgents: true})
	if v, ok := envValue(gatewayContainerEnv(t, on), identity.EnvExposeLabel); !ok || v != identity.EnvTrue {
		t.Errorf("exposing crew: %s = %q, %v", identity.EnvExposeLabel, v, ok)
	}
	for name, crew := range map[string]*kubemootv1alpha1.Crew{
		"identity left off": crewWithIdentity(true, &kubemootv1alpha1.DiscussionIdentity{}),
		"no identity block": crewWithIdentity(true, nil),
		"no access":         crewWithIdentity(false, nil),
	} {
		if _, ok := envValue(gatewayContainerEnv(t, crew), identity.EnvExposeLabel); ok {
			t.Errorf("%s: the gateway is told to stamp labels", name)
		}
	}
}

func TestAnAgentsEnvironmentCarriesTheExposureOfItsNamespacesCrew(t *testing.T) {
	scheme := agentReconcileScheme(t)
	agent := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"}}
	pick := &modelPick{ModelID: "m"}
	for name, c := range map[string]struct {
		crew *kubemootv1alpha1.Crew
		want bool
	}{
		"exposing crew": {crewWithIdentity(true, &kubemootv1alpha1.DiscussionIdentity{ExposeToAgents: true}), true},
		"identity off":  {crewWithIdentity(true, &kubemootv1alpha1.DiscussionIdentity{}), false},
		"no crew":       {nil, false},
	} {
		b := fake.NewClientBuilder().WithScheme(scheme)
		if c.crew != nil {
			b = b.WithObjects(c.crew)
		}
		r := &AgentReconciler{Client: b.Build(), Scheme: scheme, ConfigCache: NewConfigCache()}
		env := r.buildEnvVars(context.Background(), agent, pick, pick, 8080)
		v, ok := envValue(env, identity.EnvExposeCaller)
		if ok != c.want || (ok && v != identity.EnvTrue) {
			t.Errorf("%s: %s = %q, %v; want set=%v", name, identity.EnvExposeCaller, v, ok, c.want)
		}
	}
}

func TestAgentsAreReconciledWhenTheCrewStartsOrStopsExposingTheCaller(t *testing.T) {
	off := crewWithIdentity(true, &kubemootv1alpha1.DiscussionIdentity{})
	on := crewWithIdentity(true, &kubemootv1alpha1.DiscussionIdentity{ExposeToAgents: true})
	noAccess := crewWithIdentity(false, &kubemootv1alpha1.DiscussionIdentity{ExposeToAgents: true})
	p := crewAgentWiringChanged()
	if !p.Update(event.UpdateEvent{ObjectOld: off, ObjectNew: on}) || !p.Update(event.UpdateEvent{ObjectOld: on, ObjectNew: off}) {
		t.Error("turning the exposure on or off did not fire")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: on, ObjectNew: noAccess}) {
		t.Error("removing access, which ends the exposure, did not fire")
	}
	same := on.DeepCopy()
	same.Spec.Description = "x"
	if p.Update(event.UpdateEvent{ObjectOld: on, ObjectNew: same}) {
		t.Error("an unrelated change fired")
	}
}

func TestASidecarAgentsSidecarCarriesTheExposureOfItsNamespacesCrew(t *testing.T) {
	for name, c := range map[string]struct {
		crew *kubemootv1alpha1.Crew
		want bool
	}{
		"exposing crew": {crewWithIdentity(true, &kubemootv1alpha1.DiscussionIdentity{ExposeToAgents: true}), true},
		"identity off":  {crewWithIdentity(true, &kubemootv1alpha1.DiscussionIdentity{}), false},
		"no access":     {crewWithIdentity(false, &kubemootv1alpha1.DiscussionIdentity{ExposeToAgents: true}), false},
		"crew absent":   {nil, false},
	} {
		var objs []client.Object
		if c.crew != nil {
			objs = append(objs, c.crew)
		}
		r, _ := harnessReconciler(t, objs...)
		agent := sidecarAgent()
		agent.Namespace = "ns"
		d, err := r.buildSidecarDeployment(context.Background(), agent, harnessPick(testModelID, "rig0"), harnessPick("qwen3:4b", "rig1"), "p", "h1")
		if err != nil {
			t.Fatal(err)
		}
		sidecar := d.Spec.Template.Spec.InitContainers[0]
		v, ok := envValue(sidecar.Env, identity.EnvExposeCaller)
		if ok != c.want || (ok && v != identity.EnvTrue) {
			t.Errorf("%s: sidecar %s = %q, %v; want set=%v", name, identity.EnvExposeCaller, v, ok, c.want)
		}
		if _, onHarness := envValue(d.Spec.Template.Spec.Containers[0].Env, identity.EnvExposeCaller); onHarness {
			t.Errorf("%s: the harness container holds the switch", name)
		}
	}
}
