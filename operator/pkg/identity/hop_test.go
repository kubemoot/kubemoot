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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"
	"k8s.io/client-go/kubernetes"
)

const (
	hopName1 = "door"
	hopUser  = "oidc:jane"
	hopTeam  = "team-a"
	hopAud   = "kubemoot-crew/team/pilot"
)

var hopNow = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

func hopKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func publicPEM(t *testing.T, k *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

type hopRig struct {
	key      *ecdsa.PrivateKey
	signer   *HopSigner
	verifier *HopVerifier
	now      time.Time
}

func newHopRig(t *testing.T) *hopRig {
	t.Helper()
	rig := &hopRig{key: hopKey(t), now: hopNow}
	clock := func() time.Time { return rig.now }
	var err error
	if rig.signer, err = NewHopSigner(rig.key, hopName1, clock); err != nil {
		t.Fatal(err)
	}
	cfg := []TrustedHopConfig{{Name: hopName1, Audience: hopAud, PublicKeys: []string{publicPEM(t, rig.key)}}}
	if rig.verifier, err = NewHopVerifier(cfg, clock); err != nil {
		t.Fatal(err)
	}
	return rig
}

func hopRequest(token string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func TestHopAssertionIsAccepted(t *testing.T) {
	rig := newHopRig(t)
	token, err := rig.signer.Assert(hopAud, hopUser, []string{hopTeam})
	if err != nil {
		t.Fatal(err)
	}
	id, err := rig.verifier.Verify(hopRequest(token))
	if err != nil {
		t.Fatalf("a good assertion was refused: %v", err)
	}
	if id.Issuer != IssuerKubernetes || id.Subject != hopUser || len(id.Groups) != 1 || id.Groups[0] != hopTeam {
		t.Fatalf("identity = %+v", id)
	}
}

func TestHopAssertionRefusals(t *testing.T) {
	rig := newHopRig(t)
	good := func() string {
		tok, err := rig.signer.Assert(hopAud, hopUser, nil)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	cases := map[string]func() string{
		"expired": func() string {
			tok := good()
			rig.now = rig.now.Add(HopLifetime + time.Second)
			return tok
		},
		"replayed a second past its minute": func() string {
			tok := good()
			rig.now = rig.now.Add(HopLifetime)
			return tok
		},
		"another gateway's audience": func() string {
			tok, _ := rig.signer.Assert("kubemoot-crew/other/crew", hopUser, nil)
			return tok
		},
		"signed by another key": func() string {
			other, _ := NewHopSigner(hopKey(t), hopName1, func() time.Time { return rig.now })
			tok, _ := other.Assert(hopAud, hopUser, nil)
			return tok
		},
		"another issuer": func() string {
			other, _ := NewHopSigner(rig.key, "someone", func() time.Time { return rig.now })
			tok, _ := other.Assert(hopAud, hopUser, nil)
			return tok
		},
		"a system user": func() string {
			c := baseClaims("x", time.Minute)
			c.Subject = "system:admin"
			return craftHop(t, rig, hopClaims{Claims: c})
		},
		"a system group": func() string {
			return craftHop(t, rig, hopClaims{Claims: baseClaims("x", time.Minute), Groups: []string{"system:masters"}})
		},
		"no subject": func() string {
			c := baseClaims("x", time.Minute)
			c.Subject = ""
			return craftHop(t, rig, hopClaims{Claims: c})
		},
		"no id": func() string {
			return craftHop(t, rig, hopClaims{Claims: baseClaims("", time.Minute)})
		},
		"a lifetime over a minute": func() string {
			return craftHop(t, rig, hopClaims{Claims: baseClaims("x", 2*time.Minute)})
		},
		"an unsigned token": func() string {
			return "eyJhbGciOiJub25lIiwidHlwIjoia3ViZW1vb3QtaG9wK2p3dCJ9.e30."
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			rig.now = hopNow
			tok := build()
			_, err := rig.verifier.Verify(hopRequest(tok))
			want := ErrInvalid
			if name == "an unsigned token" {
				want = ErrNoCredential // not an assertion at all: left for the next verifier
			}
			if !errors.Is(err, want) {
				t.Fatalf("err = %v, want %v", err, want)
			}
		})
	}
}

func baseClaims(id string, lifetime time.Duration) josejwt.Claims {
	return josejwt.Claims{
		Issuer: hopName1, Subject: hopUser, Audience: josejwt.Audience{hopAud}, ID: id,
		IssuedAt: josejwt.NewNumericDate(hopNow), Expiry: josejwt.NewNumericDate(hopNow.Add(lifetime)),
	}
}

// craftHop signs claims the signer would not write, with the right key and header.
func craftHop(t *testing.T, rig *hopRig, claims hopClaims) string {
	t.Helper()
	kid, _ := KeyThumbprint(&rig.key.PublicKey)
	opts := (&jose.SignerOptions{}).WithType(HopTokenType).WithHeader(jose.HeaderKey("kid"), kid)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: rig.key}, opts)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := josejwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestAnythingButAnAssertionIsLeftForTheNextVerifier(t *testing.T) {
	rig := newHopRig(t)
	for name, token := range map[string]string{
		"no credential":          "",
		"an opaque token":        "abc123",
		"a kubernetes style JWT": "eyJhbGciOiJSUzI1NiJ9.e30.c2ln",
	} {
		if _, err := rig.verifier.Verify(hopRequest(token)); !errors.Is(err, ErrNoCredential) {
			t.Errorf("%s: err = %v, want ErrNoCredential", name, err)
		}
	}
}

func TestHopConfigRefusals(t *testing.T) {
	good := publicPEM(t, hopKey(t))
	for name, cfg := range map[string][]TrustedHopConfig{
		"no key":        {{Name: "a", Audience: hopAud}},
		"no audience":   {{Name: "a", PublicKeys: []string{good}}},
		"a bad key":     {{Name: "a", Audience: hopAud, PublicKeys: []string{"nope"}}},
		"listed twice":  {{Name: "a", Audience: hopAud, PublicKeys: []string{good}}, {Name: "a", Audience: hopAud, PublicKeys: []string{good}}},
		"too many keys": {{Name: "a", Audience: hopAud, PublicKeys: []string{good, good, good, good, good}}},
	} {
		if _, err := NewHopVerifier(cfg, nil); !errors.Is(err, ErrHopConfig) {
			t.Errorf("%s: err = %v, want ErrHopConfig", name, err)
		}
	}
	if _, err := ParseTrustedHops("{not a list"); !errors.Is(err, ErrHopConfig) {
		t.Errorf("a malformed list: %v", err)
	}
	if list, err := ParseTrustedHops(""); err != nil || list != nil {
		t.Errorf("empty = %v, %v", list, err)
	}
}

func TestRotatedKeysBothVerify(t *testing.T) {
	oldKey, newKey := hopKey(t), hopKey(t)
	cfg := []TrustedHopConfig{{Name: hopName1, Audience: hopAud, PublicKeys: []string{publicPEM(t, oldKey), publicPEM(t, newKey)}}}
	v, err := NewHopVerifier(cfg, func() time.Time { return hopNow })
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []*ecdsa.PrivateKey{oldKey, newKey} {
		s, _ := NewHopSigner(k, hopName1, func() time.Time { return hopNow })
		tok, _ := s.Assert(hopAud, hopUser, nil)
		if _, err := v.Verify(hopRequest(tok)); err != nil {
			t.Fatalf("a rotated key was refused: %v", err)
		}
	}
}

// TestFromEnvTrustedHopAsksRBACAboutTheAssertedUser is the path of a request: the assertion
// is verified, then the SubjectAccessReview names the asserted user and groups.
func TestFromEnvTrustedHopAsksRBACAboutTheAssertedUser(t *testing.T) {
	key := hopKey(t)
	cfg, _ := json.Marshal([]TrustedHopConfig{{Name: hopName1, Audience: hopAud, PublicKeys: []string{publicPEM(t, key)}}})
	client := tokenClient(nil, map[string]bool{"oidc:jane get crews": true})
	env := map[string]string{EnvEnforce: EnvTrue, EnvTrustedHops: string(cfg)}
	settings, err := FromEnv(func(k string) string { return env[k] },
		func() (kubernetes.Interface, error) { return client, nil })
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := NewHopSigner(key, hopName1, nil)
	for user, want := range map[string]bool{hopUser: true, "oidc:joe": false} {
		token, _ := signer.Assert(hopAud, user, []string{hopTeam})
		caller, err := settings.Authenticator.Authenticate(hopRequest(token))
		if err != nil {
			t.Fatalf("%s: %v", user, err)
		}
		got, err := settings.Authorizer.Allowed(context.Background(), caller.Identity, UseAccess(teamNS, crewName))
		if err != nil || got != want {
			t.Errorf("%s allowed = %v, %v; want %v", user, got, err, want)
		}
		if caller.Principal.Kind != "kubernetes" || caller.Principal.ID == "" {
			t.Errorf("principal = %+v", caller.Principal)
		}
	}
	// A Kubernetes token still reaches its own verifier when token review is on: the hop
	// verifier leaves it alone.
	if _, err := settings.Authenticator.Authenticate(hopRequest("abc")); !errors.Is(err, ErrRequired) {
		t.Errorf("a stranger's token: %v", err)
	}
}

func TestTheSignerRefusesWhatAGatewayWouldRefuse(t *testing.T) {
	rig := newHopRig(t)
	many := make([]string, maxHopGroups+1)
	for i := range many {
		many[i] = "g"
	}
	for name, call := range map[string]func() (string, error){
		"a system user":    func() (string, error) { return rig.signer.Assert(hopAud, "system:admin", nil) },
		"a system group":   func() (string, error) { return rig.signer.Assert(hopAud, hopUser, []string{"system:masters"}) },
		"too many groups":  func() (string, error) { return rig.signer.Assert(hopAud, hopUser, many) },
		"an empty subject": func() (string, error) { return rig.signer.Assert(hopAud, "", nil) },
	} {
		if tok, err := call(); err == nil || tok != "" {
			t.Errorf("%s was signed", name)
		}
	}
}
