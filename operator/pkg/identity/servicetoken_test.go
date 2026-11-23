/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

const (
	canary         = "CANARY-TOKEN-1"
	testIssuer     = "oreilly"
	audClaim       = "aud"
	expClaim       = "exp"
	garbageText    = "garbage"
	subClaim       = "sub"
	upstreamClaims = "upstream_claims"
	audValue       = "https://api.example/mcp"
)

var testKey = []byte("install-hash-key")

// fakeClock is a clock the tests move by hand.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// service is a fake of the service that accepts or refuses tokens.
type service struct {
	srv    *httptest.Server
	status atomic.Int32
	calls  atomic.Int32
	// gate, when set before any request, holds every call until it is closed.
	gate   chan struct{}
	mu     sync.Mutex
	auths  []string
	bodies []string
}

func newService(t *testing.T, status int) *service {
	t.Helper()
	s := &service{}
	s.status.Store(int32(status))
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		if s.gate != nil {
			<-s.gate
		}
		buf := new(strings.Builder)
		_, _ = fmt.Fprint(buf, r.Method, " ", r.Header.Get("Content-Type"))
		s.mu.Lock()
		s.auths = append(s.auths, r.Header.Get("Authorization"))
		s.bodies = append(s.bodies, buf.String())
		s.mu.Unlock()
		w.WriteHeader(int(s.status.Load()))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// jwt builds a token with the given payload and a signature nobody can check.
func jwt(t *testing.T, payload map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"HS256"}`)) + "." + enc.EncodeToString(raw) + ".c2ln"
}

func oreillyToken(t *testing.T, sub string, exp time.Time) string {
	t.Helper()
	return jwt(t, map[string]any{upstreamClaims: map[string]any{subClaim: sub}, expClaim: exp.Unix(), audClaim: audValue})
}

func bearer(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

// verifierFor returns a verifier of svc with a hand-moved clock.
func verifierFor(t *testing.T, svc *service, mutate ...func(*ServiceTokenConfig)) (*ServiceTokenVerifier, *fakeClock) {
	t.Helper()
	cfg := ServiceTokenConfig{Name: testIssuer, VerifyURL: svc.srv.URL, SubjectClaim: "upstream_claims.sub"}
	for _, m := range mutate {
		m(&cfg)
	}
	v, err := NewServiceTokenVerifier(cfg, testKey, svc.srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{t: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)}
	v.now = clock.now
	return v, clock
}

func TestAcceptsWhenTheServiceAnswers200(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc)
	token := oreillyToken(t, "acct-42", clock.now().Add(15*time.Minute))
	id, err := v.Verify(bearer(token))
	if err != nil {
		t.Fatal(err)
	}
	if id.Issuer != testIssuer || id.Subject != "acct-42" || id.Credential.Token() != token {
		t.Fatalf("identity %#v", id)
	}
	if svc.calls.Load() != 1 || svc.auths[0] != "Bearer "+token || !strings.HasPrefix(svc.bodies[0], "POST application/json") {
		t.Fatalf("the service saw calls=%d auths=%v bodies=%v", svc.calls.Load(), svc.auths, svc.bodies)
	}
}

func TestRefusesWhenTheServiceRefuses(t *testing.T) {
	for _, status := range []int{401, 403} {
		svc := newService(t, status)
		v, clock := verifierFor(t, svc)
		_, err := v.Verify(bearer(oreillyToken(t, "a", clock.now().Add(time.Hour))))
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("status %d: %v, want ErrInvalid", status, err)
		}
		if code, _ := Status(err); code != 401 {
			t.Errorf("status %d answered %d", status, code)
		}
	}
}

func TestCouldNotDecideNeverAllows(t *testing.T) {
	for _, status := range []int{500, 502, 429, 400, 404, 302} {
		svc := newService(t, status)
		v, clock := verifierFor(t, svc)
		id, err := v.Verify(bearer(oreillyToken(t, "a", clock.now().Add(time.Hour))))
		if id != nil || !errors.Is(err, ErrServiceUnavailable) {
			t.Errorf("status %d: %v %v, want ErrServiceUnavailable", status, id, err)
		}
		if code, _ := Status(err); code != 503 {
			t.Errorf("status %d answered %d", status, code)
		}
	}
}

func TestNetworkErrorRefusesAndIsNotCached(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc)
	token := oreillyToken(t, "a", clock.now().Add(time.Hour))
	svc.srv.Close()
	for i := 0; i < 2; i++ {
		id, err := v.Verify(bearer(token))
		if id != nil || !errors.Is(err, ErrServiceUnavailable) {
			t.Fatalf("got %v %v", id, err)
		}
	}
	if len(v.cache) != 0 {
		t.Error("an undecided answer was cached")
	}
}

func TestRedirectIsNotFollowedWithTheToken(t *testing.T) {
	elsewhere := newService(t, 200)
	redirector := httptest.NewTLSServer(http.RedirectHandler(elsewhere.srv.URL, http.StatusTemporaryRedirect))
	defer redirector.Close()
	v, err := NewServiceTokenVerifier(ServiceTokenConfig{Name: "x", VerifyURL: redirector.URL, SubjectClaim: subClaim}, testKey, redirector.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = v.Verify(bearer(jwt(t, map[string]any{subClaim: "a", expClaim: time.Now().Add(time.Hour).Unix()})))
	if !errors.Is(err, ErrServiceUnavailable) || elsewhere.calls.Load() != 0 {
		t.Fatalf("err %v, the redirect target was called %d times", err, elsewhere.calls.Load())
	}
}

func TestCachePositiveHitAndExpiry(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc)
	token := oreillyToken(t, "a", clock.now().Add(time.Hour))
	for i := 0; i < 3; i++ {
		if _, err := v.Verify(bearer(token)); err != nil {
			t.Fatal(err)
		}
	}
	if svc.calls.Load() != 1 {
		t.Fatalf("%d calls for one token", svc.calls.Load())
	}
	clock.advance(DefaultMaxCache - time.Second)
	if _, err := v.Verify(bearer(token)); err != nil || svc.calls.Load() != 1 {
		t.Fatalf("still within the default bound: %v, %d calls", err, svc.calls.Load())
	}
	clock.advance(2 * time.Second)
	if _, err := v.Verify(bearer(token)); err != nil || svc.calls.Load() != 2 {
		t.Fatalf("past the bound the service is asked again: %v, %d calls", err, svc.calls.Load())
	}
}

func TestCacheIsCappedByTheTokensOwnLife(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc)
	token := oreillyToken(t, "a", clock.now().Add(40*time.Second))
	if _, err := v.Verify(bearer(token)); err != nil {
		t.Fatal(err)
	}
	clock.advance(41 * time.Second)
	if _, err := v.Verify(bearer(token)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an expired token must be refused, got %v", err)
	}
	if svc.calls.Load() != 1 {
		t.Fatalf("an expired token reached the service: %d calls", svc.calls.Load())
	}
}

func TestCacheBoundIsConfigurable(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc, func(c *ServiceTokenConfig) { c.MaxCacheSeconds = 10 })
	token := oreillyToken(t, "a", clock.now().Add(time.Hour))
	_, _ = v.Verify(bearer(token))
	clock.advance(11 * time.Second)
	_, _ = v.Verify(bearer(token))
	if svc.calls.Load() != 2 {
		t.Fatalf("%d calls", svc.calls.Load())
	}
}

func TestNegativeCacheHoldsThirtySeconds(t *testing.T) {
	svc := newService(t, 401)
	v, clock := verifierFor(t, svc)
	token := oreillyToken(t, "a", clock.now().Add(time.Hour))
	for i := 0; i < 3; i++ {
		if _, err := v.Verify(bearer(token)); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	if svc.calls.Load() != 1 {
		t.Fatalf("garbage tokens made %d calls", svc.calls.Load())
	}
	clock.advance(NegativeCacheTTL + time.Second)
	svc.status.Store(200)
	if _, err := v.Verify(bearer(token)); err != nil || svc.calls.Load() != 2 {
		t.Fatalf("after the negative TTL the service is asked again: %v, %d", err, svc.calls.Load())
	}
}

func TestDifferentTokensDoNotShareACacheEntryAndKeyIsNotTheToken(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc)
	a := oreillyToken(t, "alice", clock.now().Add(time.Hour))
	b := oreillyToken(t, "bob", clock.now().Add(time.Hour))
	ida, _ := v.Verify(bearer(a))
	idb, _ := v.Verify(bearer(b))
	if ida.Subject != "alice" || idb.Subject != "bob" || svc.calls.Load() != 2 {
		t.Fatalf("%v %v %d", ida, idb, svc.calls.Load())
	}
	for k := range v.cache {
		if strings.Contains(k, a) || strings.Contains(k, b) || len(k) != 64 {
			t.Fatalf("cache key %q is not a keyed hash", k)
		}
	}
	other, _ := NewServiceTokenVerifier(v.cfg, []byte("another-install"), nil)
	if other.cacheKey(a) == v.cacheKey(a) {
		t.Error("the cache key does not depend on the install key")
	}
}

func TestCachedIdentityCannotBeChangedByACaller(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc)
	token := oreillyToken(t, "a", clock.now().Add(time.Hour))
	first, _ := v.Verify(bearer(token))
	first.Subject = "tampered"
	second, _ := v.Verify(bearer(token))
	if second.Subject != "a" {
		t.Fatal("a caller changed the cached identity")
	}
}

func TestShapeCheckRefusesBeforeAnyCall(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc, func(c *ServiceTokenConfig) { c.Audiences = []string{audValue} })
	future := clock.now().Add(time.Hour).Unix()
	enc := func(p string) string {
		return "h." + base64.RawURLEncoding.EncodeToString([]byte(p)) + ".s"
	}
	cases := map[string]string{
		"not a jwt":             "opaque-personal-access-token",
		"two parts":             "a.b",
		"four parts":            "a.b.c.d",
		"payload not base64":    "a.!!!.c",
		"payload not json":      enc("nope"),
		"payload not an object": enc("[1,2]"),
		"claim path missing":    jwt(t, map[string]any{subClaim: "x", expClaim: future, audClaim: audValue}),
		"claim parent not map":  jwt(t, map[string]any{upstreamClaims: "x", expClaim: future, audClaim: audValue}),
		"claim not a string":    jwt(t, map[string]any{upstreamClaims: map[string]any{subClaim: 7}, expClaim: future, audClaim: audValue}),
		"claim empty":           jwt(t, map[string]any{upstreamClaims: map[string]any{subClaim: ""}, expClaim: future, audClaim: audValue}),
		"claim oversize":        jwt(t, map[string]any{upstreamClaims: map[string]any{subClaim: strings.Repeat("x", maxSubjectLength+1)}, expClaim: future, audClaim: audValue}),
		"no exp":                jwt(t, map[string]any{upstreamClaims: map[string]any{subClaim: "a"}, audClaim: audValue}),
		"exp not a number":      jwt(t, map[string]any{upstreamClaims: map[string]any{subClaim: "a"}, expClaim: "soon", audClaim: audValue}),
		"expired":               oreillyToken(t, "a", clock.now().Add(-time.Second)),
		"audience mismatch":     jwt(t, map[string]any{upstreamClaims: map[string]any{subClaim: "a"}, expClaim: future, audClaim: "https://other/mcp"}),
		"too large":             "a." + strings.Repeat("A", maxTokenLength) + ".c",
		"kubernetes token":      jwt(t, map[string]any{subClaim: "system:serviceaccount:ns:sa", expClaim: future, audClaim: []string{"https://kubernetes.default.svc"}}),
	}
	for name, token := range cases {
		if _, err := v.Verify(bearer(token)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	if svc.calls.Load() != 0 {
		t.Fatalf("%d tokens that fail the shape check were sent to the service", svc.calls.Load())
	}
}

func TestAKubernetesTokenIsNeverSentOutEvenWithTheSubClaimAndNoAudiences(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc, func(c *ServiceTokenConfig) { c.SubjectClaim = subClaim })
	future := clock.now().Add(time.Hour).Unix()
	for name, payload := range map[string]map[string]any{
		"service account": {subClaim: "system:serviceaccount:ns:sa", expClaim: future, audClaim: []string{"api"}},
		"bound token":     {subClaim: "u", expClaim: future, "kubernetes.io": map[string]any{"namespace": "ns"}},
	} {
		if _, err := v.Verify(bearer(jwt(t, payload))); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if svc.calls.Load() != 0 {
		t.Fatalf("a Kubernetes token was sent to the service %d times", svc.calls.Load())
	}
}

func TestCacheHoldsNoTokenAndAHitGetsTheRequestsOwn(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc)
	token := oreillyToken(t, "a", clock.now().Add(time.Hour))
	_, _ = v.Verify(bearer(token))
	for _, e := range v.cache {
		if !e.invalid && !e.id.Credential.Empty() {
			t.Fatal("the cache holds the token")
		}
	}
	id, err := v.Verify(bearer(token))
	if err != nil || id.Credential.Token() != token {
		t.Fatalf("%v %v", id, err)
	}
}

func TestAudienceMayBeAList(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc, func(c *ServiceTokenConfig) { c.Audiences = []string{"b"} })
	token := jwt(t, map[string]any{upstreamClaims: map[string]any{subClaim: "a"}, expClaim: clock.now().Add(time.Hour).Unix(), audClaim: []string{"a", "b"}})
	if _, err := v.Verify(bearer(token)); err != nil {
		t.Fatal(err)
	}
}

func TestNoBearerIsNoCredential(t *testing.T) {
	v, _ := verifierFor(t, newService(t, 200))
	if _, err := v.Verify(bearer("")); !errors.Is(err, ErrNoCredential) {
		t.Fatal(err)
	}
	basic := httptest.NewRequest(http.MethodGet, "/", nil)
	basic.Header.Set("Authorization", "Basic abc")
	if _, err := v.Verify(basic); !errors.Is(err, ErrNoCredential) {
		t.Fatal(err)
	}
}

func TestVerifierNeedsTheInstallKey(t *testing.T) {
	_, err := NewServiceTokenVerifier(ServiceTokenConfig{Name: "x", VerifyURL: "https://x.example", SubjectClaim: subClaim}, nil, nil)
	if !errors.Is(err, ErrNoHashKey) {
		t.Fatal(err)
	}
}

func TestChainResolvesAKubernetesTokenWithoutAskingAServiceAndAPersonsTokenWithOneCall(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc)
	k8s := tokenClient(users(), nil)
	a := NewAuthenticator(Hasher{Key: testKey}, false, TokenReviewVerifier{Reviewer: Reviewer{Client: k8s}}, v)

	c, err := a.Authenticate(bearer("sa-token"))
	if err != nil || c.Principal.Kind != KindKubernetes || svc.calls.Load() != 0 {
		t.Fatalf("a Kubernetes token: %v %#v, %d service calls", err, c.Principal, svc.calls.Load())
	}
	// A token the API server does not know, that the service accepts, is a subscriber.
	c, err = a.Authenticate(bearer(oreillyToken(t, "acct-42", clock.now().Add(time.Hour))))
	if err != nil || c.Principal.Kind != KindSubscriber || svc.calls.Load() != 1 {
		t.Fatalf("a person's token: %v %#v, %d service calls", err, c.Principal, svc.calls.Load())
	}
	// A rejected Kubernetes-shaped token, and garbage, are never sent out and stay invalid.
	k8sShaped := jwt(t, map[string]any{subClaim: saUser, expClaim: clock.now().Add(time.Hour).Unix()})
	for _, token := range []string{k8sShaped, garbageText} {
		if _, err := a.Authenticate(bearer(token)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: %v", token, err)
		}
	}
	if svc.calls.Load() != 1 {
		t.Fatalf("a token that is not a person's was sent to the service: %d calls", svc.calls.Load())
	}
}

func TestChainStaysInvalidWhenTheServiceRefusesAndNeverBecomesAnonymous(t *testing.T) {
	svc := newService(t, 401)
	v, clock := verifierFor(t, svc)
	a := NewAuthenticator(Hasher{Key: testKey}, true, TokenReviewVerifier{Reviewer: Reviewer{Client: tokenClient(users(), nil)}}, v)
	if _, err := a.Authenticate(bearer(oreillyToken(t, "a", clock.now().Add(time.Hour)))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v", err)
	}
	svc.status.Store(503)
	if _, err := a.Authenticate(bearer(oreillyToken(t, "b", clock.now().Add(time.Hour)))); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("got %v", err)
	}
}

// Subscriber ids.

func TestSubscriberPrincipalIsKeyedAndStable(t *testing.T) {
	id := &Identity{Issuer: testIssuer, Subject: "acct-42"}
	h := Hasher{Key: testKey}
	p := PrincipalOf(h, id)
	if p.Kind != KindSubscriber || p.Issuer != testIssuer || p.ID != h.ID(testIssuer, "acct-42") || p.DisplayName != "" {
		t.Fatalf("principal %#v", p)
	}
	if again := PrincipalOf(Hasher{Key: testKey}, id); !SamePrincipal(p, again) {
		t.Error("the id is not stable for the same key")
	}
	if other := PrincipalOf(Hasher{Key: []byte("another-install")}, id); SamePrincipal(p, other) {
		t.Error("the id does not depend on the key")
	}
	assertIdDependsOnEverything(t, p, h)
}

func assertIdDependsOnEverything(t *testing.T, p bus.Principal, h Hasher) {
	t.Helper()
	if p.ID == (Hasher{}).ID(testIssuer, "acct-42") {
		t.Error("the id equals the guessable unkeyed hash")
	}
	if p.ID == h.ID("other", "acct-42") || p.ID == h.ID("oreilly", "acct-43") {
		t.Error("issuer and subject must both matter")
	}
	if strings.Contains(p.ID, "acct-42") {
		t.Error("the id contains the subject")
	}
}

func TestSubscriberWithoutAKeyIsRefusedAndMatchesNobody(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc)
	a := NewAuthenticator(Hasher{}, false, v)
	_, err := a.Authenticate(bearer(oreillyToken(t, "a", clock.now().Add(time.Hour))))
	if !errors.Is(err, ErrNoHashKey) {
		t.Fatalf("got %v", err)
	}
	if code, _ := Status(err); code != 503 {
		t.Errorf("answered %d", code)
	}
	p := PrincipalOf(Hasher{}, &Identity{Issuer: testIssuer, Subject: "a"})
	if p.ID != "" || SamePrincipal(p, p) {
		t.Errorf("an unnamed subscriber must match no one, not even itself: %#v", p)
	}
}

func TestKubernetesIdsAreUnchangedByAKey(t *testing.T) {
	id := &Identity{Issuer: IssuerKubernetes, Subject: humanUser}
	if PrincipalOf(Hasher{Key: testKey}, id).ID != (Hasher{}).ID(IssuerKubernetes, humanUser) {
		t.Fatal("a key changed the id of a Kubernetes caller, which would orphan recorded owners")
	}
}

func TestAuthenticatorNamesASubscriber(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc)
	a := NewAuthenticator(Hasher{Key: testKey}, false, v)
	c, err := a.Authenticate(bearer(oreillyToken(t, "acct-42", clock.now().Add(time.Hour))))
	if err != nil {
		t.Fatal(err)
	}
	if c.Principal.Kind != "subscriber" || c.Principal.ID != (Hasher{Key: testKey}).ID(testIssuer, "acct-42") || c.Anonymous() {
		t.Fatalf("%#v", c.Principal)
	}
}

// The token never appears.

func TestTheTokenNeverAppearsInLogsErrorsStringOrMarshalledOutput(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc)
	token := strings.TrimSuffix(oreillyToken(t, "acct-42", clock.now().Add(time.Hour)), "c2ln") + canary
	secret := canary
	id, err := v.Verify(bearer(token))
	if err != nil {
		t.Fatal(err)
	}
	caller := Caller{Principal: PrincipalOf(Hasher{Key: testKey}, id), Identity: id}
	outputs := make([]string, 0, 16)
	outputs = append(outputs,
		fmt.Sprint(id), fmt.Sprintf("%v %+v %#v %s %q %x", id, id, id, id, id, id),
		fmt.Sprintf("%v %+v %#v", *id, *id, *id),
		fmt.Sprintf("%v %+v %#v", caller, caller, caller),
		fmt.Sprintf("%v %+v %#v %s %q %x", id.Credential, id.Credential, id.Credential, id.Credential, id.Credential, id.Credential),
		id.String(), id.Credential.String(), id.Credential.GoString(),
	)
	for _, v := range []any{id, *id, caller, id.Credential, map[string]any{"c": id.Credential}} {
		j, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, string(j))
	}
	for i, out := range outputs {
		if strings.Contains(out, secret) {
			t.Errorf("output %d leaks the token: %s", i, out)
		}
	}
	for k := range v.cache {
		if strings.Contains(k, secret) {
			t.Error("the cache is keyed by the token")
		}
	}
}

func TestNoErrorCarriesTheToken(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc)
	token := strings.TrimSuffix(oreillyToken(t, "acct-42", clock.now().Add(time.Hour)), "c2ln") + canary
	secret := canary
	failing := newService(t, 500)
	fv, _ := verifierFor(t, failing)
	fv.now = v.now
	_, ferr := fv.Verify(bearer(token))
	refusing := newService(t, 401)
	rv, _ := verifierFor(t, refusing)
	rv.now = v.now
	_, ierr := rv.Verify(bearer(token))
	svc.srv.Close()
	v2, _ := verifierFor(t, svc)
	v2.now = v.now
	_, nerr := v2.Verify(bearer(token))
	for _, e := range []error{ferr, ierr, nerr} {
		if e == nil || strings.Contains(e.Error(), secret) {
			t.Errorf("error missing or leaks the token: %v", e)
		}
	}
}

func TestIdentityNamesNoSubject(t *testing.T) {
	id := Identity{Issuer: testIssuer, Subject: "person@example.com", Credential: NewCredential(canary)}
	for _, out := range []string{fmt.Sprint(id), fmt.Sprintf("%+v", id), fmt.Sprintf("%#v", &id)} {
		if strings.Contains(out, "person@example.com") || strings.Contains(out, canary) {
			t.Errorf("leaks: %s", out)
		}
	}
	j, _ := json.Marshal(id)
	if strings.Contains(string(j), canary) {
		t.Errorf("marshalled: %s", j)
	}
	if NewCredential(canary).Token() != canary || !(Credential{}).Empty() {
		t.Error("the token is not available to the call that needs it")
	}
}

// Parsing the payload and the claim path, with bad input.

func FuzzParseTokenClaims(f *testing.F) {
	f.Add("a.eyJzdWIiOiJ4In0.c", "sub")
	f.Add("a.e30.c", "a.b.c")
	f.Add("a.W10.c", "")
	f.Add("a."+strings.Repeat("W", 100)+".c", "x..y")
	f.Fuzz(func(t *testing.T, token, path string) {
		claims, ok := parseTokenClaims(token)
		if !ok {
			return
		}
		if s, ok := claims.String(path); ok && (s == "" || len(s) > maxSubjectLength) {
			t.Fatalf("a claim outside the bounds was returned: %q", s)
		}
		_, _ = claims.Expiry()
		_ = claims.Audiences()
	})
}

func TestDeeplyNestedPayloadIsRefusedWithoutPanic(t *testing.T) {
	deep := strings.Repeat(`{"a":`, 1500) + "1" + strings.Repeat("}", 1500)
	token := "h." + base64.RawURLEncoding.EncodeToString([]byte(deep)) + ".s"
	claims, ok := parseTokenClaims(token)
	if ok {
		if _, found := claims.String("a.a.a"); found {
			t.Fatal("unexpected string")
		}
	}
}

// Configuration.

func TestParseServiceTokens(t *testing.T) {
	good := `[{"name":"oreilly","verifyURL":"https://api.oreilly.com/api/search/v1/mcp","subjectClaim":"upstream_claims.sub","passToTools":true}]`
	list, err := ParseServiceTokens(good)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	c := list[0]
	if c.VerifyMethod != VerifyMcpInitialize || c.Authorize != AuthorizeAnyAccepted || c.maxCache() != DefaultMaxCache || !c.PassToTools {
		t.Fatalf("defaults %#v", c)
	}
	if none, err := ParseServiceTokens(""); err != nil || none != nil {
		t.Fatalf("empty must be no verifier: %v %v", none, err)
	}
}

func TestParseServiceTokensRefusesWhatTheCRDRefuses(t *testing.T) {
	base := func(extra string) string {
		return `[{"name":"o","verifyURL":"https://x.example","subjectClaim":"` + subClaim + `"` + extra + `}]`
	}
	bad := map[string]string{
		"not json":          "oreilly",
		"not a list":        `{"name":"o"}`,
		"unknown field":     base(`,"extra":1`),
		"http url":          `[{"name":"o","verifyURL":"http://x.example","subjectClaim":"` + subClaim + `"}]`,
		"no url":            `[{"name":"o","subjectClaim":"` + subClaim + `"}]`,
		"bad name":          `[{"name":"O_o","verifyURL":"https://x.example","subjectClaim":"` + subClaim + `"}]`,
		"bad claim":         `[{"name":"o","verifyURL":"https://x.example","subjectClaim":"a..b"}]`,
		"no claim":          `[{"name":"o","verifyURL":"https://x.example"}]`,
		"cache too long":    base(`,"maxCacheSeconds":901`),
		"cache negative":    base(`,"maxCacheSeconds":-1`),
		"unknown method":    base(`,"verifyMethod":"HttpGet"`),
		"unknown authorize": base(`,"authorize":"Everyone"`),
		"subjects without":  base(`,"subjects":["a"]`),
		"Subjects empty":    base(`,"authorize":"Subjects"`),
		"reserved name":     `[{"name":"kubernetes","verifyURL":"https://x.example","subjectClaim":"s"}]`,
		"trailing data":     base("") + ` extra`,
		"two without aud":   `[{"name":"a","verifyURL":"https://x.example","subjectClaim":"s"},{"name":"b","verifyURL":"https://y.example","subjectClaim":"s","audiences":["b"]}]`,
		"duplicate name":    `[{"name":"o","verifyURL":"https://x.example","subjectClaim":"s"},{"name":"o","verifyURL":"https://y.example","subjectClaim":"s"}]`,
	}
	for name, raw := range bad {
		if _, err := ParseServiceTokens(raw); !errors.Is(err, ErrServiceTokenConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestFromEnvServiceTokens(t *testing.T) {
	env := map[string]string{
		EnvEnforce: EnvTrue, EnvTokenReview: "false", EnvAllowAnonymous: "false",
		EnvServiceTokens: `[{"name":"oreilly","verifyURL":"https://x.example","subjectClaim":"upstream_claims.sub"}]`,
	}
	get := func(k string) string { return env[k] }
	if _, err := FromEnv(get, nil); !errors.Is(err, ErrNoHashKey) {
		t.Fatalf("without the key the door must not start: %v", err)
	}
	env[EnvHashKey] = "install-hash-key"
	set, err := FromEnv(get, nil)
	if err != nil {
		t.Fatal(err)
	}
	if set.Authenticator == nil || !set.Authenticator.Enforcing() || set.Authorizer == nil {
		t.Fatalf("%#v", set)
	}
	env[EnvServiceTokens] = garbageText
	if _, err := FromEnv(get, nil); !errors.Is(err, ErrServiceTokenConfig) {
		t.Fatalf("a bad list must stop the door: %v", err)
	}
	env[EnvServiceTokens] = ""
	delete(env, EnvHashKey)
	set, err = FromEnv(get, nil)
	if err != nil || set.Authorizer != nil {
		t.Fatalf("no service tokens is the old behavior: %v %#v", err, set)
	}
}

func TestFromEnvWithoutEnforceIgnoresServiceTokens(t *testing.T) {
	get := func(k string) string {
		if k == EnvServiceTokens {
			return garbageText
		}
		return ""
	}
	set, err := FromEnv(get, nil)
	if err != nil || set.Authenticator.Enforcing() {
		t.Fatal(err)
	}
}
