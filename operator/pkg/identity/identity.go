/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package identity is the one seam through which a door of a crew (the discussion
// gateway, the crew liaison) learns who is asking. A Verifier checks one kind of
// credential; an Authenticator tries its verifiers in order and turns the first success
// into a bus.Principal, hashing the subject so the raw value never leaves the door.
//
// The verifiers in this package are the anonymous caller (no verifier at all: an
// Authenticator that allows anonymous callers) and the Kubernetes bearer token
// (TokenReviewVerifier). A trusted hop in front of Kubemoot (an adopted gateway, verified
// by mTLS or its signed token) and a JWT verifier are the planned next verifiers; each
// implements Verifier and is appended to the list, and nothing else changes.
// Identity is never read from a request body, a header a client can set, or message text.
package identity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

// The kinds of principal, as the bus schema names them.
const (
	KindAnonymous  = "anonymous"
	KindKubernetes = "kubernetes"
	// KindSubscriber is a person a service the crew trusts vouched for; its issuer is the
	// name of the verifier that asked that service.
	KindSubscriber = "subscriber"
	// IssuerKubernetes is the issuer of a principal the Kubernetes API server vouched for.
	IssuerKubernetes = "kubernetes"
)

// Errors a Verifier and an Authenticator return; a door maps each to a status with Status.
var (
	// ErrNoCredential means the request carries no credential of the verifier's kind, so
	// the next verifier is tried.
	ErrNoCredential = errors.New("no credential")
	// ErrInvalid means a credential was presented and is not valid. It is never downgraded
	// to an anonymous caller.
	ErrInvalid = errors.New("a valid bearer token is required")
	// ErrRequired means the crew accepts no anonymous caller and the request had no credential.
	ErrRequired = errors.New("this crew requires a credential: send a Kubernetes token as 'Authorization: Bearer <token>'")
	// ErrNoHashKey means a subscriber was verified on a door that has no install hash key.
	// A subscriber's subject is not public, so it is never named without the key.
	ErrNoHashKey = errors.New("subscriber ids need the install hash key")
	// ErrForbidden means the caller is known and may not make the access.
	ErrForbidden = errors.New("the caller may not make this request")
)

// Identity is what a verifier proved. It stays inside the process that verified it:
// the subject and the groups are used for the access check and the audit record, and
// only the Principal built from it travels.
type Identity struct {
	Issuer  string
	Subject string
	UID     string
	Groups  []string
	Extra   map[string][]string
	// Credential is the token the verifier accepted, kept for a door that hands it to a
	// tool for the length of one turn. Only a service token verifier sets it. It prints and
	// marshals redacted, so logging an Identity cannot expose it.
	Credential Credential
}

// String names the identity without its subject or credential.
func (i Identity) String() string { return "identity{issuer: " + i.Issuer + ", credential: redacted}" }

// GoString is String, so %#v is as safe as %v.
func (i Identity) GoString() string { return i.String() }

// IsWorkload reports whether the subject is a ServiceAccount, whose name is safe to show.
func (i *Identity) IsWorkload() bool {
	return i.Issuer == IssuerKubernetes && strings.HasPrefix(i.Subject, "system:serviceaccount:")
}

// Verifier checks one kind of credential on a request. It returns ErrNoCredential when the
// request has none of its kind, ErrInvalid when it has one that is not valid, and any other
// error when it could not decide (the door answers 503).
type Verifier interface {
	Verify(r *http.Request) (*Identity, error)
}

// Caller is the verified asker. Identity is nil for an anonymous caller.
type Caller struct {
	Principal bus.Principal
	Identity  *Identity
}

// Anonymous reports whether the caller presented no credential.
func (c Caller) Anonymous() bool { return c.Identity == nil }

// AnonymousCaller is the caller of a crew that accepts anonymous callers.
func AnonymousCaller() Caller {
	return Caller{Principal: bus.Principal{Kind: bus.PrincipalKindAnonymous}}
}

// Authenticator tries its verifiers in order. With none, every request is anonymous.
type Authenticator struct {
	verifiers      []Verifier
	allowAnonymous bool
	hasher         Hasher
}

// NewAuthenticator returns an authenticator over verifiers. allowAnonymous says whether a
// request with no credential is an anonymous caller or is refused.
func NewAuthenticator(hasher Hasher, allowAnonymous bool, verifiers ...Verifier) *Authenticator {
	return &Authenticator{verifiers: verifiers, allowAnonymous: allowAnonymous, hasher: hasher}
}

// AnonymousAuthenticator is the default: it accepts every request as the anonymous caller.
func AnonymousAuthenticator() *Authenticator {
	return NewAuthenticator(Hasher{}, true)
}

// Enforcing reports whether the authenticator checks credentials. When it does not, a crew
// behaves as it did before identity existed: nothing is verified, owned or refused.
func (a *Authenticator) Enforcing() bool { return len(a.verifiers) > 0 || !a.allowAnonymous }

// Authenticate returns the caller of r. A credential that is present and not valid is
// ErrInvalid even when anonymous callers are allowed. A verifier that finds the credential
// invalid does not end the chain: a later verifier may know it (a token the API server does
// not know may be a person's token a service accepts), and when none does the answer stays
// ErrInvalid. A check that could not be made ends the chain at once.
func (a *Authenticator) Authenticate(r *http.Request) (Caller, error) {
	invalid := false
	for _, v := range a.verifiers {
		id, err := v.Verify(r)
		switch {
		case err == nil:
			return a.callerOf(id)
		case errors.Is(err, ErrNoCredential):
			continue
		case errors.Is(err, ErrInvalid):
			invalid = true
		default:
			return Caller{}, err
		}
	}
	if invalid {
		return Caller{}, ErrInvalid
	}
	if !a.allowAnonymous {
		return Caller{}, ErrRequired
	}
	return AnonymousCaller(), nil
}

// callerOf builds the caller of a verified identity: the principal carries the hashed
// subject, and a display name only for a workload. A subscriber on a door with no hash key
// is refused, never named with a guessable id.
func (a *Authenticator) callerOf(id *Identity) (Caller, error) {
	if id.Issuer != IssuerKubernetes && !a.hasher.Keyed() {
		return Caller{}, ErrNoHashKey
	}
	return Caller{Principal: PrincipalOf(a.hasher, id), Identity: id}, nil
}

// PrincipalOf is the principal of a verified identity. Every place that compares a caller
// with an owner builds it here, so the id is computed one way. A Kubernetes caller's id
// is always the unkeyed hash, so ids already recorded as owners keep matching; a subscriber's
// id is keyed, and without a key it has no id (SamePrincipal never matches it).
func PrincipalOf(h Hasher, id *Identity) bus.Principal {
	if id.Issuer != IssuerKubernetes {
		p := bus.Principal{Kind: bus.PrincipalKindSubscriber, Issuer: id.Issuer}
		if h.Keyed() {
			p.ID = h.ID(id.Issuer, id.Subject)
		}
		return p
	}
	p := bus.Principal{Kind: bus.PrincipalKindKubernetes, Issuer: id.Issuer, ID: Hasher{}.ID(id.Issuer, id.Subject)}
	if id.IsWorkload() {
		p.DisplayName = id.Subject
	}
	return p
}

// SamePrincipal reports whether two principals are the same caller: the same kind and id.
// Anonymous callers share the one anonymous principal; a principal with no id is no one in
// particular and matches nothing else.
func SamePrincipal(a, b bus.Principal) bool {
	return a.Kind == b.Kind && a.ID == b.ID && (a.ID != "" || a.Kind == bus.PrincipalKindAnonymous)
}

// Status is the HTTP status a door answers for an authentication or authorization error,
// and whether the answer carries a WWW-Authenticate challenge.
func Status(err error) (code int, challenge bool) {
	switch {
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrRequired):
		return http.StatusUnauthorized, true
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden, false
	default:
		return http.StatusServiceUnavailable, false
	}
}

