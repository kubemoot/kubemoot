package signin

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	forwardedFor = "X-Forwarded-For"
	clientNine   = "203.0.113.9"
	headerCF     = "CF-Connecting-IP"
	proxyHost    = "10.0.0.5"
)

func TestAccountPagesLimitEachPersonAndOthersAreUnaffected(t *testing.T) {
	r := newConnectRig(t, func(c *DoorConfig) {
		c.RateLimits = RateLimits{PersonBurst: 3, PersonRate: 0.1, AddressBurst: 1000, AddressRate: 1000}
	})
	jane := newBrowser(t, r.srv.URL)
	if page := r.openLink(jane, r.linkPath(userJane, acctLibrary), janeClaims); page.StatusCode != http.StatusOK {
		t.Fatalf("the first page: %d", page.StatusCode)
	}
	limited := askUntilLimited(t, jane)
	wait, err := strconv.Atoi(limited.Header.Get("Retry-After"))
	if err != nil || wait < 1 {
		t.Fatalf("Retry-After: %q", limited.Header.Get("Retry-After"))
	}
	if text := body(t, limited); !strings.Contains(text, "Too many requests") {
		t.Fatalf("the page: %s", text)
	}
	bob := newBrowser(t, r.srv.URL)
	if page := r.openLink(bob, r.linkPath(otherUser, acctLibrary), bobClaims); page.StatusCode != http.StatusOK {
		t.Fatalf("another person must not be limited by jane's use: %d", page.StatusCode)
	}
	r.advance(time.Duration(wait+1) * time.Second)
	if resp := jane.get(pathAccounts); resp.StatusCode != http.StatusOK {
		t.Fatalf("after the wait: %d", resp.StatusCode)
	}
	if strings.Contains(r.logText(), userJane) {
		t.Error("the limit event must not name the person")
	}
}

func askUntilLimited(t *testing.T, b *browser) *http.Response {
	t.Helper()
	for i := 0; i < 6; i++ {
		if resp := b.get(pathAccounts); resp.StatusCode == http.StatusTooManyRequests {
			return resp
		}
	}
	t.Fatal("a person who keeps asking must be limited")
	return nil
}

