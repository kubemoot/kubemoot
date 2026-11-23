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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"
)

const (
	// NegativeCacheTTL is how long a refused token is remembered, so garbage tokens are not
	// a way to make the door call the service over and over.
	NegativeCacheTTL = 30 * time.Second
	// verifyTimeout bounds the one call to the service.
	verifyTimeout = 3 * time.Second
	// maxCacheEntries bounds the cache; beyond it expired entries go, then the call is
	// simply not remembered.
	maxCacheEntries = 10000
	// VerifyRate and VerifyBurst limit the calls one verifier makes to its service, per second
	// and at once. A flood of new tokens past it is answered ErrServiceUnavailable without a
	// call, so the door cannot be used to hammer the service.
	VerifyRate rate.Limit = 20
	// VerifyBurst is the burst of that limit.
	VerifyBurst int = 40
	// mcpInitializeBody is the request that asks an MCP server to start a session; a
	// server answers 200 only to a caller it accepts.
	mcpInitializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
		`"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"kubemoot-identity","version":"0"}}}`
)

// ErrServiceUnavailable means the service could not be asked or gave no clear answer. The
// door answers 503 and never allows.
var ErrServiceUnavailable = errors.New("the service that verifies the token could not be reached")

// ServiceTokenVerifier accepts a bearer token when a named service accepts it. The person's
// own token is the proof: a modified payload fails at the service, so the subject read from
// the payload is trusted only after the service said yes to that exact token. It makes one
// call per token at a time (concurrent requests with one token share it), at a bounded rate,
// remembers the answer, and never logs, traces or returns the token in an
// error. It holds no state beyond the cache and keeps nothing at rest.
type ServiceTokenVerifier struct {
	cfg    ServiceTokenConfig
	key    []byte
	client *http.Client
	now    func() time.Time
	mu     sync.Mutex
	cache  map[string]cacheEntry
	// flights makes concurrent requests with one new token share one call to the service.
	flights singleflight.Group
	// limiter bounds the calls to the service; a call it refuses is not made.
	limiter *rate.Limiter
}

type cacheEntry struct {
	id      *Identity
	until   time.Time
	invalid bool
}

// NewServiceTokenVerifier returns the verifier of one service. key is the install's hash
// key: it keys the cache, so the cache holds no token (not as a key, not in an entry), and a door that cannot name a
// subscriber without it must not start without it. client may be nil.
func NewServiceTokenVerifier(cfg ServiceTokenConfig, key []byte, client *http.Client) (*ServiceTokenVerifier, error) {
	if len(key) == 0 {
		return nil, ErrNoHashKey
	}
	if err := cfg.normalize(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServiceTokenConfig, err)
	}
	if client == nil {
		// No proxy: the bearer token goes straight to the configured URL.
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		client = &http.Client{Transport: transport}
	}
	// Never follow a redirect: the bearer token goes only to the configured URL.
	c := *client
	c.Timeout = verifyTimeout
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &ServiceTokenVerifier{cfg: cfg, key: key, client: &c, now: time.Now, cache: map[string]cacheEntry{},
		limiter: rate.NewLimiter(VerifyRate, VerifyBurst)}, nil
}

// Verify checks the request's bearer token. No bearer is ErrNoCredential. A bearer that is
// not a JWT naming a person is ErrInvalid before any call goes out, so a Kubernetes token
// or garbage is never sent to the service. A service that cannot be reached or answers
// something other than yes or no is ErrServiceUnavailable, never an acceptance.
func (v *ServiceTokenVerifier) Verify(r *http.Request) (*Identity, error) {
	token := BearerToken(r)
	if token == "" {
		return nil, ErrNoCredential
	}
	claims, ok := parseTokenClaims(token)
	if !ok {
		return nil, ErrInvalid
	}
	subject, exp, ok := v.checkShape(claims)
	if !ok {
		return nil, ErrInvalid
	}
	key := v.cacheKey(token)
	id, err := v.cached(key)
	if err == nil && id == nil {
		id, err = v.verifyOnce(r.Context(), key, token, subject, exp)
	}
	if err != nil {
		return nil, err
	}
	// The cache holds the identity without the token; every caller gets its own token back.
	return withCredential(id, token), nil
}

