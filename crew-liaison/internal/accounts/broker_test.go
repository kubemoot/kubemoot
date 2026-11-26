package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
)

const (
	crewA = "ns/guide"
	crewB = "ns/other"
)

type fakeDecls map[string]Declaration

func (f fakeDecls) Account(_ context.Context, name string) (Declaration, bool, error) {
	d, ok := f[name]
	return d, ok, nil
}

func (f fakeDecls) All(context.Context) ([]Declaration, error) {
	out := make([]Declaration, 0, len(f))
	for _, d := range f {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f fakeDecls) Declared(_ context.Context, crew string) ([]Declaration, error) {
	out := make([]Declaration, 0, len(f))
	for _, d := range f {
		if contains(d.Crews, crew) {
			out = append(out, d)
		}
	}
	return out, nil
}

// service is a token endpoint: it answers refreshes with a rotating refresh token.
type service struct {
	srv     *httptest.Server
	calls   atomic.Int32
	mu      sync.Mutex
	valid   string // the refresh token it accepts
	status  int    // forced status, 0 for normal
	seq     int
	expires int64
	body    func(r *http.Request)
}

// plainClient reaches the loopback servers the tests run; the guarded client is tested apart.
func plainClient() *Client {
	c := NewClient(&http.Client{})
	c.allowHTTP = true
	return c
}

func newService(t *testing.T) *service {
	s := &service{valid: "r1", expires: 3600}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *service) serve(w http.ResponseWriter, r *http.Request) {
	s.calls.Add(1)
	_ = r.ParseForm()
	if s.body != nil {
		s.body(r)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != 0 {
		w.WriteHeader(s.status)
		if s.status == http.StatusBadRequest {
			_, _ = w.Write([]byte(invalidGrant))
		}
		return
	}
	if r.PostForm.Get("grant_type") == "refresh_token" && r.PostForm.Get("refresh_token") != s.valid {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(invalidGrant))
		return
	}
	s.seq++
	s.valid = "r" + string(rune('1'+s.seq))
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "access-" + s.valid, "refresh_token": s.valid, "expires_in": s.expires, "refresh_token_expires_in": 86400 * 3,
	})
}

type rig struct {
	broker *Broker
	vault  *Vault
	clock  *clock
	svc    *service
}

func newRig(t *testing.T) *rig {
	t.Helper()
	svc := newService(t)
	regs := map[string]Registration{acctLib: {ClientID: testClientID, ClientSecret: "csecret", AllowedCrews: []string{crewGuide}}}
	v, c := newTestVault(t, regs)
	decls := fakeDecls{acctLib: {
		Name: acctLib, TokenURL: svc.srv.URL + "/token", TokenHosts: []string{apiHostName}, Crews: []string{crewA, crewB},
	}}
	client := plainClient()
	client.allowHTTP = true
	return &rig{broker: NewBroker(v, decls, regs, client, c.now), vault: v, clock: c, svc: svc}
}

func (r *rig) connect(t *testing.T, principal string, mutate func(*Token)) {
	t.Helper()
	tok := token(r.clock)
	tok.Crews = []string{crewA}
	if mutate != nil {
		mutate(&tok)
	}
	if _, err := r.vault.Save(context.Background(), principal, acctLib, tok, tokenstore.AnyRevision); err != nil {
		t.Fatal(err)
	}
}

func reasonOf(t *testing.T, err error) string {
	t.Helper()
	var le *LeaseError
	if !errors.As(err, &le) {
		t.Fatalf("want a LeaseError, got %v", err)
	}
	return le.Reason
}

func TestLeaseUsesAFreshTokenWithoutCallingTheService(t *testing.T) {
	r := newRig(t)
	r.connect(t, pAlice, nil)
	l, err := r.broker.Lease(context.Background(), pAlice, acctLib, crewA)
	if err != nil || l.Token != "a1" || l.TokenHosts[0] != apiHostName {
		t.Fatalf("lease: %+v %v", l, err)
	}
	if r.svc.calls.Load() != 0 {
		t.Fatal("a token with time left must not be refreshed")
	}
}