func TestAccountEndpointsLimitEachSourceAddressByItsPeerNotAForwardedHeader(t *testing.T) {
	r := newConnectRig(t, func(c *DoorConfig) {
		c.RateLimits = RateLimits{AddressBurst: 2, AddressRate: 0.001, PersonBurst: 100, PersonRate: 100}
	})
	h := r.door.Handler()
	call := func(peer, forwarded, method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = peer
		if forwarded != "" {
			req.Header.Set(forwardedFor, forwarded)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for i, forwarded := range []string{"", "203.0.113.1", "203.0.113.2"} {
		got := call("198.51.100.7:4000", forwarded, http.MethodGet, pathAccounts)
		if want := i >= 2; (got.Code == http.StatusTooManyRequests) != want {
			t.Fatalf("request %d from one peer with forwarded %q: %d", i, forwarded, got.Code)
		}
	}
	if got := call("198.51.100.8:4000", "198.51.100.7", http.MethodGet, pathAccounts); got.Code == http.StatusTooManyRequests {
		t.Fatalf("another peer must have its own allowance: %d", got.Code)
	}
	revoke := call("198.51.100.7:4001", "", http.MethodPost, pathRevoke)
	if revoke.Code != http.StatusTooManyRequests || revoke.Header().Get("Retry-After") == "" ||
		!strings.Contains(revoke.Body.String(), "temporarily_unavailable") {
		t.Fatalf("the revoke endpoint answers in OAuth form: %d %s", revoke.Code, revoke.Body.String())
	}
}

func TestKeyedLimiterStaysBounded(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	k := newKeyedLimiter(1, 1, func() time.Time { return now })
	for i := 0; i < limiterMaxKey+500; i++ {
		k.allow("key-" + strconv.Itoa(i))
	}
	if len(k.entries) > limiterMaxKey {
		t.Fatalf("the table grew to %d", len(k.entries))
	}
	if ok, _ := k.allow("fresh"); !ok {
		t.Fatal("a new key must get its burst even when the table is full")
	}
	if ok, wait := k.allow("fresh"); ok || wait <= 0 {
		t.Fatalf("a spent key must wait: %v %v", ok, wait)
	}
}

func TestPeerAddressWithoutAPort(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "unix-socket"
	if got := peerAddress(req); got != "unix-socket" {
		t.Fatalf("peer: %q", got)
	}
}

func TestTheDoorLendsItsSealerForTheStateOfAnElicitation(t *testing.T) {
	r := newConnectRig(t)
	sealer := r.door.Sealer()
	blob, err := sealer.Seal("elicit", map[string]string{"u": "jane"})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := sealer.Open("elicit", blob, &got); err != nil || got["u"] != "jane" {
		t.Fatalf("round trip: %v %v", got, err)
	}
	if sealer.Open("another-purpose", blob, &got) == nil {
		t.Fatal("a value sealed for one purpose must not open for another")
	}
}

func TestFullTableForgetsTheLeastRecentlySeenKey(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	k := newKeyedLimiter(0.0001, 1, func() time.Time { return now })
	for i := 0; i < limiterMaxKey; i++ {
		k.allow("key-" + strconv.Itoa(i))
	}
	k.allow("key-0") // the oldest key is used again, so key-1 is now the least recent
	k.allow("newcomer")
	if _, kept := k.entries["key-0"]; !kept {
		t.Error("a key in use must stay")
	}
	if _, kept := k.entries["key-1"]; kept {
		t.Error("the least recently seen key must go")
	}
	if ok, _ := k.allow("key-0"); ok {
		t.Error("a key that stayed keeps its spent bucket")
	}
}

func limitsOf(t *testing.T, cidrs, header string) *accountLimits {
	t.Helper()
	env := map[string]string{EnvTrustedProxyCIDRs: cidrs, EnvTrustedProxyHeader: header}
	l, err := RateLimitsFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	return newAccountLimits(l, time.Now)
}

func addressFor(a *accountLimits, peer string, headers map[string][]string) string {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = peer
	for k, v := range headers {
		req.Header[http.CanonicalHeaderKey(k)] = v
	}
	return a.clientAddress(req)
}

func TestATrustedProxyNamesTheClientAddress(t *testing.T) {
	const proxy = proxyHost + ":443"
	xff := limitsOf(t, "10.0.0.0/8, fd00::/8", "")
	cf := limitsOf(t, "10.0.0.0/8", "cf-connecting-ip")
	cases := []struct {
		name    string
		limits  *accountLimits
		peer    string
		headers map[string][]string
		want    string
	}{
		{"forwarded for from a trusted peer", xff, proxy, map[string][]string{forwardedFor: {clientNine}}, clientNine},
		{"rightmost untrusted of a chain", xff, proxy, map[string][]string{forwardedFor: {"198.51.100.1, 203.0.113.9, 10.1.1.1"}}, clientNine},
		{"a spoofed leftmost entry is not used", xff, proxy, map[string][]string{forwardedFor: {"1.2.3.4, 203.0.113.9"}}, clientNine},
		{"several header lines are one list", xff, proxy, map[string][]string{forwardedFor: {"1.2.3.4", clientNine}}, clientNine},
		{"an IPv6 client", xff, proxy, map[string][]string{forwardedFor: {"2001:db8::7"}}, "2001:db8::7"},
		{"single header", cf, proxy, map[string][]string{headerCF: {clientNine}}, clientNine},
		{"untrusted peer, header ignored", xff, "198.51.100.77:5000", map[string][]string{forwardedFor: {clientNine}}, "198.51.100.77"},
		{"untrusted peer, single header ignored", cf, "198.51.100.77:5000", map[string][]string{headerCF: {clientNine}}, "198.51.100.77"},
		{"malformed forwarded for", xff, proxy, map[string][]string{forwardedFor: {"not-an-address"}}, proxyHost},
		{"malformed entry in a chain", xff, proxy, map[string][]string{forwardedFor: {"203.0.113.9, junk"}}, proxyHost},
		{"every entry trusted", xff, proxy, map[string][]string{forwardedFor: {"10.2.2.2, 10.3.3.3"}}, proxyHost},
		{"malformed single header", cf, proxy, map[string][]string{headerCF: {"x"}}, proxyHost},
		{"two single headers are ambiguous", cf, proxy, map[string][]string{headerCF: {clientNine, "203.0.113.10"}}, proxyHost},
		{"no header", xff, proxy, nil, proxyHost},
		{"no trusted proxy at all", limitsOf(t, "", ""), proxy, map[string][]string{forwardedFor: {clientNine}}, proxyHost},
	}
	for _, c := range cases {
		if got := addressFor(c.limits, c.peer, c.headers); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestOneClientBehindAProxyDoesNotLockOutAnother(t *testing.T) {
	r := newConnectRig(t, func(c *DoorConfig) {
		c.RateLimits = RateLimits{
			AddressBurst: 2, AddressRate: 0.001, PersonBurst: 100, PersonRate: 100,
			TrustedProxies: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
		}
	})
	h := r.door.Handler()
	call := func(client string) int {
		req := httptest.NewRequest(http.MethodGet, pathAccounts, nil)
		req.RemoteAddr = "192.0.2.1:4000" // the proxy
		req.Header.Set(forwardedFor, client)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	call("203.0.113.1")
	call("203.0.113.1")
	if call("203.0.113.1") != http.StatusTooManyRequests {
		t.Fatal("the noisy client must be limited")
	}
	if call("203.0.113.2") == http.StatusTooManyRequests {
		t.Fatal("another client behind the same proxy must not be")
	}
}

func readLimits(env map[string]string) (RateLimits, error) {
	return RateLimitsFromEnv(func(k string) string { return env[k] })
}

func TestRateLimitsFromEnv(t *testing.T) {
	l, err := readLimits(map[string]string{EnvRatePersonPerMinute: "30", EnvRatePersonBurst: "5", EnvRateAddressPerMinute: "600", EnvRateAddressBurst: "9"})
	if err != nil || l.PersonRate != 0.5 || l.PersonBurst != 5 || l.AddressRate != 10 || l.AddressBurst != 9 || len(l.TrustedProxies) != 0 {
		t.Fatalf("limits: %+v %v", l, err)
	}
	if l, err := readLimits(nil); err != nil || l.PersonBurst != 0 || l.ProxyHeader != "" {
		t.Fatalf("defaults: %+v %v", l, err)
	}
}

func TestRateLimitsFromEnvRefusesWhatItCannotRead(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"not a number":        {EnvRatePersonBurst: "many"},
		"zero":                {EnvRateAddressPerMinute: "0"},
		"not a CIDR":          {EnvTrustedProxyCIDRs: "10.0.0.1/99"},
		"header with no CIDR": {EnvTrustedProxyHeader: headerCF},
	} {
		if _, err := readLimits(env); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
