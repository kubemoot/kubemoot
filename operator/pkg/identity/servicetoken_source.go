/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	// SourceVerifyRate and SourceVerifyBurst limit the calls to the service that one source
	// address causes, so one caller flooding new tokens spends its own allowance and leaves
	// the verifier's shared limit for everyone else.
	SourceVerifyRate rate.Limit = 10
	// SourceVerifyBurst is the burst of that limit.
	SourceVerifyBurst int = 20
	// maxSources bounds the sources tracked; idle ones are dropped first, and a source that
	// cannot be tracked is still held by the shared limit.
	maxSources = 4096
	// sourceIdle is how long a source must be quiet before it is dropped.
	sourceIdle = time.Minute
)

// sourceLimits is one rate limiter per source address.
type sourceLimits struct {
	mu        sync.Mutex
	sources   map[string]*sourceEntry
	lastSweep time.Time
}

type sourceEntry struct {
	limiter *rate.Limiter
	seen    time.Time
}

func newSourceLimits() *sourceLimits { return &sourceLimits{sources: map[string]*sourceEntry{}} }

// reserve spends one call of the source's allowance and returns the reservation, so the
// caller can give it back when another limit refuses the call. ok is false when the source
// is over its allowance. A source that cannot be tracked is allowed with a nil reservation;
// the shared limit still holds it.
func (s *sourceLimits) reserve(source string, now time.Time) (res *rate.Reservation, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, found := s.sources[source]
	if !found {
		if len(s.sources) >= maxSources {
			s.dropIdle(now)
		}
		if len(s.sources) >= maxSources {
			return nil, true
		}
		e = &sourceEntry{limiter: rate.NewLimiter(SourceVerifyRate, SourceVerifyBurst)}
		s.sources[source] = e
	}
	e.seen = now
	res = e.limiter.ReserveN(now, 1)
	if !res.OK() || res.DelayFrom(now) > 0 {
		res.CancelAt(now)
		return nil, false
	}
	return res, true
}

// dropIdle forgets sources quiet for sourceIdle. It sweeps at most once a second, so a flood
// of new sources against a full table does not scan the table on every request.
func (s *sourceLimits) dropIdle(now time.Time) {
	if now.Sub(s.lastSweep) < time.Second {
		return
	}
	s.lastSweep = now
	for k, e := range s.sources {
		if now.Sub(e.seen) >= sourceIdle {
			delete(s.sources, k)
		}
	}
}

// sourceOf is the host part of the request's peer address.
func sourceOf(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
