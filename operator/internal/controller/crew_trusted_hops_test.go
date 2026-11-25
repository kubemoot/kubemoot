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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const hopPEM = "-----BEGIN PUBLIC KEY-----\nMFkw\n-----END PUBLIC KEY-----\n"

func crewWithHops(access *kubemootv1alpha1.DiscussionAccess) *kubemootv1alpha1.Crew {
	crew := crewWithAccess(access)
	crew.Namespace, crew.Name = "team", "pilot"
	return crew
}

func TestTrustedHopsBecomeTheGatewaysEnvironmentForThisCrewAlone(t *testing.T) {
	access := &kubemootv1alpha1.DiscussionAccess{
		TrustedHops: []kubemootv1alpha1.TrustedHop{{Name: "liaison", PublicKeys: []string{hopPEM}}},
	}
	env := gatewayTrustedHopEnv(crewWithHops(access))
	raw, ok := envValue(env, identity.EnvTrustedHops)
	if !ok {
		t.Fatalf("no hop environment: %v", env)
	}
	hops, err := identity.ParseTrustedHops(raw)
	if err != nil || len(hops) != 1 {
		t.Fatalf("hops %v, %v", hops, err)
	}
	if hops[0].Name != "liaison" || hops[0].Audience != identity.HopAudience("team", "pilot") || hops[0].PublicKeys[0] != hopPEM {
		t.Errorf("hop = %+v", hops[0])
	}
}

func TestACrewWithNoTrustedHopsGetsNoHopSettings(t *testing.T) {
	if env := gatewayTrustedHopEnv(crewWithHops(nil)); len(env) != 0 {
		t.Errorf("env %v", env)
	}
	if env := gatewayTrustedHopEnv(crewWithHops(&kubemootv1alpha1.DiscussionAccess{})); len(env) != 0 {
		t.Errorf("env %v", env)
	}
}

func TestAHopOnlyCrewStillGetsTheSubjectAccessReviewRight(t *testing.T) {
	access := &kubemootv1alpha1.DiscussionAccess{
		TokenReview: ptr.To(false),
		TrustedHops: []kubemootv1alpha1.TrustedHop{{Name: "liaison", PublicKeys: []string{hopPEM}}},
	}
	rules := gatewayAccessRules(access)
	if len(rules) != 1 || rules[0].Resources[0] != "subjectaccessreviews" {
		t.Fatalf("rules %v", rules)
	}
}

func TestAuthenticationConditionNamesTheTrustedHop(t *testing.T) {
	crew := crewWithHops(&kubemootv1alpha1.DiscussionAccess{
		TokenReview: ptr.To(false),
		TrustedHops: []kubemootv1alpha1.TrustedHop{{Name: "liaison", PublicKeys: []string{hopPEM}}},
	})
	reconcileAuthentication(crew)
	c := meta.FindStatusCondition(crew.Status.Conditions, conditionTypeAuthentication)
	if c == nil || c.Status != metav1.ConditionTrue || c.Reason != reasonTrustedHop {
		t.Fatalf("condition %+v", c)
	}
}
