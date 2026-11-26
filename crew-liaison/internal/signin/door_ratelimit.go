package signin

import (
	"container/list"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
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
	limiterMaxKey = 10000

	headerForwardedFor = "X-Forwarded-For"
)

// The environment of the limits and of the proxies trusted to name the client's address.
const (
	EnvRatePersonPerMinute  = "LIAISON_RATE_PERSON_PER_MINUTE"
	EnvRatePersonBurst      = "LIAISON_RATE_PERSON_BURST"
	EnvRateAddressPerMinute = "LIAISON_RATE_ADDRESS_PER_MINUTE"
	EnvRateAddressBurst     = "LIAISON_RATE_ADDRESS_BURST"
	EnvTrustedProxyCIDRs    = "LIAISON_TRUSTED_PROXY_CIDRS"
	EnvTrustedProxyHeader   = "LIAISON_TRUSTED_PROXY_HEADER"
)

// RateLimits are the per-person and per-source-address limits of the connect pages and the
// account endpoints. Zero values take the defaults.
//
// TrustedProxies are the networks of the proxies, tunnels and gateways in front of the liaison.
// Only a connection from one of them may say, in ProxyHeader, which address the client has; any
// other connection is keyed on its own peer address and its header is ignored. With none
// listed, every connection is keyed on its peer address, so behind a proxy all callers share one
// allowance.
type RateLimits struct {
	PersonRate, AddressRate   rate.Limit
	PersonBurst, AddressBurst int
	TrustedProxies            []netip.Prefix
	// ProxyHeader is "X-Forwarded-For" (the rightmost address not in TrustedProxies is the
	// client) or the name of a header that holds the client's address alone, such as
	// "CF-Connecting-IP". Empty with TrustedProxies set means "X-Forwarded-For".
	ProxyHeader string
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
	if l.ProxyHeader == "" {
		l.ProxyHeader = headerForwardedFor
	}
	return l
}

// RateLimitsFromEnv reads the limits and the trusted proxies. Unset values keep the defaults
// and trust no proxy; a value that cannot be read is an error, so a typo never silently turns a
// limit or the trust off.
func RateLimitsFromEnv(getenv func(string) string) (RateLimits, error) {
	var l RateLimits
	for _, f := range []struct {
		env string
		set func(int)
	}{
		{EnvRatePersonPerMinute, func(n int) { l.PersonRate = rate.Limit(float64(n) / 60) }},
		{EnvRatePersonBurst, func(n int) { l.PersonBurst = n }},
		{EnvRateAddressPerMinute, func(n int) { l.AddressRate = rate.Limit(float64(n) / 60) }},
		{EnvRateAddressBurst, func(n int) { l.AddressBurst = n }},
	} {
		raw := getenv(f.env)
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return RateLimits{}, fmt.Errorf("%s must be a whole number above zero", f.env)
		}
		f.set(n)
	}
	for _, cidr := range splitList(getenv(EnvTrustedProxyCIDRs)) {
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			return RateLimits{}, fmt.Errorf("%s: %q is not a CIDR", EnvTrustedProxyCIDRs, cidr)
		}
		l.TrustedProxies = append(l.TrustedProxies, p.Masked())
	}
	l.ProxyHeader = http.CanonicalHeaderKey(strings.TrimSpace(getenv(EnvTrustedProxyHeader)))
	if l.ProxyHeader != "" && len(l.TrustedProxies) == 0 {
		return RateLimits{}, fmt.Errorf("%s needs %s: a header is read only from a trusted proxy",
			EnvTrustedProxyHeader, EnvTrustedProxyCIDRs)
	}
	return l, nil
}

// keyedLimiter is one token bucket per key. When it holds too many keys it forgets the key
// seen least recently.
type keyedLimiter struct {
	mu      sync.Mutex
	rate    rate.Limit
	burst   int
	now     func() time.Time
	entries map[string]*list.Element // value: *limiterEntry; the front is the most recently seen
	order   *list.List
}

type limiterEntry struct {
	key string
	lim *rate.Limiter
}

func newKeyedLimiter(r rate.Limit, burst int, now func() time.Time) *keyedLimiter {
	return &keyedLimiter{rate: r, burst: burst, now: now, entries: map[string]*list.Element{}, order: list.New()}
}

// allow spends one token for key. When none is left it returns false and how long until one is.
func (k *keyedLimiter) allow(key string) (bool, time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	el, ok := k.entries[key]
	if ok {
		k.order.MoveToFront(el)
	} else {
		if len(k.entries) >= limiterMaxKey {
			oldest := k.order.Back()
			delete(k.entries, oldest.Value.(*limiterEntry).key)
			k.order.Remove(oldest)
		}
		el = k.order.PushFront(&limiterEntry{key: key, lim: rate.NewLimiter(k.rate, k.burst)})
		k.entries[key] = el
	}
	res := el.Value.(*limiterEntry).lim.ReserveN(now, 1)
	if delay := res.DelayFrom(now); delay > 0 {
		res.CancelAt(now)
		return false, delay
	}
	return true, 0
}

// accountLimits holds the two limiters and the proxy trust.
type accountLimits struct {
	person, address *keyedLimiter
	proxies         []netip.Prefix
	header          string
}

func newAccountLimits(l RateLimits, now func() time.Time) *accountLimits {
	l = l.withDefaults()
	return &accountLimits{
		person:  newKeyedLimiter(l.PersonRate, l.PersonBurst, now),
		address: newKeyedLimiter(l.AddressRate, l.AddressBurst, now),
		proxies: l.TrustedProxies, header: l.ProxyHeader,
	}
}

// peerAddress is the address the connection came from.
func peerAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *accountLimits) trusted(addr netip.Addr) bool {
	for _, p := range a.proxies {
		if p.Contains(addr.Unmap()) {
			return true
		}
	}
	return false
}

// clientAddress is the key of the address limit: the connection's peer address, or, when that
// peer is a trusted proxy and its header names a valid client address, that address. A header
// from any other peer is ignored, and a header that cannot be read falls back to the peer.
func (a *accountLimits) clientAddress(r *http.Request) string {
	peer := peerAddress(r)
	addr, err := netip.ParseAddr(peer)
	if err != nil || len(a.proxies) == 0 || !a.trusted(addr) {
		return peer
	}
	values := r.Header.Values(a.header)
	if len(values) == 0 {
		return peer
	}
	if a.header == headerForwardedFor {
		if client, ok := a.rightmostUntrusted(strings.Join(values, ",")); ok {
			return client
		}
		return peer
	}
	if len(values) == 1 {
		if client, err := netip.ParseAddr(strings.TrimSpace(values[0])); err == nil {
			return client.Unmap().String()
		}
	}
	return peer
}

// rightmostUntrusted walks the list from the right, past the proxies this install trusts, and
// returns the first address that is not one of them: the nearest hop an attacker cannot have
// written. An entry that is not an address ends the walk with no answer.
func (a *accountLimits) rightmostUntrusted(chain string) (string, bool) {
	parts := strings.Split(chain, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			return "", false
		}
		if !a.trusted(addr) {
			return addr.Unmap().String(), true
		}
	}
	return "", false
}

// limited wraps a connect or account handler. It limits by source address first, then by the
// person the door session names when there is one, and answers 429 with Retry-After. Nothing
// about the person is logged.
func (d *door) limited(next http.HandlerFunc, perPerson bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok, wait := d.limits.address.allow(d.limits.clientAddress(r))
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
