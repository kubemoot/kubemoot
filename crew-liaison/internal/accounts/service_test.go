package accounts

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newService2(t *testing.T) (*Service, *rig) {
	t.Helper()
	r := newRig(t)
	r.broker.regs["lib"] = Registration{ClientID: testClientID, AllowedCrews: []string{crewGuide}}
	return &Service{Vault: r.vault, Decls: r.broker.decls, Regs: r.broker.regs, Client: r.broker.client, Now: r.clock.now}, r
}

func TestPrincipalIDIsStableAndPerPerson(t *testing.T) {
	a, b := PrincipalID("oidc:alice"), PrincipalID("oidc:bob")
	if a == b || a != PrincipalID("oidc:alice") || len(a) != 32 {
		t.Fatalf("ids: %q %q", a, b)
	}
}

func TestOfferedChecksRegistrationDeclarationAndAllowedCrews(t *testing.T) {
	s, _ := newService2(t)
	ctx := context.Background()
	o, err := s.Offered(ctx, acctLib)
	if err != nil || len(o.Crews) != 1 || o.Crews[0] != crewA {
		t.Fatalf("offered: %+v %v", o, err)
	}
	if _, err := s.Offered(ctx, "unregistered"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("an account the install did not register: %v", err)
	}
	s.Regs["nodecl"] = Registration{ClientID: "c"}
	if _, err := s.Offered(ctx, "nodecl"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("an account no crew declares: %v", err)
	}
	s.Regs[acctLib] = Registration{ClientID: "c", AllowedCrews: []string{"nobody"}}
	if _, err := s.Offered(ctx, acctLib); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("no allowed crew: %v", err)
	}
	if _, err := s.OfferedFor(ctx, acctLib, crewA); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("offered for a crew that is not allowed: %v", err)
	}
	s.Regs[acctLib] = Registration{ClientID: "c"}
	if _, err := s.OfferedFor(ctx, acctLib, "ns/stranger"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("offered for a crew that does not declare it: %v", err)
	}
	if _, err := s.OfferedFor(ctx, acctLib, crewB); err != nil {
		t.Fatalf("offered for a declaring crew: %v", err)
	}
}

func TestCompleteKeepsTheTokensWithTheServicesLimits(t *testing.T) {
	s, r := newService2(t)
	ctx := context.Background()
	o, _ := s.Offered(ctx, acctLib)
	r.svc.valid = "code"
	r.svc.body = func(req *http.Request) {
		if req.PostForm.Get("grant_type") == grantCode && req.PostForm.Get("code_verifier") != "ver" {
			t.Errorf("the verifier did not reach the service")
		}
	}
	if err := s.Complete(ctx, "alice", o, []string{crewA}, "code", "ver", "https://door/oauth/callback"); err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.Vault.Get(ctx, "alice", acctLib)
	if err != nil || tok.Access == "" || tok.Refresh == "" || tok.State != StateConnected || tok.KeepUntil.IsZero() {
		t.Fatalf("the kept token: %+v %v", tok, err)
	}
	r.svc.status = http.StatusBadRequest
	if err := s.Complete(ctx, "alice", o, nil, "bad", "ver", "https://door/cb"); !errors.Is(err, ErrRefused) {
		t.Fatalf("a refused code: %v", err)
	}
}

// revocationRig is a service whose revocation endpoint records what it was sent.
type revocationRig struct {
	srv   *httptest.Server
	calls atomic.Int32
	last  atomic.Value
}

