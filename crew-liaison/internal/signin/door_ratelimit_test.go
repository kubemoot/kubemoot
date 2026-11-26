package signin

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const forwardedFor = "X-Forwarded-For"

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
