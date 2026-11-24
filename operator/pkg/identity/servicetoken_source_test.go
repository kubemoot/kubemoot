/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

const (
	floodSource = "198.51.100.7:4000"
	quietSource = "203.0.113.9:5000"
)

// bearerFrom is a request carrying token from a peer address.
func bearerFrom(token, remote string) *http.Request {
	r := bearer(token)
	r.RemoteAddr = remote
	return r
}

func TestOneSourceFloodingNewTokensSpendsOnlyItsOwnAllowance(t *testing.T) {
	svc := newService(t, 401)
	v, clock := verifierFor(t, svc)
	var limited int
	for i := 0; i < 50; i++ {
		_, err := v.Verify(bearerFrom(oreillyToken(t, fmt.Sprintf("f-%d", i), clock.now().Add(time.Hour)), floodSource))
		if errors.Is(err, ErrServiceUnavailable) {
			limited++
		}
	}
	if n := svc.calls.Load(); n != int32(SourceVerifyBurst) || limited != 50-SourceVerifyBurst {
		t.Fatalf("calls=%d limited=%d, want %d and %d", n, limited, SourceVerifyBurst, 50-SourceVerifyBurst)
	}
	// Another source is not held back by the flood, and its token is accepted once asked.
	svc.status.Store(200)
	if _, err := v.Verify(bearerFrom(oreillyToken(t, "quiet", clock.now().Add(time.Hour)), quietSource)); err != nil {
		t.Fatalf("a quiet source was refused during another's flood: %v", err)
	}
}

func TestSourceAllowanceRefillsWithTime(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc)
	ask := func(i int) error {
		_, err := v.Verify(bearerFrom(oreillyToken(t, fmt.Sprintf("r-%d", i), clock.now().Add(time.Hour)), floodSource))
		return err
	}
	for i := 0; i < SourceVerifyBurst; i++ {
		if err := ask(i); err != nil {
			t.Fatal(err)
		}
	}
	if err := ask(100); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("over the source limit: %v", err)
	}
	clock.advance(time.Second)
	if err := ask(101); err != nil {
		t.Fatalf("after a second the source may ask again: %v", err)
	}
}

func TestASourceLimitNeverTouchesACachedToken(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc)
	token := oreillyToken(t, "again", clock.now().Add(time.Hour))
	for i := 0; i < 3*SourceVerifyBurst; i++ {
		if _, err := v.Verify(bearerFrom(token, floodSource)); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if svc.calls.Load() != 1 {
		t.Fatalf("%d calls", svc.calls.Load())
	}
}

func TestSourceIsThePeerAddressNeverAForwardedHeader(t *testing.T) {
	svc := newService(t, 401)
	v, clock := verifierFor(t, svc)
	for i := 0; i < 3*SourceVerifyBurst; i++ {
		r := bearerFrom(oreillyToken(t, fmt.Sprintf("x-%d", i), clock.now().Add(time.Hour)), floodSource)
		r.Header.Set("X-Forwarded-For", fmt.Sprintf("10.0.0.%d", i))
		_, _ = v.Verify(r)
	}
	if n := svc.calls.Load(); n != int32(SourceVerifyBurst) {
		t.Fatalf("a spoofed forwarded-for header escaped the source limit: %d calls", n)
	}
}

func TestSourceTableIsBoundedAndDropsIdleSources(t *testing.T) {
	s := newSourceLimits()
	start := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	for i := 0; i < maxSources; i++ {
		s.reserve(fmt.Sprintf("host-%d", i), start)
	}
	// Full of live sources: a new one is untracked and allowed (the shared limit holds it).
	if _, ok := s.reserve("newcomer", start); !ok || len(s.sources) != maxSources {
		t.Fatalf("table %d, the table must stay bounded", len(s.sources))
	}
	// Once the others are idle, a newcomer is tracked and the idle ones go.
	later := start.Add(sourceIdle + time.Second)
	s.reserve("newcomer", later)
	if len(s.sources) != 1 {
		t.Fatalf("idle sources were not dropped: %d", len(s.sources))
	}
}

func TestSourceOfPeerAddress(t *testing.T) {
	for remote, want := range map[string]string{"192.0.2.1:1234": "192.0.2.1", "[2001:db8::1]:80": "2001:db8::1", "bare": "bare"} {
		r := bearer("")
		r.RemoteAddr = remote
		if got := sourceOf(r); got != want {
			t.Errorf("%q: got %q, want %q", remote, got, want)
		}
	}
}

func TestRevokedTokenStaysAcceptedOnlyUntilItsCacheEntryEnds(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc, func(c *ServiceTokenConfig) { c.MaxCacheSeconds = 60 })
	token := oreillyToken(t, "revoked-later", clock.now().Add(time.Hour))
	if _, err := v.Verify(bearer(token)); err != nil {
		t.Fatal(err)
	}
	svc.status.Store(401) // the service revokes the token
	clock.advance(59 * time.Second)
	if _, err := v.Verify(bearer(token)); err != nil || svc.calls.Load() != 1 {
		t.Fatalf("within the bound the cached acceptance stands: %v, %d calls", err, svc.calls.Load())
	}
	clock.advance(2 * time.Second)
	if _, err := v.Verify(bearer(token)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("past the bound the revocation is seen: %v", err)
	}
	// The refusal is remembered for the negative TTL, then asked again.
	svc.status.Store(200)
	if _, err := v.Verify(bearer(token)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a refusal is held for the negative TTL: %v", err)
	}
	clock.advance(NegativeCacheTTL + time.Second)
	if _, err := v.Verify(bearer(token)); err != nil {
		t.Fatalf("after the negative TTL a restored token is accepted: %v", err)
	}
}

func TestACallTheSharedLimitRefusesIsNotChargedToTheSource(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc)
	v.limiter = rate.NewLimiter(1, 1)
	for i := 0; i < 10; i++ {
		_, _ = v.Verify(bearerFrom(oreillyToken(t, fmt.Sprintf("s-%d", i), clock.now().Add(time.Hour)), floodSource))
	}
	// One call was made and nine were refused by the shared limit; the source paid for one.
	clock.advance(time.Second)
	v.limiter = rate.NewLimiter(1000, 1000)
	for i := 0; i < SourceVerifyBurst-1; i++ {
		if _, err := v.Verify(bearerFrom(oreillyToken(t, fmt.Sprintf("t-%d", i), clock.now().Add(time.Hour)), floodSource)); err != nil {
			t.Fatalf("call %d was refused though the source spent only one: %v", i, err)
		}
	}
}
