package liaison

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/accounts"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
)

const (
	gatewaySubject = "system:serviceaccount:ns:guide-discussion"
	acctLibrary    = "library"
	apiHost        = "api.example.org"
	guideCrew      = "ns/guide"
)

type fakeReviewer map[string]*identity.Identity

func (f fakeReviewer) Review(_ context.Context, token string) (*identity.Identity, error) {
	if token == "" {
		return nil, identity.ErrNoCredential
	}
	id, ok := f[token]
	if !ok {
		return nil, identity.ErrInvalid
	}
	return id, nil
}

type brokerRig struct {
	handler http.Handler
	vault   *accounts.Vault
}

type sealerStub struct{}

func (sealerStub) Seal(_ string, v any) (string, error) {
	raw, err := json.Marshal(v)
	return string(raw), err
}

func (sealerStub) Open(_, blob string, v any) error { return json.Unmarshal([]byte(blob), v) }

func newBrokerRig(t *testing.T) *brokerRig {
	t.Helper()
	regs := map[string]accounts.Registration{acctLibrary: {ClientID: "c"}}
	store, err := tokenstore.NewSealed(tokenstore.NewMemory(nil), sealerStub{}, 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	vault, _ := accounts.NewVault(store, regs, nil)
	decls := fakeAccountDecls{acctLibrary: {
		Name: acctLibrary, TokenURL: "https://auth.example.org/t", TokenHosts: []string{apiHost},
		Crews: []string{guideCrew},
	}}
	broker := accounts.NewBroker(vault, decls, regs, accounts.NewClient(nil), nil)
	reviewer := fakeReviewer{
		"gateway-token": {Subject: gatewaySubject},
		"other-token":   {Subject: "system:serviceaccount:ns:someone"},
		"user-token":    {Subject: "oidc:alice"},
	}
	return &brokerRig{handler: NewAccountBroker(broker, reviewer).Handler(), vault: vault}
}

type fakeAccountDecls map[string]accounts.Declaration

func (f fakeAccountDecls) Account(_ context.Context, n string) (accounts.Declaration, bool, error) {
	d, ok := f[n]
	return d, ok, nil
}
func (f fakeAccountDecls) All(context.Context) ([]accounts.Declaration, error) {
	out := make([]accounts.Declaration, 0, len(f))
	for _, d := range f {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f fakeAccountDecls) Declared(ctx context.Context, crew string) ([]accounts.Declaration, error) {
	all, _ := f.All(ctx)
	var out []accounts.Declaration
	for _, d := range all {
		for _, c := range d.Crews {
			if c == crew {
				out = append(out, d)
			}
		}
	}
	return out, nil
}

func (r *brokerRig) call(path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	r.handler.ServeHTTP(rec, req)
	return rec
}

func (r *brokerRig) connect(t *testing.T, mutate func(*accounts.Token)) {
	t.Helper()
	tok := accounts.Token{
		Access: "leased-access-token", Refresh: "r", AccessExpires: time.Now().Add(time.Hour), GrantedAt: time.Now(),
		LastUsed: time.Now(), Crews: []string{guideCrew},
	}
	if mutate != nil {
		mutate(&tok)
	}
	if _, err := r.vault.Save(context.Background(), "alice", acctLibrary, tok, tokenstore.AnyRevision); err != nil {
		t.Fatal(err)
	}
}

const (
	leasePath     = "/internal/v1/accounts/lease"
	connectedPath = "/internal/v1/accounts/connected"
	askAlice      = `{"principalId":"alice","account":"library"}`
)

func TestBrokerLeasesToTheGatewayOfTheCrew(t *testing.T) {
	r := newBrokerRig(t)
	r.connect(t, nil)
	rec := r.call(leasePath, "gateway-token", askAlice)
	var got leaseAnswer
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || got.Token != "leased-access-token" || got.TokenHosts[0] != apiHost || got.ExpiresAt == "" {
		t.Fatalf("lease: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("a lease must not be cached")
	}
}

func TestBrokerRefusalsCarryTheReason(t *testing.T) {
	r := newBrokerRig(t)
	cases := []struct {
		name   string
		setup  func()
		want   int
		reason string
	}{
		{"nothing connected", func() {}, http.StatusNotFound, accounts.ReasonNoGrant},
		{"marked for reconnect", func() { r.connect(t, func(t *accounts.Token) { t.State = accounts.StateNeedsReconnect }) },
			http.StatusConflict, accounts.ReasonNeedsReconnect},
		{"a crew the person never consented to", func() { r.connect(t, func(t *accounts.Token) { t.Crews = []string{"ns/other"} }) },
			http.StatusConflict, accounts.ReasonNeedsReconnect},
	}
	for _, c := range cases {
		_ = r.vault.Disconnect(context.Background(), "alice", acctLibrary)
		c.setup()
		rec := r.call(leasePath, "gateway-token", askAlice)
		var got reasonAnswer
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if rec.Code != c.want || got.Reason != c.reason || strings.Contains(rec.Body.String(), "leased-access-token") {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
	}
}

func TestBrokerRefusesCallersThatAreNotACrewsGateway(t *testing.T) {
	r := newBrokerRig(t)
	r.connect(t, nil)
	cases := map[string]struct {
		token string
		want  int
	}{
		"no token":                {"", http.StatusUnauthorized},
		"a token that is invalid": {"forged", http.StatusUnauthorized},
		"another ServiceAccount":  {"other-token", http.StatusForbidden},
		"a person's token":        {"user-token", http.StatusForbidden},
	}
	for name, c := range cases {
		if rec := r.call(leasePath, c.token, askAlice); rec.Code != c.want || strings.Contains(rec.Body.String(), "leased-access-token") {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
}

func TestBrokerAnswersForTheCrewOfTheCallerNotOfTheRequest(t *testing.T) {
	r := newBrokerRig(t)
	r.connect(t, nil)
	// A request that names another crew is refused as an unknown field; the crew is the caller's.
	rec := r.call(leasePath, "gateway-token", `{"principalId":"alice","account":"library","crew":"ns/other"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a crew in the body: %d", rec.Code)
	}
}

func TestBrokerRejectsMalformedRequests(t *testing.T) {
	r := newBrokerRig(t)
	for name, body := range map[string]string{
		"not json": "x", "no principal": `{"account":"library"}`, "no account": `{"principalId":"alice"}`,
		"too large": `{"principalId":"` + strings.Repeat("a", maxBrokerRequestBytes) + `","account":"library"}`,
	} {
		if rec := r.call(leasePath, "gateway-token", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
}

func TestBrokerConnectedCheckNeverReturnsATokenOrRefreshes(t *testing.T) {
	r := newBrokerRig(t)
	for _, c := range []struct {
		name   string
		setup  func()
		live   bool
		reason string
	}{
		{"connected", func() { r.connect(t, nil) }, true, ""},
		{"not connected", func() { _ = r.vault.Disconnect(context.Background(), "alice", acctLibrary) }, false, accounts.ReasonNoGrant},
	} {
		c.setup()
		rec := r.call(connectedPath, "gateway-token", askAlice)
		var got reasonAnswer
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if rec.Code != http.StatusOK || got.Connected == nil || *got.Connected != c.live || got.Reason != c.reason ||
			strings.Contains(rec.Body.String(), "leased-access-token") {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
	}
	if rec := r.call(connectedPath, "", askAlice); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", rec.Code)
	}
}

func TestCrewOfGateway(t *testing.T) {
	for subject, want := range map[string]string{
		"system:serviceaccount:ns:guide-discussion": guideCrew,
		"system:serviceaccount:a-b:x-y-discussion":  "a-b/x-y",
		"system:serviceaccount:ns:guide":            "",
		"system:serviceaccount::guide-discussion":   "",
		"system:serviceaccount:ns:-discussion":      "",
		"oidc:alice":                                "",
		"system:serviceaccount:ns":                  "",
		"system:serviceaccount:ns:a:b-discussion":   "",
	} {
		got, ok := crewOfGateway(subject)
		if got != want || ok != (want != "") {
			t.Errorf("%q: %q %v", subject, got, ok)
		}
	}
}
