/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	decisionTrue = `{"decision":true}`
	askerGroup   = "oidc:library"
	askerUser    = "oidc:ann"
)

func TestParseAskersAcceptsEachKindAndOnlyOne(t *testing.T) {
	good := map[string]string{
		"kubernetesRBAC":   `{"kubernetesRBAC":{}}`,
		"connectedAccount": `{"connectedAccount":"library"}`,
		"issuerGroups":     `{"issuerGroups":["a"]}`,
		"anySignedIn":      `{"anySignedIn":{}}`,
		"external":         `{"external":{"url":"https://pdp.example.org/access/v1/evaluation"}}`,
	}
	for kind, raw := range good {
		c, err := ParseAskers(raw)
		if err != nil || c.Kind() != kind {
			t.Errorf("%s: kind %v, %v", kind, c, err)
		}
	}
	if c, err := ParseAskers("  "); c != nil || err != nil {
		t.Errorf("empty: %v, %v", c, err)
	}
}

func TestParseAskersRefusesMalformedRules(t *testing.T) {
	for name, raw := range map[string]string{
		garbageText:     `[`,
		"no kind":       `{}`,
		"two kinds":     `{"anySignedIn":{},"connectedAccount":"x"}`,
		"empty groups":  `{"issuerGroups":[]}`,
		"blank group":   `{"issuerGroups":["a",""]}`,
		"http pdp":      `{"external":{"url":"http://pdp.example.org/x"}}`,
		"no pdp host":   `{"external":{"url":"https://"}}`,
		"external only": `{"external":{}}`,
	} {
		if _, err := ParseAskers(raw); !errors.Is(err, ErrAskersConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func asker(groups ...string) *Identity {
	return &Identity{Issuer: HopIssuerPrefix + "liaison", Subject: askerUser, Groups: groups}
}

func allowedBy(t *testing.T, rule AskersConfig, id *Identity) bool {
	t.Helper()
	rule.Namespace, rule.Crew = teamNS, crewName
	got, err := AskerAuthorizer{Rule: rule, Log: logr.Discard()}.Allowed(context.Background(), id, UseAccess(teamNS, crewName))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestAnySignedInAdmitsEveryAssertedPerson(t *testing.T) {
	if !allowedBy(t, AskersConfig{AnySignedIn: &struct{}{}}, asker()) {
		t.Error("refused")
	}
}

func TestIssuerGroupsAdmitOnlyAMemberOfAListedGroup(t *testing.T) {
	rule := AskersConfig{IssuerGroups: []string{askerGroup, "oidc:staff"}}
	if !allowedBy(t, rule, asker("oidc:other", "oidc:staff")) {
		t.Error("a member was refused")
	}
	if allowedBy(t, rule, asker("oidc:other")) || allowedBy(t, rule, asker()) {
		t.Error("a non-member was admitted")
	}
}

func TestAskerRulesAdmitUseOfTheCrewAndNothingElse(t *testing.T) {
	a := AskerAuthorizer{Rule: AskersConfig{AnySignedIn: &struct{}{}, Namespace: teamNS, Crew: crewName}}
	for _, attrs := range []authorizationv1.ResourceAttributes{ConversationsAccess(teamNS, crewName, "list"), UseAccess(teamNS, "")} {
		if ok, err := a.Allowed(context.Background(), asker(), attrs); ok || err != nil {
			t.Errorf("%+v: %v, %v", attrs, ok, err)
		}
	}
}

func TestARuleAdmitsOnlyForTheCrewItGuards(t *testing.T) {
	rule := AskersConfig{AnySignedIn: &struct{}{}, Namespace: teamNS, Crew: crewName}
	a := AskerAuthorizer{Rule: rule}
	for name, attrs := range map[string]authorizationv1.ResourceAttributes{
		"another crew":      UseAccess(teamNS, "other-crew"),
		"another namespace": UseAccess("other-ns", crewName),
	} {
		if ok, err := a.Allowed(context.Background(), asker(), attrs); ok || err != nil {
			t.Errorf("%s: %v, %v", name, ok, err)
		}
	}
	if ok, _ := (AskerAuthorizer{Rule: AskersConfig{AnySignedIn: &struct{}{}}}).Allowed(context.Background(), asker(), UseAccess(teamNS, crewName)); ok {
		t.Error("a rule that names no crew admitted")
	}
}

func TestAnExternalRuleWithNoDecisionPointRefuses(t *testing.T) {
	if allowedBy(t, AskersConfig{External: &ExternalConfig{URL: "https://pdp.example.org"}}, asker()) {
		t.Error("admitted")
	}
}

func envFor(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func hopEnv(t *testing.T, askers string, extra map[string]string) map[string]string {
	t.Helper()
	cfg, _ := json.Marshal([]TrustedHopConfig{{Name: hopName1, Audience: hopAud, PublicKeys: []string{publicPEM(t, hopKey(t))}}})
	env := map[string]string{EnvEnforce: EnvTrue, EnvTrustedHops: string(cfg), EnvAskers: askers}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

func TestFromEnvNonRBACKindsNeverNeedTheKubernetesClient(t *testing.T) {
	for _, rule := range []string{`{"anySignedIn":{}}`, `{"connectedAccount":"x"}`, `{"issuerGroups":["g"]}`} {
		env := hopEnv(t, rule, map[string]string{EnvHashKey: "install-key"})
		called := false
		s, err := FromEnv(envFor(env), func() (kubernetes.Interface, error) { called = true; return nil, errors.New("no") })
		if err != nil || called || s.Authorizer == nil {
			t.Errorf("%s: %v called=%v", rule, err, called)
		}
	}
}

func TestFromEnvRefusesHopsWithoutAnAskersRule(t *testing.T) {
	env := hopEnv(t, "", nil)
	if _, err := FromEnv(envFor(env), noClient); !errors.Is(err, ErrAskersConfig) {
		t.Errorf("err %v", err)
	}
}

func TestFromEnvNonRBACKindsNeedTheHashKey(t *testing.T) {
	env := hopEnv(t, `{"anySignedIn":{}}`, nil)
	if _, err := FromEnv(envFor(env), noClient); !errors.Is(err, ErrNoHashKey) {
		t.Errorf("err %v", err)
	}
}

func TestAPersonOfANonRBACRuleIsNotAKubernetesUser(t *testing.T) {
	key := hopKey(t)
	cfg, _ := json.Marshal([]TrustedHopConfig{{Name: hopName1, Audience: hopAud, PublicKeys: []string{publicPEM(t, key)}}})
	env := map[string]string{EnvEnforce: EnvTrue, EnvTrustedHops: string(cfg), EnvAskers: `{"issuerGroups":["` + askerGroup + `"],"namespace":"` + teamNS + `","crew":"` + crewName + `"}`, EnvHashKey: "k"}
	s, err := FromEnv(envFor(env), noClient)
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := NewHopSigner(key, hopName1, nil)
	member, _ := signer.Assert(hopAud, askerUser, []string{askerGroup})
	other, _ := signer.Assert(hopAud, "oidc:bob", nil)
	caller, err := s.Authenticator.Authenticate(hopRequest(member))
	if err != nil || caller.Principal.Kind != "subscriber" || caller.Principal.Issuer != HopIssuerPrefix+hopName1 || caller.Principal.ID == "" {
		t.Fatalf("caller %+v, %v", caller, err)
	}
	if ok, err := s.Authorizer.Allowed(context.Background(), caller.Identity, UseAccess(teamNS, crewName)); !ok || err != nil {
		t.Errorf("member: %v %v", ok, err)
	}
	caller, _ = s.Authenticator.Authenticate(hopRequest(other))
	if ok, _ := s.Authorizer.Allowed(context.Background(), caller.Identity, UseAccess(teamNS, crewName)); ok {
		t.Error("a person outside the group was admitted")
	}
}

func TestAMissingAssertionIsRefusedAndAMalformedOneIsNotTakenForAToken(t *testing.T) {
	env := hopEnv(t, `{"anySignedIn":{}}`, map[string]string{EnvHashKey: "k"})
	s, err := FromEnv(envFor(env), noClient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticator.Authenticate(hopRequest("")); !errors.Is(err, ErrRequired) {
		t.Errorf("no assertion: %v", err)
	}
	if _, err := s.Authenticator.Authenticate(hopRequest("not.a.jwt")); !errors.Is(err, ErrRequired) && !errors.Is(err, ErrInvalid) {
		t.Errorf("malformed: %v", err)
	}
}

func TestFromEnvRefusesAMalformedAskersRule(t *testing.T) {
	env := hopEnv(t, `{"anySignedIn":{},"issuerGroups":["a"]}`, map[string]string{EnvHashKey: "k"})
	if _, err := FromEnv(envFor(env), noClient); !errors.Is(err, ErrAskersConfig) {
		t.Errorf("err %v", err)
	}
}

func TestTheKubernetesRBACKindMapsThePersonToAKubernetesUser(t *testing.T) {
	key := hopKey(t)
	cfg, _ := NewHopVerifier([]TrustedHopConfig{{Name: hopName1, Audience: hopAud, PublicKeys: []string{publicPEM(t, key)}, KubernetesUser: true}}, nil)
	signer, _ := NewHopSigner(key, hopName1, nil)
	tok, _ := signer.Assert(hopAud, askerUser, nil)
	id, err := cfg.Verify(hopRequest(tok))
	if err != nil || id.Issuer != IssuerKubernetes {
		t.Errorf("identity %+v, %v", id, err)
	}
}

// pdp is an AuthZEN policy decision point that records the last request.
type pdp struct {
	status int
	body   string
	got    map[string]any
	auth   string
}

func (p *pdp) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &p.got)
		p.auth = r.Header.Get("Authorization")
		w.WriteHeader(p.status)
		_, _ = io.WriteString(w, p.body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func pdpAuthorizer(t *testing.T, p *pdp, token string) AskerAuthorizer {
	t.Helper()
	srv := p.server(t)
	c, err := NewAuthZENClient(ExternalConfig{URL: srv.URL + "/access/v1/evaluation"}, token, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return AskerAuthorizer{Rule: AskersConfig{External: &ExternalConfig{URL: srv.URL}, Namespace: teamNS, Crew: crewName}, PDP: c}
}

func TestExternalAdmitsOnlyDecisionTrue(t *testing.T) {
	p := &pdp{status: 200, body: `{"decision":true,"context":{"x":1}}`}
	a := pdpAuthorizer(t, p, "pdp-token")
	ok, err := a.Allowed(context.Background(), asker(askerGroup), UseAccess(teamNS, crewName))
	if !ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	if p.auth != "Bearer pdp-token" {
		t.Errorf("auth %q", p.auth)
	}
}

func TestExternalSendsSubjectActionAndResource(t *testing.T) {
	p := &pdp{status: 200, body: decisionTrue}
	a := pdpAuthorizer(t, p, "")
	if _, err := a.Allowed(context.Background(), asker(askerGroup), UseAccess(teamNS, crewName)); err != nil {
		t.Fatal(err)
	}
	subject := p.got["subject"].(map[string]any)
	resource := p.got["resource"].(map[string]any)
	action := p.got["action"].(map[string]any)
	if subject["type"] != "user" || subject["id"] != askerUser || resource["type"] != "crew" ||
		resource["id"] != teamNS+"/"+crewName || action["name"] != "ask" {
		t.Errorf("request %v", p.got)
	}
	if g := subject["properties"].(map[string]any)["groups"].([]any); len(g) != 1 || g[0] != askerGroup {
		t.Errorf("groups %v", g)
	}
}

func TestExternalRefusesADenialAndFailsClosedOnEverythingElse(t *testing.T) {
	cases := map[string]*pdp{
		"denied":          {status: 200, body: `{"decision":false}`},
		"no decision":     {status: 200, body: `{}`},
		"string decision": {status: 200, body: `{"decision":"true"}`},
		garbageText:       {status: 200, body: `yes`},
		"server error":    {status: 500, body: decisionTrue},
		"unauthorized":    {status: 401, body: decisionTrue},
		"redirect":        {status: 302, body: decisionTrue},
		"too large":       {status: 200, body: `{"decision":true,"context":"` + strings.Repeat("a", authzenMaxBody) + `"}`},
	}
	for name, p := range cases {
		ok, _ := pdpAuthorizer(t, p, "").Allowed(context.Background(), asker(), UseAccess(teamNS, crewName))
		if ok {
			t.Errorf("%s admitted", name)
		}
	}
}

func TestExternalFailsClosedWhenTheDecisionPointIsUnreachable(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	c, _ := NewAuthZENClient(ExternalConfig{URL: srv.URL}, "", srv.Client())
	srv.Close()
	ok, err := AskerAuthorizer{Rule: AskersConfig{External: &ExternalConfig{URL: srv.URL}, Namespace: teamNS, Crew: crewName}, PDP: c}.
		Allowed(context.Background(), asker(), UseAccess(teamNS, crewName))
	if ok || err == nil {
		t.Errorf("%v %v", ok, err)
	}
}

func TestAuthZENClientRefusesAPlainHTTPEndpoint(t *testing.T) {
	if _, err := NewAuthZENClient(ExternalConfig{URL: "http://pdp"}, "", nil); !errors.Is(err, ErrAskersConfig) {
		t.Error(err)
	}
}

func TestExternalSendsConfiguredTypesAndNoTokenWhenNoneIsSet(t *testing.T) {
	p := &pdp{status: 200, body: decisionTrue}
	srv := p.server(t)
	c, _ := NewAuthZENClient(ExternalConfig{URL: srv.URL, SubjectType: "person", ResourceType: "agent-crew", Action: "use"}, "", srv.Client())
	if ok, err := c.Evaluate(context.Background(), *asker(), "ns/c"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if p.got["subject"].(map[string]any)["type"] != "person" || p.got["resource"].(map[string]any)["type"] != "agent-crew" ||
		p.got["action"].(map[string]any)["name"] != "use" || p.auth != "" {
		t.Errorf("%v auth %q", p.got, p.auth)
	}
}