// Hasher turns an issuer and subject into the pseudonymous id the bus carries: an HMAC
// with the install's key, so an id cannot be reversed by hashing guessed subjects, and
// the same on every replica that has the key. With no key the hash is plain SHA-256
// with a fixed label, stable but guessable for a subject that is public, which is all a
// Kubernetes username is; a subscriber's subject needs the key.
type Hasher struct{ Key []byte }

const hashLabel = "kubemoot-principal-v1"

// idLength is the hex digits of an id: 128 bits.
const idLength = 32

// Keyed reports whether the hasher has an install key.
func (h Hasher) Keyed() bool { return len(h.Key) > 0 }

// ID returns the id of subject as issued by issuer.
func (h Hasher) ID(issuer, subject string) string {
	key := h.Key
	if len(key) == 0 {
		key = []byte(hashLabel)
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(issuer))
	m.Write([]byte{0})
	m.Write([]byte(subject))
	return hex.EncodeToString(m.Sum(nil))[:idLength]
}

type callerKey struct{}

// WithCaller returns ctx carrying the caller.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom returns the caller a door put in ctx, or the anonymous caller.
func CallerFrom(ctx context.Context) Caller {
	if c, ok := ctx.Value(callerKey{}).(Caller); ok {
		return c
	}
	return AnonymousCaller()
}

// BearerToken is the token of an "Authorization: Bearer <token>" header, or "".
func BearerToken(r *http.Request) string {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}
