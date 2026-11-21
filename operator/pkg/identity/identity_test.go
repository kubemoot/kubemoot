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
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	saUser    = "system:serviceaccount:team:builder"
	humanUser = "jane@example.com"
	crewName  = "pilot"
	teamNS    = "team"
)

// tokenClient answers TokenReviews from a table of token to user and audiences, and
// SubjectAccessReviews from the set of "user verb resource/subresource" strings allowed.
func tokenClient(users map[string]authenticationv1.TokenReviewStatus, allowed map[string]bool) *fake.Clientset {
	c := fake.NewSimpleClientset()
	c.PrependReactor("create", "tokenreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		tr := a.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		st := users[tr.Spec.Token]
		return true, &authenticationv1.TokenReview{Status: st}, nil
	})
	c.PrependReactor("create", "subjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		sar := a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		ra := sar.Spec.ResourceAttributes
		key := sar.Spec.User + " " + ra.Verb + " " + ra.Resource
		if ra.Subresource != "" {
			key += "/" + ra.Subresource
		}
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: allowed[key]}}, nil
	})
	return c
}

func users() map[string]authenticationv1.TokenReviewStatus {
	return map[string]authenticationv1.TokenReviewStatus{
		"sa-token": {Authenticated: true, User: authenticationv1.UserInfo{Username: saUser, Groups: []string{"g"}},
			Audiences: []string{"https://kubernetes.default.svc"}},
		"gw-token": {Authenticated: true, User: authenticationv1.UserInfo{Username: saUser},
			Audiences: []string{"kubemoot-gateway"}},
		"human-token": {Authenticated: true, User: authenticationv1.UserInfo{Username: humanUser}},
	}
}

func request(authorization string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	return r
}

func tokenAuthenticator(allowAnonymous bool, audiences ...string) *Authenticator {
	rv := Reviewer{Client: tokenClient(users(), nil), Audiences: audiences}
	return NewAuthenticator(Hasher{}, allowAnonymous, TokenReviewVerifier{Reviewer: rv})
}

func TestAnonymousAuthenticatorAcceptsEveryoneAndEnforcesNothing(t *testing.T) {
	a := AnonymousAuthenticator()
	if a.Enforcing() {
		t.Fatal("the default authenticator must not enforce")
	}
	// A token is ignored, not checked: the crew behaves as it did before identity.
	for _, h := range []string{"", "Bearer sa-token", "Bearer garbage", "Basic abc"} {
		c, err := a.Authenticate(request(h))
		if err != nil || !c.Anonymous() || c.Principal.Kind != "anonymous" || c.Principal.ID != "" {
			t.Errorf("%q: %+v, %v", h, c, err)
		}
	}
}

func TestTokenReviewVerifierValidTokenBecomesAHashedPrincipal(t *testing.T) {
	c, err := tokenAuthenticator(false).Authenticate(request("Bearer human-token"))
	if err != nil {
		t.Fatal(err)
	}
	p := c.Principal
	if p.Kind != "kubernetes" || p.Issuer != IssuerKubernetes || len(p.ID) != idLength || p.ID == humanUser {
		t.Fatalf("principal %+v", p)
	}
	if p.DisplayName != "" {
		t.Errorf("a person's name must not travel: %q", p.DisplayName)
	}
	if c.Identity.Subject != humanUser {
		t.Errorf("the raw subject stays in the identity: %+v", c.Identity)
	}
	again, _ := tokenAuthenticator(false).Authenticate(request("Bearer human-token"))
	if again.Principal.ID != p.ID {
		t.Error("the id must be stable across requests")
	}
}

func TestTokenReviewVerifierShowsAWorkloadsName(t *testing.T) {
	c, err := tokenAuthenticator(false).Authenticate(request("bearer sa-token"))
	if err != nil || c.Principal.DisplayName != saUser {
		t.Fatalf("%+v, %v", c, err)
	}
}

func TestTokenReviewVerifierInvalidTokenIsRefusedEvenWhenAnonymousIsAllowed(t *testing.T) {
	_, err := tokenAuthenticator(true).Authenticate(request("Bearer nope"))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid: a bad credential is never downgraded to anonymous", err)
	}
	if code, challenge := Status(err); code != http.StatusUnauthorized || !challenge {
		t.Errorf("status %d challenge %v", code, challenge)
	}
}

