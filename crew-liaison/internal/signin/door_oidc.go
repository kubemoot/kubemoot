package signin

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// identityProvider is the seam where a person proves who they are. This version has one, an
// OpenID Connect issuer the install names.
type identityProvider interface {
	// loginURL is where to send the person's browser, with the door's own state, nonce and PKCE.
	loginURL(ctx context.Context, state, nonce, verifier, redirect string) (string, error)
	// finish trades the code for an ID token, checks it, and says who the person is.
	finish(ctx context.Context, code, verifier, nonce, redirect string) (person, error)
}

// idTokenAlgs are the signature algorithms an ID token may use: the asymmetric ones issuers sign with.
var idTokenAlgs = []string{oidc.RS256, oidc.ES256, oidc.PS256, oidc.EdDSA}

const (
	maxClaimGroups   = 64
	maxClaimLength   = 256
	maxDisplayLength = 100
	oidcWait         = 15 * time.Second
)

// oidcProvider is the door as a relying party: authorization code with PKCE S256, an ID token
// checked by the maintained go-oidc library (signature against the issuer's keys, issuer,
// audience, expiry) and a nonce that must match the one this login sent.
type oidcProvider struct {
	cfg    OIDCConfig
	client *http.Client

	mu       sync.Mutex
	provider *oidc.Provider
}

func newOIDCProvider(cfg *DoorConfig) *oidcProvider {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: oidcWait}
	}
	return &oidcProvider{cfg: cfg.OIDC, client: client}
}

// discover reads the issuer's metadata once it can. The issuer being down at start must not keep
// the liaison down, so this runs on the first sign-in and again until it works.
func (p *oidcProvider) discover() (*oidc.Provider, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.provider != nil {
		return p.provider, nil
	}
	ctx := oidc.ClientContext(context.Background(), p.client)
	prov, err := oidc.NewProvider(ctx, p.cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("the identity provider could not be reached: %w", err)
	}
	p.provider = prov
	return prov, nil
}

func (p *oidcProvider) oauth(prov *oidc.Provider, redirect string) *oauth2.Config {
	return &oauth2.Config{
		ClientID: p.cfg.ClientID, ClientSecret: p.cfg.ClientSecret, Endpoint: prov.Endpoint(),
		RedirectURL: redirect, Scopes: strings.Fields(p.cfg.Scopes),
	}
}

func (p *oidcProvider) loginURL(_ context.Context, state, nonce, verifier, redirect string) (string, error) {
	prov, err := p.discover()
	if err != nil {
		return "", err
	}
	return p.oauth(prov, redirect).AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
}

var errNoIDToken = errors.New("the identity provider returned no ID token")

func (p *oidcProvider) finish(ctx context.Context, code, verifier, nonce, redirect string) (person, error) {
	prov, err := p.discover()
	if err != nil {
		return person{}, err
	}
	ctx = oidc.ClientContext(ctx, p.client)
	tok, err := p.oauth(prov, redirect).Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return person{}, errors.New("the identity provider refused the code")
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return person{}, errNoIDToken
	}
	idVerifier := prov.Verifier(&oidc.Config{ClientID: p.cfg.ClientID, SupportedSigningAlgs: idTokenAlgs})
	idToken, err := idVerifier.Verify(ctx, raw)
	if err != nil {
		return person{}, errors.New("the ID token is not valid")
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(nonce)) != 1 {
		return person{}, errors.New("the ID token answers another sign-in")
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return person{}, errors.New("the ID token's claims could not be read")
	}
	return personFrom(p.cfg, claims)
}

// personFrom names the person as Kubernetes RBAC will know them: the user and every group
// carry the install's prefix, so an identity provider cannot name a Kubernetes system user or
// a group an administrator bound for another purpose.
func personFrom(cfg OIDCConfig, claims map[string]any) (person, error) {
	sub, _ := claims["sub"].(string)
	if sub == "" || len(sub) > maxClaimLength {
		return person{}, errors.New("the ID token has no usable subject")
	}
	who := person{User: cfg.UserPrefix + sub, Name: claimName(claims)}
	if strings.HasPrefix(who.User, "system:") {
		return person{}, errors.New("the subject would name a Kubernetes system user")
	}
	for _, g := range claimStrings(claims[cfg.GroupsClaim]) {
		if len(who.Groups) == maxClaimGroups {
			break
		}
		if group := cfg.UserPrefix + g; len(group) <= maxClaimLength && !strings.HasPrefix(group, "system:") {
			who.Groups = append(who.Groups, group)
		}
	}
	return who, nil
}

// claimStrings reads a claim that is a string or a list of strings.
func claimStrings(v any) []string {
	switch c := v.(type) {
	case string:
		return []string{c}
	case []any:
		var out []string
		for _, item := range c {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// claimName is what to call the person on the consent page; it is never used to decide anything.
func claimName(claims map[string]any) string {
	for _, key := range []string{"name", "preferred_username", "email"} {
		if s, _ := claims[key].(string); s != "" {
			return truncate(s, maxDisplayLength)
		}
	}
	return ""
}