func TestLeaseRefusals(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.connect(t, pAlice, nil)
	cases := []struct {
		name, principal, account, crew, want string
	}{
		{"nobody connected", pBob, acctLib, crewA, ReasonNoGrant},
		{"account no crew declares", pAlice, "unknown", crewA, ReasonNoGrant},
		{"the install does not offer it to this crew", pAlice, acctLib, crewB, ReasonNoGrant},
		{"a crew that does not declare it", pAlice, acctLib, "ns/stranger", ReasonNoGrant},
	}
	for _, c := range cases {
		_, err := r.broker.Lease(ctx, c.principal, c.account, c.crew)
		if got := reasonOf(t, err); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestLeaseNeedsConsentForTheCrewAndAcceptsReconnects(t *testing.T) {
	r := newRig(t)
	// The install allows both crews and both declare it, but the person consented to one.
	r.broker.regs[acctLib] = Registration{ClientID: testClientID}
	r.connect(t, pAlice, nil)
	if _, err := r.broker.Lease(context.Background(), pAlice, acctLib, crewB); reasonOf(t, err) != ReasonNeedsReconnect {
		t.Fatalf("a crew the person never consented to: %v", err)
	}
	r.connect(t, "carol", func(t *Token) { t.State = StateNeedsReconnect })
	if _, err := r.broker.Lease(context.Background(), "carol", acctLib, crewA); reasonOf(t, err) != ReasonNeedsReconnect {
		t.Fatalf("a connection marked for reconnect: %v", err)
	}
}

func TestLeaseRefreshesRotatesAndWritesBeforeReturning(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.connect(t, pAlice, func(t *Token) { t.AccessExpires = r.clock.now().Add(10 * time.Second) })
	l, err := r.broker.Lease(ctx, pAlice, acctLib, crewA)
	if err != nil || l.Token != accessR2 {
		t.Fatalf("refresh: %+v %v", l, err)
	}
	stored, _, _ := r.vault.Get(ctx, pAlice, acctLib)
	if stored.Refresh != "r2" || stored.Access != accessR2 || stored.KeepUntil.IsZero() {
		t.Fatalf("the rotated token was not kept: %+v", stored)
	}
	if again, _ := r.broker.Lease(ctx, pAlice, acctLib, crewA); again.Token != accessR2 || r.svc.calls.Load() != 1 {
		t.Fatalf("a second lease must reuse the new token: %+v calls=%d", again, r.svc.calls.Load())
	}
}

func TestConcurrentLeasesShareOneRefresh(t *testing.T) {
	r := newRig(t)
	r.connect(t, pAlice, func(t *Token) { t.AccessExpires = r.clock.now().Add(5 * time.Second) })
	release := make(chan struct{})
	r.svc.body = func(*http.Request) { <-release }
	var wg sync.WaitGroup
	results := make([]string, 6)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := r.broker.Lease(context.Background(), pAlice, acctLib, crewA)
			if err == nil {
				results[i] = l.Token
			}
		}()
	}
	time.Sleep(150 * time.Millisecond)
	close(release)
	wg.Wait()
	for i, got := range results {
		if got != accessR2 {
			t.Errorf("caller %d got %q", i, got)
		}
	}
	if r.svc.calls.Load() != 1 {
		t.Fatalf("%d refresh calls, want 1", r.svc.calls.Load())
	}
}

func TestALostSwapUsesTheWinnersToken(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.connect(t, pAlice, func(t *Token) { t.AccessExpires = r.clock.now().Add(5 * time.Second) })
	// Another replica renews while this one is calling the service.
	r.svc.body = func(*http.Request) {
		cur, rev, _ := r.vault.Get(ctx, pAlice, acctLib)
		cur.Access, cur.Refresh, cur.AccessExpires = "winner", "rw", r.clock.now().Add(time.Hour)
		_, _ = r.vault.Save(ctx, pAlice, acctLib, cur, rev)
	}
	l, err := r.broker.Lease(ctx, pAlice, acctLib, crewA)
	if err != nil || l.Token != "winner" {
		t.Fatalf("a replica that lost the swap: %+v %v", l, err)
	}
}

func TestInvalidGrantMarksReconnectAndStopsAsking(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.connect(t, pAlice, func(t *Token) { t.AccessExpires = r.clock.now().Add(5 * time.Second); t.Refresh = "revoked" })
	if _, err := r.broker.Lease(ctx, pAlice, acctLib, crewA); reasonOf(t, err) != ReasonNeedsReconnect {
		t.Fatalf("first: %v", err)
	}
	calls := r.svc.calls.Load()
	if _, err := r.broker.Lease(ctx, pAlice, acctLib, crewA); reasonOf(t, err) != ReasonNeedsReconnect {
		t.Fatalf("second: %v", err)
	}
	if r.svc.calls.Load() != calls {
		t.Fatal("a connection marked for reconnect must not call the service again")
	}
	conns, _ := r.vault.List(ctx, pAlice)
	if len(conns) != 1 || conns[0].State != StateNeedsReconnect {
		t.Fatalf("what the person sees: %+v", conns)
	}
}

