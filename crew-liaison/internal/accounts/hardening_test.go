package accounts

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/netguard"
)

const (
	evilCrew   = "a/aaa"
	evilTokens = "https://evil.example.net/token"
)

func decl(crews []string, tokenURL string) Declaration {
	return Declaration{
		Name: acctLib, DisplayName: libraryTitle, Crews: crews, AuthorizeURL: "https://auth.example.org/a",
		TokenURL: tokenURL, TokenHosts: []string{apiHostName},
	}
}

func TestOnlyCrewsTheInstallAllowsSupplyTheEndpoints(t *testing.T) {
	all := []Declaration{decl([]string{evilCrew}, evilTokens), decl([]string{guideFull}, authToken)}
	reg := Registration{ClientID: "c", AllowedCrews: []string{guideFull}}
	o, err := resolve(all, acctLib, reg)
	if err != nil || o.Declaration.TokenURL != authToken || len(o.Crews) != 1 || o.Crews[0] != guideFull {
		t.Fatalf("a crew the install did not allow must not supply the token endpoint: %+v %v", o, err)
	}
	// The order of the declarations does not matter.
	if o, _ := resolve([]Declaration{all[1], all[0]}, acctLib, reg); o.Declaration.TokenURL != authToken {
		t.Fatalf("reordered: %+v", o)
	}
}

func TestAllowedCrewsThatDisagreeOnTheServiceGetNothing(t *testing.T) {
	all := []Declaration{decl([]string{evilCrew}, evilTokens), decl([]string{guideFull}, authToken)}
	for name, reg := range map[string]Registration{
		"every crew allowed": {ClientID: "c"},
		"both named":         {ClientID: "c", AllowedCrews: []string{"aaa", "guide"}},
	} {
		if _, err := resolve(all, acctLib, reg); !errors.Is(err, ErrConflict) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Crews that agree are one offer, whatever their names.
	same := []Declaration{decl([]string{evilCrew}, authToken), decl([]string{guideFull}, authToken)}
	if o, err := resolve(same, acctLib, Registration{ClientID: "c"}); err != nil || len(o.Crews) != 2 {
		t.Fatalf("agreeing crews: %+v %v", o, err)
	}
	if _, err := resolve(all, "other", Registration{}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("an account nobody declares: %v", err)
	}
}

func TestAPinnedRegistrationDropsADeclarationElsewhere(t *testing.T) {
	all := []Declaration{decl([]string{evilCrew}, evilTokens), decl([]string{guideFull}, authToken)}
	pinned := Registration{ClientID: "c", Hosts: []string{"auth.example.org"}}
	o, err := resolve(all, acctLib, pinned)
	if err != nil || o.Declaration.TokenURL != authToken || len(o.Crews) != 1 {
		t.Fatalf("pinned: %+v %v", o, err)
	}
	elsewhere := decl([]string{guideFull}, authToken)
	elsewhere.RevocationURL = "https://evil.example.net/revoke"
	if _, err := resolve([]Declaration{elsewhere}, acctLib, pinned); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("a revocation endpoint off the pinned hosts: %v", err)
	}
	if !(Registration{}).PinsAllow(elsewhere) || (Registration{Hosts: []string{"x"}}).PinsAllow(Declaration{TokenURL: "::"}) {
		t.Error("no pin allows every host, and an address that is not a URL is never allowed")
	}
}

func TestACrewListFailureIsNotAnAnswerAboutTheAccount(t *testing.T) {
	r := newRig(t)
	r.connect(t, pAlice, nil)
	r.broker.decls = brokenDecls{}
	_, err := r.broker.Lease(context.Background(), pAlice, acctLib, crewA)
	var le *LeaseError
	if err == nil || errors.As(err, &le) {
		t.Fatalf("an API failure must not read as NO_GRANT: %v", err)
	}
	if reason, err := r.broker.Check(context.Background(), pAlice, acctLib, crewA); err == nil || reason != "" {
		t.Fatalf("check: %q %v", reason, err)
	}
	if _, _, err := r.broker.offered(context.Background(), acctLib, crewA); !unreadable(err) {
		t.Fatalf("offered: %v", err)
	}
}

type brokenDecls struct{ fakeDecls }

func (brokenDecls) All(context.Context) ([]Declaration, error) { return nil, errors.New("api away") }

func TestCrewNamesMatchExactly(t *testing.T) {
	if contains([]string{"ns/Guide"}, "ns/guide") || !contains([]string{"ns/guide"}, "ns/guide") {
		t.Fatal("crew names are case sensitive")
	}
}

func TestOnlyAnInvalidGrantOrClientEndsAConnection(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   error
	}{
		{400, invalidGrant, ErrRefused},
		{401, `{"error":"invalid_client"}`, ErrRefused},
		{400, `{"error":"invalid_request"}`, ErrUnavailable},
		{400, ``, ErrUnavailable},
		{401, `not json`, ErrUnavailable},
		{408, invalidGrant, ErrUnavailable},
		{429, ``, ErrUnavailable},
		{403, invalidGrant, ErrUnavailable},
		{500, invalidGrant, ErrUnavailable},
		{200, `{"error":"x"}`, ErrUnavailable},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(c.body))
		}))
		_, err := plainClient().Refresh(context.Background(), Declaration{TokenURL: srv.URL}, Registration{}, "r")
		srv.Close()
		if !errors.Is(err, c.want) {
			t.Errorf("%d %q: %v, want %v", c.status, c.body, err, c.want)
		}
	}
}

