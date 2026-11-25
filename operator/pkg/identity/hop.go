/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"
)

// A trusted hop is a door in front of the crew that signs in people itself (the crew liaison
// with its own sign-in). It does not forward the person's token. For each request it signs a
// short assertion of who the person is, and the crew's gateway verifies it against the hop's
// public key, which the Crew lists in spec.discussion.access.trustedHops. The gateway then
// applies the crew's askers rule (see AskerAuthorizer) to the person asserted. Only the
// kubernetesRBAC rule makes the person a Kubernetes user and asks the API server.
const (
	// EnvTrustedHops is the JSON list of TrustedHopConfig the operator writes for a gateway.
	EnvTrustedHops = "ACCESS_TRUSTED_HOPS"
	// HopTokenType is the `typ` header of an assertion. A bearer token without it is not
	// read as an assertion, so a Kubernetes token never reaches this verifier's key check.
	HopTokenType = "kubemoot-hop+jwt"
	// HopLifetime is the longest an assertion may live, from issue to expiry.
	HopLifetime = 60 * time.Second
	// hopSkew is the clock difference between the signer and the gateway that is tolerated.
	hopSkew = 5 * time.Second

	maxHopSubject = 256
	maxHopGroups  = 64
	maxHopKeys    = 4
	maxHops       = 4
	systemPrefix  = "system:"
)

// ErrHopConfig marks an invalid ACCESS_TRUSTED_HOPS value.
var ErrHopConfig = errors.New("invalid trusted hop configuration")

// TrustedHopConfig is one door the crew trusts to say who is asking. The JSON names are the
// ones of Crew.spec.discussion.access.trustedHops.
type TrustedHopConfig struct {
	// Name is the assertion's issuer (iss).
	Name string `json:"name"`
	// Audience is what the assertion's aud must be: the one gateway it was signed for.
	Audience string `json:"audience"`
	// PublicKeys are PEM encoded PKIX ECDSA P-256 keys; several let a key rotate.
	PublicKeys []string `json:"publicKeys"`
	// KubernetesUser makes the asserted person a Kubernetes user and groups, which RBAC then
	// decides. Off, the person is only the sign-in provider's, issued by the hop. The crew's
	// askers rule sets it: only the kubernetesRBAC kind maps a person to Kubernetes.
	KubernetesUser bool `json:"kubernetesUser,omitempty"`
}

// HopAudience is the audience of an assertion for the gateway of one crew.
func HopAudience(namespace, crew string) string {
	return "kubemoot-crew/" + namespace + "/" + crew
}

// ParseTrustedHops reads the JSON list of ACCESS_TRUSTED_HOPS. An empty value is no hops.
func ParseTrustedHops(raw string) ([]TrustedHopConfig, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var list []TrustedHopConfig
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("%w: %s must be one JSON list", ErrHopConfig, EnvTrustedHops)
	}
	if len(list) > maxHops {
		return nil, fmt.Errorf("%w: at most %d hops", ErrHopConfig, maxHops)
	}
	return list, nil
}

// KeyThumbprint is the key id of an ECDSA signing key: the RFC 7638 thumbprint of its public
// half, so a signer and a verifier name one key the same way without a shared list. The door's
// access tokens use it too.
func KeyThumbprint(pub *ecdsa.PublicKey) (string, error) {
	sum, err := (&jose.JSONWebKey{Key: pub}).Thumbprint(crypto.SHA256)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sum[:16]), nil
}

// ParseHopPublicKey reads one PEM encoded PKIX ECDSA P-256 public key.
func ParseHopPublicKey(pemText string) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("not PEM")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := key.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, errors.New("not an ECDSA P-256 public key")
	}
	return pub, nil
}

// hopTrust is one configured hop with its keys by id.
type hopTrust struct {
	issuer   string
	audience string
	keys     map[string]*ecdsa.PublicKey
}

// HopVerifier verifies the assertions of the configured hops. Identity comes only from a
// signature checked against a configured key, never from a header or a body.
type HopVerifier struct {
	hops map[string]hopTrust
	now  func() time.Time
}

// NewHopVerifier builds the verifier of cfgs; now is the clock, nil for the system's.
func NewHopVerifier(cfgs []TrustedHopConfig, now func() time.Time) (*HopVerifier, error) {
	if now == nil {
		now = time.Now
	}
	v := &HopVerifier{hops: map[string]hopTrust{}, now: now}
	for _, c := range cfgs {
		trust, err := newHopTrust(c)
		if err != nil {
			return nil, err
		}
		if _, dup := v.hops[c.Name]; dup {
			return nil, fmt.Errorf("%w: hop %q is listed twice", ErrHopConfig, c.Name)
		}
		v.hops[c.Name] = trust
	}
	return v, nil
}

func newHopTrust(c TrustedHopConfig) (hopTrust, error) {
	if c.Name == "" || c.Audience == "" || len(c.PublicKeys) == 0 || len(c.PublicKeys) > maxHopKeys {
		return hopTrust{}, fmt.Errorf("%w: a hop needs a name, an audience and one to %d public keys",
			ErrHopConfig, maxHopKeys)
	}
	trust := hopTrust{issuer: HopIssuerPrefix + c.Name, audience: c.Audience, keys: map[string]*ecdsa.PublicKey{}}
	for _, text := range c.PublicKeys {
		pub, err := ParseHopPublicKey(text)
		if err != nil {
			return hopTrust{}, fmt.Errorf("%w: hop %q: %w", ErrHopConfig, c.Name, err)
		}
		kid, err := KeyThumbprint(pub)
		if err != nil {
			return hopTrust{}, fmt.Errorf("%w: hop %q: %w", ErrHopConfig, c.Name, err)
		}
		trust.keys[kid] = pub
	}
	if c.KubernetesUser {
		trust.issuer = IssuerKubernetes
	}
	return trust, nil
}

