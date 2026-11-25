/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const (
	askersHop      = "liaison"
	askersHash     = "hash"
	askersNS       = "askers-ns"
	askersCrew     = "askers-crew"
	askersPDP      = "https://pdp.example.org/x"
	askersTokenKey = "pdp-token"
)

const malformedPEM = "-----BEGIN PUBLIC KEY-----\nMFkw\n-----END PUBLIC KEY-----\n"

func validHopKey(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func hopAccess(key string, rule *kubemootv1alpha1.AskerRule) *kubemootv1alpha1.DiscussionAccess {
	return &kubemootv1alpha1.DiscussionAccess{
		TokenReview:      ptr.To(false),
		TrustedHops:      []kubemootv1alpha1.TrustedHop{{Name: askersHop, PublicKeys: []string{key}}},
		Askers:           rule,
		HashKeySecretRef: &kubemootv1alpha1.HashKeySecretRef{Name: askersHash},
	}
}

func crewWithHops(access *kubemootv1alpha1.DiscussionAccess) *kubemootv1alpha1.Crew {
	crew := crewWithAccess(access)
	crew.Namespace, crew.Name = askersNS, askersCrew
	return crew
}

func authCondition(crew *kubemootv1alpha1.Crew) *metav1.Condition {
	reconcileAuthentication(crew)
	return meta.FindStatusCondition(crew.Status.Conditions, conditionTypeAuthentication)
}

var anySignedIn = &kubemootv1alpha1.AskerRule{AnySignedIn: &kubemootv1alpha1.AnySignedInAskers{}}

func TestTrustedHopsAndTheRuleBecomeTheGatewaysEnvironmentForThisCrewAlone(t *testing.T) {
	crew := crewWithHops(hopAccess(validHopKey(t), anySignedIn))
	env := gatewayTrustedHopEnv(crew)
	raw, ok := envValue(env, identity.EnvTrustedHops)
	if !ok {
		t.Fatalf("no hop environment: %v", env)
	}
	hops, err := identity.ParseTrustedHops(raw)
	if err != nil || len(hops) != 1 || hops[0].Name != askersHop || hops[0].Audience != identity.HopAudience(askersNS, askersCrew) {
		t.Fatalf("hops %+v, %v", hops, err)
	}
}

func TestTheRuleIsBoundToThisCrewAndCarriesTheHashKey(t *testing.T) {
	env := gatewayTrustedHopEnv(crewWithHops(hopAccess(validHopKey(t), anySignedIn)))
	rule, _ := envValue(env, identity.EnvAskers)
	if parsed, err := identity.ParseAskers(rule); err != nil || parsed.Kind() != "anySignedIn" ||
		parsed.Namespace != askersNS || parsed.Crew != askersCrew {
		t.Errorf("rule %q, %v", rule, err)
	}
	if _, ok := envValue(env, identity.EnvHashKey); !ok {
		t.Error("a rule that is not kubernetesRBAC needs the hash key")
	}
}

func TestKubernetesRBACNeedsNoHashKeyAndOthersCarryOne(t *testing.T) {
	rbac := hopAccess(validHopKey(t), &kubemootv1alpha1.AskerRule{KubernetesRBAC: &kubemootv1alpha1.KubernetesRBACAskers{}})
	rbac.HashKeySecretRef = nil
	env := gatewayTrustedHopEnv(crewWithHops(rbac))
	if _, ok := envValue(env, identity.EnvHashKey); ok || len(env) != 2 {
		t.Errorf("env %v", env)
	}
	other := hopAccess(validHopKey(t), anySignedIn)
	other.HashKeySecretRef = nil
	if env := gatewayTrustedHopEnv(crewWithHops(other)); env != nil {
		t.Errorf("a rule that needs the hash key got env %v", env)
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

func TestOnlyTheKubernetesRBACKindGetsTheSubjectAccessReviewRight(t *testing.T) {
	key := validHopKey(t)
	rbac := gatewayAccessRules(hopAccess(key, &kubemootv1alpha1.AskerRule{KubernetesRBAC: &kubemootv1alpha1.KubernetesRBACAskers{}}))
	if len(rbac) != 1 || rbac[0].Resources[0] != "subjectaccessreviews" {
		t.Fatalf("rules %v", rbac)
	}
	for name, rule := range map[string]*kubemootv1alpha1.AskerRule{
		"any":      anySignedIn,
		"account":  {ConnectedAccount: celAccount},
		"groups":   {IssuerGroups: []string{"g"}},
		"external": {External: &kubemootv1alpha1.ExternalAskers{URL: askersPDP}},
	} {
		if rules := gatewayAccessRules(hopAccess(key, rule)); len(rules) != 0 {
			t.Errorf("%s: the asker is not a Kubernetes user, rules %v", name, rules)
		}
	}
}

func TestAuthenticationConditionNamesTheTrustedHopAndWhoMayAsk(t *testing.T) {
	for want, rule := range map[string]*kubemootv1alpha1.AskerRule{
		"get on the Crew":   {KubernetesRBAC: &kubemootv1alpha1.KubernetesRBACAskers{}},
		"one of the groups": {IssuerGroups: []string{"g"}},
		"external policy":   {External: &kubemootv1alpha1.ExternalAskers{URL: askersPDP}},
		"every person":      anySignedIn,
	} {
		c := authCondition(crewWithHops(hopAccess(validHopKey(t), rule)))
		if c == nil || c.Status != metav1.ConditionTrue || c.Reason != reasonTrustedHop || !strings.Contains(c.Message, askersHop) {
			t.Fatalf("condition %+v", c)
		}
		if !strings.Contains(strings.ToLower(c.Message), strings.ToLower(want)) && want != "every person" {
			t.Errorf("%q not in %q", want, c.Message)
		}
	}
}

func TestAMalformedTrustedHopKeyIsReportedAndNoAssertionIsAccepted(t *testing.T) {
	crew := crewWithHops(hopAccess(malformedPEM, anySignedIn))
	c := authCondition(crew)
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != reasonInvalidTrustedHops ||
		!strings.Contains(c.Message, `"liaison"`) || !strings.Contains(c.Message, "publicKeys[0]") {
		t.Fatalf("condition %+v", c)
	}
	if env := gatewayTrustedHopEnv(crew); env != nil {
		t.Errorf("a malformed key reached the gateway: %v", env)
	}
}

func TestAnInvalidAskersRuleIsReportedAndRefusesEveryone(t *testing.T) {
	key := validHopKey(t)
	cases := map[string]*kubemootv1alpha1.DiscussionAccess{
		"no rule":             hopAccess(key, nil),
		"no kind":             hopAccess(key, &kubemootv1alpha1.AskerRule{}),
		"two kinds":           hopAccess(key, &kubemootv1alpha1.AskerRule{AnySignedIn: &kubemootv1alpha1.AnySignedInAskers{}, ConnectedAccount: "x"}),
		"undeclared account":  hopAccess(key, &kubemootv1alpha1.AskerRule{ConnectedAccount: celAccount}),
		"http decision point": hopAccess(key, &kubemootv1alpha1.AskerRule{External: &kubemootv1alpha1.ExternalAskers{URL: "http://pdp"}}),
	}
	for name, access := range cases {
		crew := crewWithHops(access)
		c := authCondition(crew)
		if c == nil || c.Status != metav1.ConditionFalse || c.Reason != reasonInvalidTrustedHops {
			t.Errorf("%s: condition %+v", name, c)
		}
		if env := gatewayTrustedHopEnv(crew); env != nil {
			t.Errorf("%s: env %v", name, env)
		}
	}
}

func TestADeclaredConnectedAccountIsAccepted(t *testing.T) {
	crew := crewWithHops(hopAccess(validHopKey(t), &kubemootv1alpha1.AskerRule{ConnectedAccount: celAccount}))
	crew.Spec.ConnectedAccounts = []kubemootv1alpha1.ConnectedAccount{{Name: celAccount}}
	if c := authCondition(crew); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("condition %+v", c)
	}
}

func TestExternalDecisionPointTokenComesFromItsSecret(t *testing.T) {
	rule := &kubemootv1alpha1.AskerRule{External: &kubemootv1alpha1.ExternalAskers{
		URL: askersPDP, TokenSecretRef: &kubemootv1alpha1.SecretKeyReference{Name: "pdp", Key: askersTokenKey},
	}}
	env := gatewayTrustedHopEnv(crewWithHops(hopAccess(validHopKey(t), rule)))
	for _, e := range env {
		if e.Name == identity.EnvAskersToken && e.ValueFrom.SecretKeyRef.Name == "pdp" && e.ValueFrom.SecretKeyRef.Key == askersTokenKey {
			return
		}
	}
	t.Errorf("env %v", env)
}
