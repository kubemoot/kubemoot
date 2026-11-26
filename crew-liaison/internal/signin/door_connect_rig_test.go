package signin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/accounts"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
)

const (
	acctLibrary     = "library"
	libraryCrew     = "ns/guide"
	upstreamAccess  = "upstream-access-token-xyz"
	upstreamRefresh = "upstream-refresh-token-xyz"
	connectUpCode   = "good-upstream-code"
	installClient   = "install-client"
	otherUser       = "oidc:bob-2"
)

var bobClaims = map[string]any{claimSub: "bob-2", claimNameKey: "Bob"}

type testDecls map[string]accounts.Declaration

func (d testDecls) Account(_ context.Context, name string) (accounts.Declaration, bool, error) {
	v, ok := d[name]
	return v, ok, nil
}

func (d testDecls) All(context.Context) ([]accounts.Declaration, error) {
	out := make([]accounts.Declaration, 0, len(d))
	for _, v := range d {
		out = append(out, v)
	}
	return out, nil
}

func (d testDecls) Declared(context.Context, string) ([]accounts.Declaration, error) { return nil, nil }

// upstreamService is the outside service: an authorization address that is only inspected, a
// token endpoint and a revocation endpoint.
type upstreamService struct {
	srv         *httptest.Server
	mu          sync.Mutex
	revoked     []string
	revokeFails bool
	tokenFails  bool
	exchanged   url.Values
}

func newUpstream(t *testing.T) *upstreamService {
	u := &upstreamService{}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", u.token)
	mux.HandleFunc("/revoke", u.revoke)
	u.srv = httptest.NewTLSServer(mux)
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstreamService) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	u.mu.Lock()
	defer u.mu.Unlock()
	u.exchanged = r.PostForm
	if u.tokenFails || r.PostForm.Get("code") != connectUpCode || r.PostForm.Get("code_verifier") == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	_, _ = w.Write([]byte(`{"access_token":"` + upstreamAccess + `","refresh_token":"` + upstreamRefresh +
		`","expires_in":900,"refresh_token_expires_in":604800,"scope":"search offline_access"}`))
}

func (u *upstreamService) revoke(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	u.mu.Lock()
	defer u.mu.Unlock()
	u.revoked = append(u.revoked, r.PostForm.Get(formToken))
	if u.revokeFails {
		w.WriteHeader(http.StatusInternalServerError)
	}
}

type connectRig struct {
	*doorRig
	up    *upstreamService
	vault *accounts.Vault
	svc   *accounts.Service
	store *flakyBackend
}

// flakyBackend is a store that can be made to fail its deletions.
type flakyBackend struct {
	tokenstore.Backend
	failDeletes atomic.Bool
}

func (f *flakyBackend) Delete(ctx context.Context, group, name string) error {
	if f.failDeletes.Load() {
		return errors.New("store away")
	}
	return f.Backend.Delete(ctx, group, name)
}

func newConnectRig(t *testing.T) *connectRig {
	t.Helper()
	up := newUpstream(t)
	sealer, err := ParseKeys(testKey(t, "vault"))
	if err != nil {
		t.Fatal(err)
	}
	flaky := &flakyBackend{Backend: tokenstore.NewMemory(nil)}
	store, err := tokenstore.NewSealed(flaky, sealer, 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	regs := map[string]accounts.Registration{acctLibrary: {ClientID: installClient, ClientSecret: "install-secret"}}
	vault, err := accounts.NewVault(store, regs, nil)
	if err != nil {
		t.Fatal(err)
	}
	decls := testDecls{acctLibrary: {
		Name: acctLibrary, DisplayName: "Example Library", Purpose: "Search the library with your subscription.",
		Required: true, Scopes: []string{"search", "offline_access"}, Crews: []string{libraryCrew},
		AuthorizeURL: up.srv.URL + "/authorize", TokenURL: up.srv.URL + "/token", RevocationURL: up.srv.URL + "/revoke",
		TokenHosts: []string{"api.example.org"},
	}}
	svc := &accounts.Service{People: accounts.NewPrincipals([]byte("install-key")), Vault: vault, Decls: decls, Regs: regs, Client: accounts.NewClient(up.srv.Client()), Now: time.Now}
	r := newDoorRig(t, func(c *DoorConfig) { c.Accounts = svc })
	return &connectRig{doorRig: r, up: up, vault: vault, svc: svc, store: flaky}
}

// linkPath is the connect link the liaison would hand person, as a path.
func (r *connectRig) linkPath(user, account string) string {
	r.t.Helper()
	link, err := r.door.ConnectURL(r.svc.PrincipalID(user), account, libraryCrew)
	if err != nil {
		r.t.Fatal(err)
	}
	u, _ := url.Parse(link)
	return u.RequestURI()
}

// openLink is a browser opening a connect link, signing in at the issuer when it has no
// session, and arriving at the page behind it.
func (r *connectRig) openLink(b *browser, path string, claims map[string]any) *http.Response {
	r.t.Helper()
	resp := b.get(path)
	if resp.StatusCode != http.StatusFound {
		return resp
	}
	to := location(r.t, resp)
	if !strings.HasPrefix(to.String(), r.idp.srv.URL) {
		return resp
	}
	code := r.idp.approve(to.String(), claims)
	back := b.get(pathCallback + "?" + url.Values{paramCode: {code}, paramState: {to.Query().Get(paramState)}}.Encode())
	if back.StatusCode != http.StatusFound {
		return back
	}
	return b.get(location(r.t, back).RequestURI())
}

// press posts the consent page's form and returns the answer.
func (r *connectRig) press(b *browser, pageText string) *http.Response {
	r.t.Helper()
	form := fieldsOf(r.t, pageText)
	return b.do(http.MethodPost, pathConnect+acctLibrary, form, nil)
}

// serviceReturn is the service sending the person back with a code.
func (r *connectRig) serviceReturn(b *browser, toService *url.URL, extra url.Values) *http.Response {
	r.t.Helper()
	q := url.Values{paramCode: {connectUpCode}, paramState: {toService.Query().Get(paramState)}, paramIss: {r.up.srv.URL}}
	for k, v := range extra {
		q[k] = v
	}
	return b.get(pathCallback + "?" + q.Encode())
}

// connectAs runs the whole flow for user and returns the browser and the page the callback answered.
func (r *connectRig) connectAs(user string, claims map[string]any) (*browser, *http.Response) {
	r.t.Helper()
	b := newBrowser(r.t, r.srv.URL)
	page := r.openLink(b, r.linkPath(user, acctLibrary), claims)
	toService := location(r.t, r.press(b, body(r.t, page)))
	return b, r.serviceReturn(b, toService, nil)
}

var formStart = regexp.MustCompile(`<form[^>]*action="([^"]+)"`)

// fieldsOfForm reads the hidden fields of the form on a page whose action is action.
func fieldsOfForm(t *testing.T, page, action string) url.Values {
	t.Helper()
	for _, part := range strings.Split(page, "<form")[1:] {
		m := formStart.FindStringSubmatch("<form" + part)
		if m == nil || m[1] != action {
			continue
		}
		out := url.Values{}
		for _, f := range anyField.FindAllStringSubmatch(strings.SplitN(part, "</form>", 2)[0], -1) {
			out.Set(f[1], htmlUnescape(f[2]))
		}
		return out
	}
	t.Fatalf("no form with action %q in %q", action, page)
	return nil
}

var anyField = regexp.MustCompile(`name="([a-z]+)" value="([^"]+)"`)