// verifyOnce asks the service about a token that is not cached, once for all concurrent
// requests with that token. The answer is cached inside the flight, so a request that
// arrives after it finishes hits the cache. Only a yes or a no is cached: a refusal for the
// negative TTL, an acceptance until the token's own expiry or the bound. A call that was
// rate limited or failed is never cached, so the next request asks again.
func (v *ServiceTokenVerifier) verifyOnce(
	ctx context.Context, key, token, subject string, exp time.Time,
) (*Identity, error) {
	res, err, _ := v.flights.Do(key, func() (any, error) {
		if id, err := v.cached(key); id != nil || err != nil {
			return id, err
		}
		if !v.limiter.AllowN(v.now(), 1) {
			return nil, ErrServiceUnavailable
		}
		// The call is shared, so one request leaving must not cancel it for the others.
		accepted, err := v.ask(context.WithoutCancel(ctx), token)
		if err != nil {
			return nil, err
		}
		if !accepted {
			v.store(key, cacheEntry{invalid: true, until: v.now().Add(NegativeCacheTTL)})
			return nil, ErrInvalid
		}
		id := &Identity{Issuer: v.cfg.Name, Subject: subject}
		v.store(key, cacheEntry{id: id, until: v.positiveUntil(exp)})
		return id, nil
	})
	if err != nil {
		return nil, err
	}
	return res.(*Identity), nil
}

// cached is the cached answer for key: the identity, ErrInvalid for a remembered refusal,
// or nil and nil when nothing live is cached.
func (v *ServiceTokenVerifier) cached(key string) (*Identity, error) {
	e, hit := v.lookup(key)
	switch {
	case !hit:
		return nil, nil
	case e.invalid:
		return nil, ErrInvalid
	}
	return e.id, nil
}

// checkShape is the cheap check before any call: the token has a subject at the claim path,
// has not expired, and has a wanted audience when audiences are configured.
func (v *ServiceTokenVerifier) checkShape(c tokenClaims) (subject string, exp time.Time, ok bool) {
	subject, ok = c.String(v.cfg.SubjectClaim)
	if !ok || c.isKubernetes() {
		return "", time.Time{}, false
	}
	exp, ok = c.Expiry()
	if !ok || !exp.After(v.now()) {
		return "", time.Time{}, false
	}
	if len(v.cfg.Audiences) > 0 && !anyIn(v.cfg.Audiences, c.Audiences()) {
		return "", time.Time{}, false
	}
	return subject, exp, true
}

func anyIn(wanted, have []string) bool {
	for _, w := range wanted {
		if slices.Contains(have, w) {
			return true
		}
	}
	return false
}

// ask makes the one call. 200 accepts; 401 and 403 refuse; anything else is not an answer.
func (v *ServiceTokenVerifier) ask(ctx context.Context, token string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.cfg.VerifyURL, strings.NewReader(mcpInitializeBody))
	if err != nil {
		return false, ErrServiceUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := v.client.Do(req)
	if err != nil {
		// The transport error names the URL and the cause, never a header; it is not
		// passed on, so nothing a log line could carry leaves this function.
		return false, ErrServiceUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return false, nil
	}
	return false, ErrServiceUnavailable
}

// positiveUntil is how long an accepted token is remembered: its own life, at most the bound.
func (v *ServiceTokenVerifier) positiveUntil(exp time.Time) time.Time {
	until := v.now().Add(v.cfg.maxCache())
	if exp.Before(until) {
		return exp
	}
	return until
}

// cacheKey is a keyed hash of the token, so the cache holds no token and a memory read of
// the map yields nothing a caller could replay.
func (v *ServiceTokenVerifier) cacheKey(token string) string {
	m := hmac.New(sha256.New, v.key)
	m.Write([]byte("service-token-cache\x00" + v.cfg.Name + "\x00" + token))
	return hex.EncodeToString(m.Sum(nil))
}

// lookup returns a live cache entry.
func (v *ServiceTokenVerifier) lookup(key string) (cacheEntry, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	e, ok := v.cache[key]
	if ok && !e.until.After(v.now()) {
		delete(v.cache, key)
		return cacheEntry{}, false
	}
	return e, ok
}

// store remembers an answer, dropping expired entries first when the cache is full.
func (v *ServiceTokenVerifier) store(key string, e cacheEntry) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.cache) >= maxCacheEntries {
		now := v.now()
		for k, old := range v.cache {
			if !old.until.After(now) {
				delete(v.cache, k)
			}
		}
		if len(v.cache) >= maxCacheEntries {
			return
		}
	}
	v.cache[key] = e
}

// withCredential returns a copy of id carrying token, so a caller cannot change what the
// cache holds and the cache never holds a token.
func withCredential(id *Identity, token string) *Identity {
	c := *id
	c.Credential = NewCredential(token)
	return &c
}