func TestMissingCredential(t *testing.T) {
	if _, err := tokenAuthenticator(false).Authenticate(request("")); !errors.Is(err, ErrRequired) {
		t.Fatalf("got %v, want ErrRequired", err)
	}
	if _, err := tokenAuthenticator(false).Authenticate(request("Basic abc")); !errors.Is(err, ErrRequired) {
		t.Fatalf("a non-bearer header is no credential: %v", err)
	}
	c, err := tokenAuthenticator(true).Authenticate(request(""))
	if err != nil || !c.Anonymous() {
		t.Fatalf("anonymous allowed: %+v, %v", c, err)
	}
}

func TestWrongAudienceIsRefused(t *testing.T) {
	a := tokenAuthenticator(false, "kubemoot-gateway")
	if _, err := a.Authenticate(request("Bearer sa-token")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a token minted for the API server's audience: %v", err)
	}
	if c, err := a.Authenticate(request("Bearer gw-token")); err != nil || c.Principal.ID == "" {
		t.Fatalf("a token bound to the gateway's audience: %+v, %v", c, err)
	}
}

func TestVerifierFailureIsNotAnAuthenticationAnswer(t *testing.T) {
	c := fake.NewSimpleClientset()
	c.PrependReactor("create", "tokenreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("api server down")
	})
	a := NewAuthenticator(Hasher{}, true, TokenReviewVerifier{Reviewer: Reviewer{Client: c}})
	_, err := a.Authenticate(request("Bearer x"))
	if err == nil || errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v", err)
	}
	if code, _ := Status(err); code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", code)
	}
}

func TestVerifiersAreTriedInOrderAndANoCredentialFallsThrough(t *testing.T) {
	first := verifierFunc(func(*http.Request) (*Identity, error) { return nil, ErrNoCredential })
	second := verifierFunc(func(*http.Request) (*Identity, error) {
		return &Identity{Issuer: "https://idp.example", Subject: "u1"}, nil
	})
	c, err := NewAuthenticator(Hasher{}, false, first, second).Authenticate(request(""))
	if err != nil || c.Principal.Issuer != "https://idp.example" {
		t.Fatalf("%+v, %v", c, err)
	}
}

type verifierFunc func(*http.Request) (*Identity, error)

func (f verifierFunc) Verify(r *http.Request) (*Identity, error) { return f(r) }

func TestHasherIsKeyedAndSeparatesIssuerFromSubject(t *testing.T) {
	plain, keyed := Hasher{}, Hasher{Key: []byte("install-secret")}
	if plain.ID("a", "bc") == plain.ID("ab", "c") {
		t.Error("issuer and subject must not run together")
	}
	if plain.ID("kubernetes", "u") == keyed.ID("kubernetes", "u") {
		t.Error("a keyed hash must differ from the unkeyed one")
	}
	again := Hasher{Key: []byte("install-secret")}
	if keyed.ID("kubernetes", "u") != again.ID("kubernetes", "u") {
		t.Error("the id must be deterministic across replicas holding the key")
	}
}

func TestAllowedAsksTheAPIServerAndReadsTheAnswer(t *testing.T) {
	rv := Reviewer{Client: tokenClient(users(), map[string]bool{
		saUser + " get crews": true,
	})}
	id := &Identity{Issuer: IssuerKubernetes, Subject: saUser}
	if ok, err := rv.Allowed(context.Background(), id, UseAccess(teamNS, crewName)); err != nil || !ok {
		t.Errorf("use: %v, %v", ok, err)
	}
	other := UseAccess(teamNS, "other-crew")
	other.Verb = "delete"
	if ok, err := rv.Allowed(context.Background(), id, other); err != nil || ok {
		t.Errorf("a verb with no rule: %v, %v", ok, err)
	}
	if ok, err := rv.Allowed(context.Background(), &Identity{Subject: humanUser}, UseAccess(teamNS, crewName)); err != nil || ok {
		t.Errorf("an identity with no rule: %v, %v", ok, err)
	}
}

func TestAllowedReportsAFailedReview(t *testing.T) {
	c := fake.NewSimpleClientset()
	c.PrependReactor("create", "subjectaccessreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("boom")
	})
	if _, err := (Reviewer{Client: c}).Allowed(context.Background(), &Identity{}, UseAccess(teamNS, crewName)); err == nil {
		t.Fatal("a failed review must be an error, never an allow")
	}
}

func TestBearerToken(t *testing.T) {
	const tok = "abc"
	cases := map[string]string{"Bearer " + tok: tok, "bearer " + tok: tok, "BEARER  " + tok + " ": tok, "Basic " + tok: "", tok: "", "": "", "Bearer": ""}
	for header, want := range cases {
		if got := BearerToken(request(header)); got != want {
			t.Errorf("BearerToken(%q) = %q, want %q", header, got, want)
		}
	}
}

