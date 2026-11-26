package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicAddressRule(t *testing.T) {
	for addr, want := range map[string]bool{
		"93.184.216.34": true, "2606:2800:220:1::1": true, "8.8.8.8": true,
		"10.1.2.3": false, "172.16.0.1": false, "192.168.0.1": false, "127.0.0.1": false, "0.0.0.0": false, "169.254.1.1": false,
		"100.100.100.100": false, "224.0.0.1": false, "255.255.255.255": false, "::1": false, "::": false, "fe80::1": false,
		"fc00::1": false, "::ffff:127.0.0.1": false, "64:ff9b::a00:1": false, "2002:a00:1::": false, "198.18.0.1": false, "2001:0:4136:e378::1": false, "3fff::1": false, "192.88.99.1": false, "240.0.0.1": false,
	} {
		if got := PublicAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("PublicAddr(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestDialerChecksEveryAnswerAndDialsTheCheckedAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	answers := 0
	d := &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			answers++
			// A public name that also answers with a private address, then with a public one.
			if answers == 1 {
				return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.5")}, nil
			}
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
		Allow: PublicAddr,
	}
	if _, err := d.DialContext(context.Background(), "tcp", "rebind.example.org:"+port); !errors.Is(err, ErrBlocked) {
		t.Fatalf("a name with one private answer: %v", err)
	}
	if _, err := d.DialContext(context.Background(), "tcp", "10.1.2.3:"+port); !errors.Is(err, ErrBlocked) {
		t.Fatalf("a private literal: %v", err)
	}
	if _, err := d.DialContext(context.Background(), "tcp", "rebind.example.org:"+port); !errors.Is(err, ErrBlocked) {
		t.Fatalf("a name that now answers loopback: %v", err)
	}
	none := &Dialer{Lookup: func(context.Context, string) ([]netip.Addr, error) { return nil, nil }, Allow: PublicAddr}
	if _, err := none.DialContext(context.Background(), "tcp", "x.example.org:1"); err == nil {
		t.Fatal("a name that does not resolve")
	}
	if _, err := none.DialContext(context.Background(), "tcp", "no-port"); err == nil {
		t.Fatal("an address with no port")
	}
}

func allowAll(netip.Addr) bool { return true }

func TestClientUsesNoProxyAndKeepsNoConnection(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hit = true }))
	defer srv.Close()
	c := (&Dialer{Lookup: SystemLookup, Allow: allowAll}).Client(time.Second, nil)
	tr := c.Transport.(*http.Transport)
	if tr.Proxy != nil || !tr.DisableKeepAlives {
		t.Fatalf("proxy set %v, keep-alives disabled %v", tr.Proxy != nil, tr.DisableKeepAlives)
	}
	resp, err := c.Get(srv.URL)
	if err != nil || !hit {
		t.Fatalf("the call did not go straight to the server: %v", err)
	}
	_ = resp.Body.Close()
}

func TestClientDoesNotFollowARedirect(t *testing.T) {
	var target atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			target.Add(1)
			return
		}
		http.Redirect(w, r, "/target", http.StatusFound)
	}))
	defer srv.Close()
	c := (&Dialer{Lookup: SystemLookup, Allow: allowAll}).Client(time.Second, nil)
	resp, err := c.Get(srv.URL + "/start")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound || target.Load() != 0 {
		t.Fatalf("status %d, target hits %d: the redirect was followed", resp.StatusCode, target.Load())
	}
}

func TestClientNeverConnectsToABlockedAddress(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	c := PublicDialer().Client(time.Second, nil)
	if _, err := c.Get(srv.URL); !errors.Is(err, ErrBlocked) || hits.Load() != 0 {
		t.Fatalf("err = %v, hits = %d: a loopback server was reached", err, hits.Load())
	}
}

func TestPublicDialerIsFreshEachCallAndAllowsPublicAddressesOnly(t *testing.T) {
	a, b := PublicDialer(), PublicDialer()
	if a == b {
		t.Fatal("two calls share one Dialer")
	}
	a.Client(time.Second, nil) // sets a's dial timeout
	if b.Net.Timeout != 0 {
		t.Fatal("building a client from one Dialer changed another")
	}
	if a.Allow(netip.MustParseAddr("10.0.0.1")) || !a.Allow(netip.MustParseAddr("93.184.216.34")) || a.Lookup == nil {
		t.Fatal("PublicDialer does not allow public addresses only")
	}
}