func TestServiceDownIsAnErrorNotAReconnect(t *testing.T) {
	r := newRig(t)
	r.connect(t, pAlice, func(t *Token) { t.AccessExpires = r.clock.now().Add(5 * time.Second) })
	r.svc.status = http.StatusBadGateway
	_, err := r.broker.Lease(context.Background(), pAlice, acctLib, crewA)
	var le *LeaseError
	if err == nil || errors.As(err, &le) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a service that is away: %v", err)
	}
	stored, _, _ := r.vault.Get(context.Background(), pAlice, acctLib)
	if stored.State == StateNeedsReconnect {
		t.Fatal("an outage must not end the connection")
	}
}

func TestLeaseWithoutARefreshTokenAndAnEndedLimit(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.connect(t, pAlice, func(t *Token) { t.Refresh = ""; t.AccessExpires = r.clock.now().Add(5 * time.Second) })
	if _, err := r.broker.Lease(ctx, pAlice, acctLib, crewA); reasonOf(t, err) != ReasonNeedsReconnect {
		t.Fatalf("an ending access token with no refresh token: %v", err)
	}
	r.connect(t, "dave", func(t *Token) { t.KeepUntil = r.clock.now().Add(time.Hour) })
	r.clock.advance(2 * time.Hour)
	// The record itself has gone with the limit, which reads as nothing connected.
	if _, err := r.broker.Lease(ctx, "dave", acctLib, crewA); reasonOf(t, err) != ReasonNoGrant {
		t.Fatalf("after the service's limit: %v", err)
	}
}

func TestExpiredLimitOnALiveRecord(t *testing.T) {
	r := newRig(t)
	r.connect(t, pAlice, func(t *Token) { t.KeepUntil = r.clock.now().Add(time.Hour) })
	// The vault caps the time to live at the limit, so a record past it is gone; a record
	// whose limit passed while its time to live did not (a clock skew) is deleted on lease.
	tok, rev, _ := r.vault.Get(context.Background(), pAlice, acctLib)
	tok.KeepUntil = r.clock.now().Add(-time.Second)
	_, _ = r.vault.store.Put(context.Background(), recordOf(pAlice, acctLib, tok), time.Hour, rev)
	if _, err := r.broker.Lease(context.Background(), pAlice, acctLib, crewA); reasonOf(t, err) != ReasonExpired {
		t.Fatalf("a limit that has passed: %v", err)
	}
	if _, _, err := r.vault.Get(context.Background(), pAlice, acctLib); !errors.Is(err, ErrNotConnected) {
		t.Fatal("the record must be deleted")
	}
}

func recordOf(principal, account string, tok Token) tokenstore.Record {
	raw, _ := json.Marshal(tok)
	return tokenstore.Record{Principal: principal, Account: account, Value: raw}
}

func TestClientCallsSendCredentialsAndRefuseUnsafeEndpoints(t *testing.T) {
	var gotAuth, gotCT string
	var gotForm string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotCT = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		_ = r.ParseForm()
		gotForm = r.PostForm.Encode()
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":60,"refresh_expires_in":120}`))
	}))
	defer srv.Close()
	c := plainClient()
	d := Declaration{TokenURL: srv.URL}
	c.allowHTTP = true
	tok, err := c.Exchange(context.Background(), d, Registration{ClientID: testClientID, ClientSecret: "sec"}, "code", "ver", "https://door/cb")
	if err != nil || tok.Access != "a" || tok.RefreshLifetime() != 2*time.Minute {
		t.Fatalf("exchange: %+v %v", tok, err)
	}
	if !strings.HasPrefix(gotAuth, "Basic ") || gotCT != "application/x-www-form-urlencoded" ||
		!strings.Contains(gotForm, "code_verifier=ver") || strings.Contains(gotForm, "client_id") {
		t.Fatalf("a confidential client: auth=%q form=%q", gotAuth, gotForm)
	}
	_, err = c.Exchange(context.Background(), d, Registration{ClientID: "pub"}, "code", "ver", "https://door/cb")
	publicClientSendsItsIDInTheForm(t, err, gotAuth, gotForm)
}

