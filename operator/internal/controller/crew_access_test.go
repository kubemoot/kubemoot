/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

func crewWithAccess(access *kubemootv1alpha1.DiscussionAccess) *kubemootv1alpha1.Crew {
	return &kubemootv1alpha1.Crew{Spec: kubemootv1alpha1.CrewSpec{Discussion: &kubemootv1alpha1.DiscussionConfig{Access: access}}}
}

func TestAnUnconfiguredCrewsGatewayGetsNoAccessSettingsAndNoReviewRights(t *testing.T) {
	if env := gatewayEnv("nats://n:4222", nil); len(envNamed(env, identity.EnvEnforce)) != 0 {
		t.Fatalf("an install with no access config must run the gateway as before: %v", env)
	}
	if rules := gatewayAccessRules(nil); len(rules) != 0 {
		t.Fatalf("rules %v", rules)
	}
}

func TestAccessBecomesTheGatewaysEnvironmentAndRights(t *testing.T) {
	access := &kubemootv1alpha1.DiscussionAccess{TokenAudiences: []string{"a", "b"}}
	env := gatewayEnv("nats://n:4222", crewWithAccess(access))
	for name, want := range map[string]string{
		identity.EnvEnforce: valueTrue, identity.EnvTokenReview: valueTrue, identity.EnvAllowAnonymous: "false", identity.EnvTokenAudiences: "a,b",
	} {
		if got, _ := envValue(env, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if rules := gatewayAccessRules(access); len(rules) != 2 {
		t.Fatalf("a gateway that verifies tokens needs TokenReview and SubjectAccessReview: %v", rules)
	}
	off := &kubemootv1alpha1.DiscussionAccess{TokenReview: ptr.To(false), AllowAnonymous: ptr.To(true)}
	if got, _ := envValue(gatewayEnv("x", crewWithAccess(off)), identity.EnvAllowAnonymous); got != valueTrue {
		t.Errorf("allowAnonymous = %q", got)
	}
	if rules := gatewayAccessRules(off); len(rules) != 0 {
		t.Errorf("no review rights without token review: %v", rules)
	}
}

func TestAuthenticationConditionNamesTheAnonymousState(t *testing.T) {
	crew := crewWithAccess(nil)
	reconcileAuthentication(crew, nil)
	c := meta.FindStatusCondition(crew.Status.Conditions, conditionTypeAuthentication)
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != reasonAnonymous {
		t.Fatalf("condition %+v", c)
	}
	crew = crewWithAccess(&kubemootv1alpha1.DiscussionAccess{})
	reconcileAuthentication(crew, nil)
	if c := meta.FindStatusCondition(crew.Status.Conditions, conditionTypeAuthentication); c.Status != metav1.ConditionTrue || c.Reason != reasonTokenReview {
		t.Fatalf("condition %+v", c)
	}
}

func TestNoAuthenticationConditionWithoutAGateway(t *testing.T) {
	crew := crewWithAccess(nil)
	crew.Spec.Discussion.Enabled = ptr.To(false)
	reconcileAuthentication(crew, nil)
	if meta.FindStatusCondition(crew.Status.Conditions, conditionTypeAuthentication) != nil {
		t.Fatal("a crew with no gateway accepts no callers")
	}
}

func envNamed(env []corev1.EnvVar, name string) []corev1.EnvVar {
	var out []corev1.EnvVar
	for _, e := range env {
		if e.Name == name {
			out = append(out, e)
		}
	}
	return out
}

func TestAuthenticationConditionFollowsWhatTheGatewayDoes(t *testing.T) {
	cases := map[string]struct {
		access *kubemootv1alpha1.DiscussionAccess
		status metav1.ConditionStatus
		reason string
	}{
		"no token review, anonymous allowed: verifies nothing": {
			&kubemootv1alpha1.DiscussionAccess{TokenReview: ptr.To(false), AllowAnonymous: ptr.To(true)},
			metav1.ConditionFalse, reasonAnonymous},
		"no token review, no anonymous: refuses all": {
			&kubemootv1alpha1.DiscussionAccess{TokenReview: ptr.To(false)}, metav1.ConditionFalse, reasonClosed},
		"token review with anonymous allowed": {
			&kubemootv1alpha1.DiscussionAccess{AllowAnonymous: ptr.To(true)}, metav1.ConditionTrue, reasonTokenReview},
	}
	for name, tc := range cases {
		crew := crewWithAccess(tc.access)
		reconcileAuthentication(crew, nil)
		c := meta.FindStatusCondition(crew.Status.Conditions, conditionTypeAuthentication)
		if c == nil || c.Status != tc.status || c.Reason != tc.reason {
			t.Errorf("%s: %+v", name, c)
		}
	}
}

// The operator writes the environment and the gateway reads it with FromEnv: a round trip
// proves the two agree on every name, so a rename cannot leave a gateway running open.
func TestTheGatewayEnvironmentRoundTripsThroughFromEnv(t *testing.T) {
	client := func() (kubernetes.Interface, error) { return fake.NewSimpleClientset(), nil }
	getenv := func(access *kubemootv1alpha1.DiscussionAccess) func(string) string {
		env := gatewayEnv("nats://n:4222", crewWithAccess(access))
		return func(name string) string { v, _ := envValue(env, name); return v }
	}
	open, err := identity.FromEnv(getenv(nil), client)
	if err != nil || open.Authenticator.Enforcing() || open.Authorizer != nil {
		t.Fatalf("an unconfigured crew must run open: %+v, %v", open, err)
	}
	set, err := identity.FromEnv(getenv(&kubemootv1alpha1.DiscussionAccess{TokenAudiences: []string{"gw"}}), client)
	if err != nil || !set.Authenticator.Enforcing() || set.Authorizer == nil {
		t.Fatalf("a configured crew must verify: %+v, %v", set, err)
	}
	if _, err := set.Authenticator.Authenticate(httptest.NewRequest("GET", "/", nil)); err == nil {
		t.Fatal("a request with no token must be refused when anonymous is not allowed")
	}
	closed, err := identity.FromEnv(getenv(&kubemootv1alpha1.DiscussionAccess{TokenReview: ptr.To(false)}), client)
	if err != nil || !closed.Authenticator.Enforcing() {
		t.Fatalf("a closed crew still enforces: %+v, %v", closed, err)
	}
}
