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
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

const concurrent = 50

// verifyConcurrently runs n requests at once, one per token, and returns each outcome. The
// service call is held until every request has started, so they overlap.
func verifyConcurrently(t *testing.T, v *ServiceTokenVerifier, svc *service, tokens []string) []error {
	t.Helper()
	errs := make([]error, len(tokens))
	var started, done sync.WaitGroup
	started.Add(len(tokens))
	done.Add(len(tokens))
	for i, token := range tokens {
		go func() {
			defer done.Done()
			started.Done()
			_, errs[i] = v.Verify(bearer(token))
		}()
	}
	started.Wait()
	if svc.gate != nil {
		time.Sleep(50 * time.Millisecond) // let the requests reach the verifier before the answer
		close(svc.gate)
	}
	done.Wait()
	return errs
}

func repeat(token string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = token
	}
	return out
}

func TestConcurrentRequestsWithOneNewTokenMakeOneCall(t *testing.T) {
	svc := newService(t, 200)
	svc.gate = make(chan struct{})
	v, clock := verifierFor(t, svc)
	token := oreillyToken(t, "acct-1", clock.now().Add(time.Hour))
	for i, err := range verifyConcurrently(t, v, svc, repeat(token, concurrent)) {
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if n := svc.calls.Load(); n != 1 {
		t.Fatalf("%d concurrent requests with one token made %d calls", concurrent, n)
	}
}

func TestEachConcurrentRequestGetsItsOwnTokenBack(t *testing.T) {
	svc := newService(t, 200)
	svc.gate = make(chan struct{})
	v, clock := verifierFor(t, svc)
	token := oreillyToken(t, "acct-1", clock.now().Add(time.Hour))
	ids := make([]*Identity, 8)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids[i], _ = v.Verify(bearer(token))
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(svc.gate)
	wg.Wait()
	for i, id := range ids {
		if id == nil || id.Credential.Token() != token || id.Subject != "acct-1" {
			t.Fatalf("request %d got %#v", i, id)
		}
		if i > 0 && id == ids[0] {
			t.Fatal("two requests share one Identity value")
		}
	}
}

func TestConcurrentRefusedTokenMakesOneCallAndIsRefusedForAll(t *testing.T) {
	svc := newService(t, 401)
	svc.gate = make(chan struct{})
	v, clock := verifierFor(t, svc)
	token := oreillyToken(t, "bad", clock.now().Add(time.Hour))
	for i, err := range verifyConcurrently(t, v, svc, repeat(token, concurrent)) {
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if n := svc.calls.Load(); n != 1 {
		t.Fatalf("%d calls", n)
	}
}

func TestAFailedCallIsRefusedForAllAndNotCached(t *testing.T) {
	svc := newService(t, 500)
	svc.gate = make(chan struct{})
	v, clock := verifierFor(t, svc)
	token := oreillyToken(t, "a", clock.now().Add(time.Hour))
	for i, err := range verifyConcurrently(t, v, svc, repeat(token, concurrent)) {
		if !errors.Is(err, ErrServiceUnavailable) {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	// Neither a success nor a refusal was remembered: the service is asked again.
	before := svc.calls.Load()
	svc.status.Store(200)
	if _, err := v.Verify(bearer(token)); err != nil || svc.calls.Load() != before+1 {
		t.Fatalf("after recovery: %v, %d calls", err, svc.calls.Load()-before)
	}
}

// countOutcomes counts the requests the service refused and those the rate limit stopped.
func countOutcomes(t *testing.T, errs []error) (refused, limited int) {
	t.Helper()
	for _, err := range errs {
		switch {
		case errors.Is(err, ErrInvalid):
			refused++
		case errors.Is(err, ErrServiceUnavailable):
			limited++
		default:
			t.Fatalf("unexpected %v", err)
		}
	}
	return refused, limited
}

func TestAFloodOfNewTokensIsRateLimitedAndNotCached(t *testing.T) {
	svc := newService(t, 401)
	v, clock := verifierFor(t, svc)
	v.limiter = rate.NewLimiter(1, 5)
	tokens := make([]string, 200)
	for i := range tokens {
		tokens[i] = oreillyToken(t, fmt.Sprintf("flood-%d", i), clock.now().Add(time.Hour))
	}
	refused, limited := countOutcomes(t, verifyConcurrently(t, v, svc, tokens))
	if n := svc.calls.Load(); n != 5 || refused != 5 || limited != 195 {
		t.Fatalf("calls=%d refused=%d limited=%d, want 5, 5, 195", n, refused, limited)
	}
	// A limited token is not remembered as refused: once the limiter allows, the service answers.
	svc.status.Store(200)
	clock.advance(2 * time.Second)
	var accepted int
	for _, token := range tokens {
		if _, err := v.Verify(bearer(token)); err == nil {
			accepted++
		}
	}
	if accepted != 2 {
		t.Fatalf("after 2 seconds the limiter allows 2 more calls, %d tokens were accepted", accepted)
	}
}

func TestRateLimitNeverTurnsIntoAnAcceptance(t *testing.T) {
	svc := newService(t, 200)
	v, clock := verifierFor(t, svc)
	v.limiter = rate.NewLimiter(1, 1)
	first := oreillyToken(t, "a", clock.now().Add(time.Hour))
	second := oreillyToken(t, "b", clock.now().Add(time.Hour))
	if _, err := v.Verify(bearer(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(bearer(second)); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("a call over the limit must not be allowed, got %v", err)
	}
	// A cached token needs no call, so the limit does not touch it.
	if _, err := v.Verify(bearer(first)); err != nil {
		t.Fatalf("a cached token is not limited: %v", err)
	}
	if n := svc.calls.Load(); n != 1 {
		t.Fatalf("%d calls", n)
	}
}

func TestADefaultVerifierIsRateLimited(t *testing.T) {
	v, _ := verifierFor(t, newService(t, 200))
	if v.limiter.Limit() != VerifyRate || v.limiter.Burst() != VerifyBurst {
		t.Fatalf("limiter %v/%d", v.limiter.Limit(), v.limiter.Burst())
	}
}