func TestARateLimitedServiceDoesNotBreakTheConnection(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.connect(t, pAlice, func(t *Token) { t.AccessExpires = r.clock.now().Add(5 * time.Second) })
	r.svc.status = http.StatusTooManyRequests
	if _, err := r.broker.Lease(ctx, pAlice, acctLib, crewA); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("429: %v", err)
	}
	tok, _, _ := r.vault.Get(ctx, pAlice, acctLib)
	if tok.State == StateNeedsReconnect || tok.Refresh == "" {
		t.Fatalf("a 429 must not mark the connection: %+v", tok)
	}
	r.svc.status = 0
	if l, err := r.broker.Lease(ctx, pAlice, acctLib, crewA); err != nil || l.Token == "" {
		t.Fatalf("the next lease: %+v %v", l, err)
	}
}

// TestTheGuardedClientNeverReachesAPrivateAddress runs a real TLS service on loopback and shows the
// default client does not connect to it, whether it is named by address or by a name that resolves there.
func TestTheGuardedClientNeverReachesAPrivateAddress(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"access_token":"a"}`))
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	c := NewClient(nil)
	for _, endpoint := range []string{srv.URL + "/token", "https://10.1.2.3:" + port + "/token", "https://[::1]:" + port + "/token"} {
		if _, err := c.Refresh(context.Background(), Declaration{TokenURL: endpoint}, Registration{}, "r"); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v", endpoint, err)
		}
		if err := c.Revoke(context.Background(), Declaration{RevocationURL: endpoint}, Registration{}, "t", "refresh_token"); err == nil {
			t.Errorf("revoke %s was sent", endpoint)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("a private address was reached %d times", hits.Load())
	}
}

func TestTheGuardedClientRefusesANameWithAPrivateAnswer(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	var mu sync.Mutex
	asked := 0
	lookup := func(context.Context, string) ([]netip.Addr, error) {
		mu.Lock()
		defer mu.Unlock()
		asked++
		// A public address beside a private one the first time, loopback after: one bad answer
		// refuses the name, and the connection is never made to the public one alone.
		if asked == 1 {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.5")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	hc := guardedHTTP(&netguard.Dialer{Lookup: lookup, Allow: netguard.PublicAddr})
	hc.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test server
	c := NewClient(hc)
	for range 2 {
		_, _ = c.Refresh(context.Background(), Declaration{TokenURL: "https://rebind.example.org:" + port + "/t"}, Registration{}, "r")
	}
	if hits.Load() != 0 {
		t.Fatalf("a name that moved to loopback reached the server %d times", hits.Load())
	}
}

func TestTwoBrokersOnOneStoreRefreshOnce(t *testing.T) {
	r := newRig(t)
	other := NewBroker(r.vault, r.broker.decls, r.broker.regs, r.broker.client, r.clock.now)
	r.connect(t, pAlice, func(t *Token) { t.AccessExpires = r.clock.now().Add(5 * time.Second) })
	release := make(chan struct{})
	r.svc.body = func(*http.Request) { <-release }
	results := make(chan string, 2)
	for _, b := range []*Broker{r.broker, other} {
		go func() {
			l, err := b.Lease(context.Background(), pAlice, acctLib, crewA)
			if err != nil {
				results <- "error: " + err.Error()
				return
			}
			results <- l.Token
		}()
	}
	time.Sleep(300 * time.Millisecond) // the loser is waiting for the winner
	close(release)
	for range 2 {
		if got := <-results; got != accessR2 {
			t.Errorf("a broker got %q", got)
		}
	}
	if r.svc.calls.Load() != 1 {
		t.Fatalf("%d refresh calls across two brokers, want 1", r.svc.calls.Load())
	}
	if ok, _ := r.vault.TryLock(context.Background(), pAlice, acctLib, time.Minute); !ok {
		t.Fatal("the refresh lease must be given back")
	}
}

func TestALoserGivesUpWhenTheWinnerNeverWrites(t *testing.T) {
	r := newRig(t)
	r.connect(t, pAlice, func(t *Token) { t.AccessExpires = r.clock.now().Add(5 * time.Second) })
	if ok, _ := r.vault.TryLock(context.Background(), pAlice, acctLib, time.Minute); !ok {
		t.Fatal("setup")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if _, err := r.broker.Lease(ctx, pAlice, acctLib, crewA); err == nil {
		t.Fatal("a lease with the refresh lease held elsewhere and a dead winner")
	}
	if r.svc.calls.Load() != 0 {
		t.Fatal("the loser must not ask the service")
	}
}

func TestTheRefreshLeaseIsPerRecord(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if ok, _ := r.vault.TryLock(ctx, pAlice, acctLib, time.Minute); !ok {
		t.Fatal("first")
	}
	if ok, _ := r.vault.TryLock(ctx, pAlice, acctLib, time.Minute); ok {
		t.Fatal("the same record twice")
	}
	if ok, _ := r.vault.TryLock(ctx, pBob, acctLib, time.Minute); !ok {
		t.Fatal("another person's record")
	}
	r.vault.Unlock(ctx, pAlice, acctLib)
	if ok, _ := r.vault.TryLock(ctx, pAlice, acctLib, time.Minute); !ok {
		t.Fatal("after unlock")
	}
}