func TestCallerFromDefaultsToAnonymous(t *testing.T) {
	if !CallerFrom(context.Background()).Anonymous() {
		t.Fatal("no caller in the context is the anonymous caller")
	}
	c := Caller{Identity: &Identity{Subject: "u"}}
	if CallerFrom(WithCaller(context.Background(), c)).Identity.Subject != "u" {
		t.Fatal("the caller a door stored must come back")
	}
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func noClient() (kubernetes.Interface, error) { return nil, errors.New("no client") }

func TestFromEnvWithNoSettingsEnforcesNothingAndNeedsNoClient(t *testing.T) {
	if s, err := FromEnv(envOf(nil), noClient); err != nil || s.Authenticator.Enforcing() {
		t.Fatalf("%+v, %v", s, err)
	}
}

func TestFromEnvEnforcedWithoutTokenReviewRefusesCallersWithNoCredential(t *testing.T) {
	closed, err := FromEnv(envOf(map[string]string{EnvEnforce: EnvTrue}), noClient)
	if err != nil || !closed.Authenticator.Enforcing() {
		t.Fatalf("%+v, %v", closed, err)
	}
	if _, err := closed.Authenticator.Authenticate(request("")); !errors.Is(err, ErrRequired) {
		t.Fatalf("%v", err)
	}
}

func TestFromEnvFailsWhenTheClientCannotBeBuilt(t *testing.T) {
	if _, err := FromEnv(envOf(map[string]string{EnvEnforce: EnvTrue, EnvTokenReview: EnvTrue}), noClient); err == nil {
		t.Fatal("serving open when verification was asked for is the wrong failure")
	}
}

func TestSplitList(t *testing.T) {
	if got := splitList(" a, ,b,"); len(got) != 2 || splitList("") != nil {
		t.Fatalf("%v", got)
	}
}

func TestRefusal(t *testing.T) {
	code, msg, challenge := Refusal(ErrInvalid, "r")
	if code != http.StatusUnauthorized || challenge != `Bearer realm="r"` || msg == "" {
		t.Errorf("%d %q %q", code, msg, challenge)
	}
	code, msg, challenge = Refusal(errors.New("api server at 10.0.0.1 refused"), "r")
	if code != http.StatusServiceUnavailable || challenge != "" || strings.Contains(msg, "10.0.0.1") {
		t.Errorf("the cause of a failed check must not be sent: %d %q %q", code, msg, challenge)
	}
}

func TestPrincipalOfAndSamePrincipal(t *testing.T) {
	sa := PrincipalOf(Hasher{}, &Identity{Issuer: IssuerKubernetes, Subject: saUser})
	if sa.DisplayName != saUser || sa.ID == "" {
		t.Fatalf("%+v", sa)
	}
	if PrincipalOf(Hasher{}, &Identity{Issuer: IssuerKubernetes, Subject: humanUser}).DisplayName != "" {
		t.Error("a person's name must not travel")
	}
	other := PrincipalOf(Hasher{}, &Identity{Issuer: "https://idp.example", Subject: saUser})
	if SamePrincipal(sa, other) {
		t.Error("the same subject from another issuer is another principal")
	}
	if !SamePrincipal(sa, PrincipalOf(Hasher{}, &Identity{Issuer: IssuerKubernetes, Subject: saUser})) {
		t.Error("the same identity is the same principal")
	}
	anon := AnonymousCaller().Principal
	if SamePrincipal(sa, anon) || !SamePrincipal(anon, anon) {
		t.Error("anonymous callers share one principal, and no other")
	}
}

func TestValidateAccessReasonCountsCharacters(t *testing.T) {
	for header, ok := range map[string]bool{
		"": false, "   ": false, " why ": true, strings.Repeat("é", MaxAccessReason): true,
		strings.Repeat("é", MaxAccessReason+1): false,
	} {
		if reason, got := ValidateAccessReason(header); got != ok || (ok && reason != strings.TrimSpace(header)) {
			t.Errorf("%q: %q %v", header, reason, got)
		}
	}
}

func TestOperatorAccessAuditHasOneShape(t *testing.T) {
	var line string
	log := funcr.New(func(prefix, args string) { line = prefix + args }, funcr.Options{})
	OperatorAccess{Operator: &Identity{Issuer: IssuerKubernetes, Subject: "admin"}, Operation: "stream", Namespace: "ns",
		Crew: "c", ConversationID: "conv", OwnerID: "own", Reason: "why"}.Audit(log)
	for _, want := range []string{AuditEvent, `"operator"="admin"`, `"operation"="stream"`, `"owner"="own"`, `"reason"="why"`, `"conversationId"="conv"`} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %s: %s", want, line)
		}
	}
}
