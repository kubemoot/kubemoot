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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestAnAgentsEnvironmentCarriesTheExposureOfItsNamespacesCrew(t *testing.T) {
	scheme := agentReconcileScheme(t)
	crew := crewWithIdentity(true, &kubemootv1alpha1.DiscussionIdentity{ExposeToAgents: true})
	r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(crew).Build()}
	if env := agentIdentityEnv(r.crewOf(context.Background(), "ns")); len(env) != 1 {
		t.Errorf("crew that exposes the caller: %v", env)
	}
	if env := agentIdentityEnv(r.crewOf(context.Background(), "other")); len(env) != 0 {
		t.Errorf("namespace without a crew: %v", env)
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