func newRevocationRig(t *testing.T) *revocationRig {
	rr := &revocationRig{}
	rr.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		_ = q.ParseForm()
		rr.calls.Add(1)
		rr.last.Store(q.PostForm.Get("token") + "/" + q.PostForm.Get("token_type_hint"))
		if q.PostForm.Get("token") == "r-fail" {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(rr.srv.Close)
	return rr
}

func (rr *revocationRig) got() string { v, _ := rr.last.Load().(string); return v }

func TestDisconnectRevokesThenDeletesEvenWhenRevocationFails(t *testing.T) {
	s, r := newService2(t)
	ctx := context.Background()
	rr := newRevocationRig(t)
	d := s.Decls.(fakeDecls)[acctLib]
	d.RevocationURL = rr.srv.URL
	s.Decls.(fakeDecls)[acctLib] = d
	s.Client.allowHTTP = true

	r.connect(t, "alice", func(t *Token) { t.Refresh = "r-ok" })
	out, err := s.Disconnect(ctx, "alice", acctLib)
	if err != nil || !out.Asked || !out.Revoked || rr.got() != "r-ok/refresh_token" {
		t.Fatalf("a revoked disconnect: %+v %v %q", out, err, rr.got())
	}
	r.connect(t, "bob", func(t *Token) { t.Refresh = "r-fail" })
	out, err = s.Disconnect(ctx, "bob", acctLib)
	if err != nil || !out.Asked || out.Revoked {
		t.Fatalf("a failed revocation: %+v %v", out, err)
	}
	for _, who := range []string{"alice", "bob"} {
		if _, _, err := s.Vault.Get(ctx, who, acctLib); !errors.Is(err, ErrNotConnected) {
			t.Fatalf("%s: the record must be deleted whether or not the service answered", who)
		}
	}
	revokesByAccessTokenWhenThereIsNoRefreshToken(t, s, r, rr)
}

func revokesByAccessTokenWhenThereIsNoRefreshToken(t *testing.T, s *Service, r *rig, rr *revocationRig) {
	t.Helper()
	ctx := context.Background()
	r.connect(t, "carol", func(t *Token) { t.Refresh = "" })
	if out, _ := s.Disconnect(ctx, "carol", acctLib); !out.Asked || !strings.HasSuffix(rr.got(), "/access_token") {
		t.Fatalf("access token revoke: %+v %q", out, rr.got())
	}
	// Nothing connected: nothing asked, no error.
	if out, err := s.Disconnect(ctx, "nobody", acctLib); err != nil || out.Asked {
		t.Fatalf("nothing to disconnect: %+v %v", out, err)
	}
}

func TestDisconnectWithoutARevocationEndpointDoesNotAsk(t *testing.T) {
	s, r := newService2(t)
	r.connect(t, "alice", nil)
	out, err := s.Disconnect(context.Background(), "alice", acctLib)
	if err != nil || out.Asked {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestOverviewShowsConnectedAndMissingAndHidesUnofferedAccounts(t *testing.T) {
	s, r := newService2(t)
	ctx := context.Background()
	r.connect(t, "alice", nil)
	s.Regs["mail"] = Registration{ClientID: "m"}
	declared := []Declaration{
		{Name: acctLib, DisplayName: "Library", Crews: []string{crewA}, Required: true},
		{Name: "mail", DisplayName: "Mail", Crews: []string{crewA}},
		{Name: "secret", DisplayName: "Not registered", Crews: []string{crewA}},
	}
	got, err := s.overviewOf(ctx, "alice", declared)
	if err != nil || len(got) != 2 || got[0].State != StateConnected || got[1].State != StateMissing || got[0].DisplayName != "Library" {
		t.Fatalf("overview: %+v %v", got, err)
	}
	other, _ := s.overviewOf(ctx, "bob", declared)
	if other[0].State != StateMissing {
		t.Fatalf("another person sees a connection: %+v", other)
	}
	s.Regs["mail"] = Registration{ClientID: "m", AllowedCrews: []string{"nobody"}}
	if got, _ := s.overviewOf(ctx, "alice", declared); len(got) != 1 {
		t.Fatalf("an account no declaring crew may use: %+v", got)
	}
}

func TestIssuerAccepted(t *testing.T) {
	d := Declaration{AuthorizeURL: "https://auth.example.org/oauth2/authorize"}
	cases := []struct {
		name, iss string
		reg       Registration
		want      bool
	}{
		{"explicit match", issuerURL, Registration{Issuer: issuerURL}, true},
		{"explicit mismatch", "https://evil.example.net", Registration{Issuer: issuerURL}, false},
		{"host of the endpoint", "https://auth.example.org/tenant", Registration{}, true},
		{"another host", "https://evil.example.net", Registration{}, false},
		{"not https", "http://auth.example.org", Registration{}, false},
		{"garbage", "::", Registration{}, false},
	}
	for _, c := range cases {
		if IssuerAccepted(c.iss, c.reg, d) != c.want {
			t.Errorf("%s", c.name)
		}
	}
}

func TestScopesOf(t *testing.T) {
	if got := scopesOf("a b", []string{"x"}); len(got) != 2 || got[0] != "a" {
		t.Errorf("granted: %v", got)
	}
	if got := scopesOf("", []string{"x"}); len(got) != 1 || got[0] != "x" {
		t.Errorf("asked: %v", got)
	}
	_ = time.Second
}

func TestOverviewReadsEveryDeclaration(t *testing.T) {
	s, r := newService2(t)
	r.connect(t, "alice", nil)
	got, err := s.Overview(context.Background(), "alice")
	if err != nil || len(got) != 1 || got[0].Account != acctLib || got[0].State != StateConnected {
		t.Fatalf("overview: %+v %v", got, err)
	}
	if n, err := s.SignOut(context.Background(), "alice"); err != nil || n != 1 {
		t.Fatalf("sign out: %d %v", n, err)
	}
}