func publicClientSendsItsIDInTheForm(t *testing.T, err error, auth, form string) {
	t.Helper()
	if err != nil || !strings.Contains(form, "client_id=pub") || auth != "" {
		t.Fatalf("a public client: auth=%q form=%q %v", auth, form, err)
	}
}

func TestClientRefusesUnsafeEndpoints(t *testing.T) {
	c := NewClient(&http.Client{})
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	// https only: a plain http endpoint is refused before any request.
	if _, err := c.Exchange(context.Background(), Declaration{TokenURL: srv.URL}, Registration{ClientID: "x"}, "code", "ver", "https://door/cb"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("plain http: %v", err)
	}
	c.allowHTTP = true
	for _, bad := range []string{"", "::", "ftp://x", "https://"} {
		if _, err := c.Refresh(context.Background(), Declaration{TokenURL: bad}, Registration{}, "r"); !errors.Is(err, ErrUnavailable) {
			t.Errorf("endpoint %q: %v", bad, err)
		}
	}
}

func TestClientDoesNotFollowRedirectsAndBoundsAnswers(t *testing.T) {
	var hit atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit.Add(1) }))
	defer target.Close()
	redirecting := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	defer redirecting.Close()
	c := plainClient()
	c.allowHTTP = true
	if _, err := c.Refresh(context.Background(), Declaration{TokenURL: redirecting.URL}, Registration{}, "r"); err == nil {
		t.Fatal("a redirect is not tokens")
	}
	if hit.Load() != 0 {
		t.Fatal("a redirect was followed")
	}
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"` + strings.Repeat("a", maxServiceReply) + `"}`))
	}))
	defer big.Close()
	if _, err := c.Refresh(context.Background(), Declaration{TokenURL: big.URL}, Registration{}, "r"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an oversized answer: %v", err)
	}
}

func TestRevoke(t *testing.T) {
	var form string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm.Encode()
		w.WriteHeader(status)
	}))
	defer srv.Close()
	c := plainClient()
	c.allowHTTP = true
	d := Declaration{RevocationURL: srv.URL}
	if err := c.Revoke(context.Background(), d, Registration{ClientID: "c"}, "tok", "refresh_token"); err != nil || !strings.Contains(form, "token=tok") {
		t.Fatalf("revoke: %v %q", err, form)
	}
	status = http.StatusInternalServerError
	if err := c.Revoke(context.Background(), d, Registration{}, "tok", "refresh_token"); err == nil {
		t.Fatal("a failing service must be reported")
	}
	if err := c.Revoke(context.Background(), Declaration{}, Registration{}, "tok", ""); err == nil {
		t.Fatal("no endpoint")
	}
}

func TestCheckSaysWhyWithoutCallingTheService(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.connect(t, pAlice, func(t *Token) { t.AccessExpires = r.clock.now().Add(5 * time.Second) })
	r.connect(t, "carol", func(t *Token) { t.State = StateNeedsReconnect })
	r.connect(t, "dave", func(t *Token) { t.KeepUntil = r.clock.now().Add(time.Hour) })
	r.clock.advance(30 * time.Minute)
	cases := []struct{ name, principal, account, crew, want string }{
		{"live even when its access token is about to end", pAlice, acctLib, crewA, ""},
		{"nobody connected", pBob, acctLib, crewA, ReasonNoGrant},
		{"a crew that is not offered it", pAlice, acctLib, crewB, ReasonNoGrant},
		{"an unknown account", pAlice, "unknown", crewA, ReasonNoGrant},
		{"marked for reconnect", "carol", acctLib, crewA, ReasonNeedsReconnect},
		{"consent for another crew", pAlice, acctLib, crewA, ""},
	}
	for _, c := range cases {
		got, err := r.broker.Check(ctx, c.principal, c.account, c.crew)
		if err != nil || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.name, got, err, c.want)
		}
	}
	if r.svc.calls.Load() != 0 {
		t.Fatal("a check must not call the service")
	}
	if got, _ := r.broker.Check(ctx, "dave", acctLib, crewA); got != "" {
		t.Errorf("a limit in the future: %q", got)
	}
	r.clock.advance(time.Hour)
	if got, _ := r.broker.Check(ctx, "dave", acctLib, crewA); got != ReasonNoGrant && got != ReasonExpired {
		t.Errorf("past the limit: %q", got)
	}
}