// hopClaims is the payload of an assertion beyond the registered claims.
type hopClaims struct {
	josejwt.Claims
	Groups []string `json:"groups,omitempty"`
}

// Verify reads the request's bearer token as an assertion. A token that is not an assertion
// at all (not a JWT, or without the assertion type) is ErrNoCredential so the next verifier
// tries it; an assertion that is not valid is ErrInvalid, which the chain never downgrades: later verifiers
// may still be asked, and the answer stays ErrInvalid.
func (v *HopVerifier) Verify(r *http.Request) (*Identity, error) {
	token := BearerToken(r)
	if token == "" {
		return nil, ErrNoCredential
	}
	parsed, err := josejwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil || len(parsed.Headers) != 1 || parsed.Headers[0].ExtraHeaders[jose.HeaderType] != HopTokenType {
		return nil, ErrNoCredential
	}
	return v.check(parsed)
}

func (v *HopVerifier) check(parsed *josejwt.JSONWebToken) (*Identity, error) {
	var unverified josejwt.Claims
	if err := parsed.UnsafeClaimsWithoutVerification(&unverified); err != nil {
		return nil, ErrInvalid
	}
	trust, ok := v.hops[unverified.Issuer]
	if !ok {
		return nil, ErrInvalid
	}
	key, ok := trust.keys[parsed.Headers[0].KeyID]
	if !ok {
		return nil, ErrInvalid
	}
	var claims hopClaims
	if err := parsed.Claims(key, &claims); err != nil {
		return nil, ErrInvalid
	}
	return v.identityOf(trust, claims)
}

// identityOf applies the rules every assertion meets: issuer, audience, time, a lifetime of
// at most a minute, an id, and a user and groups that are not a Kubernetes system identity.
func (v *HopVerifier) identityOf(trust hopTrust, c hopClaims) (*Identity, error) {
	now := v.now()
	err := c.ValidateWithLeeway(josejwt.Expected{AnyAudience: []string{trust.audience}, Time: now}, hopSkew)
	switch {
	case err != nil, c.IssuedAt == nil, c.Expiry == nil, c.ID == "":
		return nil, ErrInvalid
	case !now.Before(c.Expiry.Time()):
		// The skew only lets a signer's clock run a little ahead; an assertion never lives past its minute.
		return nil, ErrInvalid
	case c.Expiry.Time().Sub(c.IssuedAt.Time()) > HopLifetime, c.IssuedAt.Time().After(now.Add(hopSkew)):
		return nil, ErrInvalid
	case !hopName(c.Subject), len(c.Groups) > maxHopGroups:
		return nil, ErrInvalid
	}
	for _, g := range c.Groups {
		if !hopName(g) {
			return nil, ErrInvalid
		}
	}
	return &Identity{Issuer: trust.issuer, Subject: c.Subject, Groups: c.Groups}, nil
}

// checkAsserted refuses, at the signer, what a gateway would refuse: a user or group a gateway
// will not accept would otherwise fail on every crew with no explanation.
func checkAsserted(user string, groups []string) error {
	if !hopName(user) || len(groups) > maxHopGroups {
		return errors.New("the user or the number of groups cannot be asserted")
	}
	for _, g := range groups {
		if !hopName(g) {
			return errors.New("a group name cannot be asserted")
		}
	}
	return nil
}

// hopName reports whether s may be asserted as a user or group: present, bounded, and not a
// Kubernetes system identity, so a door whose key is stolen still cannot name system:masters.
func hopName(s string) bool {
	return s != "" && len(s) <= maxHopSubject && !strings.HasPrefix(s, systemPrefix)
}

// HopSigner signs assertions for one issuer with one key.
type HopSigner struct {
	signer jose.Signer
	issuer string
	now    func() time.Time
}

// NewHopSigner returns a signer of assertions issued as issuer; now is the clock, nil for
// the system's.
func NewHopSigner(key *ecdsa.PrivateKey, issuer string, now func() time.Time) (*HopSigner, error) {
	kid, err := KeyThumbprint(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	opts := (&jose.SignerOptions{}).WithType(HopTokenType).WithHeader(jose.HeaderKey("kid"), kid)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, opts)
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &HopSigner{signer: signer, issuer: issuer, now: now}, nil
}

// Assert signs the assertion that user, in groups, is asking the gateway named by audience. It
// has a fresh random id and lives HopLifetime.
func (s *HopSigner) Assert(audience, user string, groups []string) (string, error) {
	if err := checkAsserted(user, groups); err != nil {
		return "", err
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	now := s.now()
	return josejwt.Signed(s.signer).Claims(hopClaims{
		Claims: josejwt.Claims{
			Issuer: s.issuer, Subject: user, Audience: josejwt.Audience{audience}, ID: hex.EncodeToString(id),
			IssuedAt: josejwt.NewNumericDate(now), Expiry: josejwt.NewNumericDate(now.Add(HopLifetime)),
		},
		Groups: groups,
	}).Serialize()
}
