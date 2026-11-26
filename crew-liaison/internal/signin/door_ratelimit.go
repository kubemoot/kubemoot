package signin

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limits on the connect pages and the account endpoints. A person opens these a few times, so
// the limits stop a script from using the door to hammer an outside service's authorization
// endpoint or from guessing, not a person from using the pages.
const (
	personRate    = rate.Limit(1.0 / 3) // one request every three seconds once the burst is spent
	personBurst   = 20
	addressRate   = rate.Limit(2)
	addressBurst  = 60
	limiterIdle   = 10 * time.Minute
	limiterMaxKey = 10000
)

// RateLimits are the per-person and per-source-address limits of the connect pages and the
// account endpoints. Zero values take the defaults.
type RateLimits struct {
	PersonRate, AddressRate   rate.Limit
	PersonBurst, AddressBurst int
}

func (l RateLimits) withDefaults() RateLimits {
	if l.PersonRate == 0 {
		l.PersonRate = personRate
	}
	if l.AddressRate == 0 {
		l.AddressRate = addressRate
	}
	if l.PersonBurst == 0 {
		l.PersonBurst = personBurst
	}
	if l.AddressBurst == 0 {
		l.AddressBurst = addressBurst
	}
	return l
}

// keyedLimiter is one token bucket per key, forgotten after it has been idle. When it holds
// too many keys it drops an arbitrary one, so memory stays bounded.
type keyedLimiter struct {
	mu      sync.Mutex
	rate    rate.Limit
	burst   int
	now     func() time.Time
	entries map[string]*limiterEntry
}

type limiterEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

func newKeyedLimiter(r rate.Limit, burst int, now func() time.Time) *keyedLimiter {
	return &keyedLimiter{rate: r, burst: burst, now: now, entries: map[string]*limiterEntry{}}
}

// allow spends one token for key. When none is left it returns false and how long until one is.
func (k *keyedLimiter) allow(key string) (bool, time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	e, ok := k.entries[key]
	if !ok {
		k.makeRoom(now)
		e = &limiterEntry{lim: rate.NewLimiter(k.rate, k.burst)}
		k.entries[key] = e
	}
	e.seen = now
	res := e.lim.ReserveN(now, 1)
	if delay := res.DelayFrom(now); delay > 0 {
		res.CancelAt(now)
		return false, delay
	}
	return true, 0
}

// makeRoom drops idle keys, and an arbitrary one when none is idle, once the table is full.
func (k *keyedLimiter) makeRoom(now time.Time) {
	if len(k.entries) < limiterMaxKey {
		return
	}
	for key, e := range k.entries {
		if now.Sub(e.seen) > limiterIdle {
			delete(k.entries, key)
		}
	}
	for key := range k.entries {
		if len(k.entries) < limiterMaxKey {
			return
		}
		delete(k.entries, key)
	}
}

// accountLimits holds the two limiters.
type accountLimits struct {
	person, address *keyedLimiter
}

func newAccountLimits(l RateLimits, now func() time.Time) *accountLimits {
	l = l.withDefaults()
	return &accountLimits{
		person:  newKeyedLimiter(l.PersonRate, l.PersonBurst, now),
		address: newKeyedLimiter(l.AddressRate, l.AddressBurst, now),
	}
}

// peerAddress is the address the connection came from. A forwarded header is never read: any
// caller can write one, so keying on it would let a script choose its own limit.
func peerAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limited wraps a connect or account handler. It limits by source address first, then by the
// person the door session names when there is one, and answers 429 with Retry-After. Nothing
// about the person is logged.
func (d *door) limited(next http.HandlerFunc, perPerson bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok, wait := d.limits.address.allow(peerAddress(r))
		if ok && perPerson {
			if s, has := d.session(r); has {
				ok, wait = d.limits.person.allow(d.principalOf(s))
			}
		}
		if ok {
			next(w, r)
			return
		}
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
		d.event("an account request was limited", "path", r.URL.Path)
		if r.URL.Path == pathRevoke {
			writeOAuthError(w, http.StatusTooManyRequests, "temporarily_unavailable", "too many requests, try again later")
			return
		}
		d.notice(w, http.StatusTooManyRequests, "Too many requests", "Wait a moment, then try again.")
	}
}
