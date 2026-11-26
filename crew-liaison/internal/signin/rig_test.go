package signin

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
)

const (
	appHost          = "app.example.test"
	errDenied        = errAccessDenied
	appState         = "app-state"
	appCallback      = "https://app.example.test/cb"
	upstreamClientID = "front-upstream-client"
	appRedirect      = "https://app.example.test/callback"
	upstreamCode     = "UPSTREAM-CODE-1"
	accessToken      = "ACCESS-TOKEN-SECRET"
	refreshToken     = "REFRESH-TOKEN-SECRET"
)

func testKey(t *testing.T, id string) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return id + ":" + base64.StdEncoding.EncodeToString(b)
}

// fakeUpstream stands in for the upstream sign-in service: a token endpoint that records
// what it was sent and honours single-use codes and PKCE.
type fakeUpstream struct {
	srv *httptest.Server

	mu        sync.Mutex
	forms     []url.Values
	challenge string
	used      map[string]bool
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	u := &fakeUpstream{used: map[string]bool{}}
	u.srv = httptest.NewServer(http.HandlerFunc(u.token))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *fakeUpstream) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	u.mu.Lock()
	defer u.mu.Unlock()
	u.forms = append(u.forms, r.PostForm)
	w.Header().Set("Content-Type", "application/json")
	switch r.PostForm.Get(paramGrantType) {
	case grantAuthCode:
		if r.PostForm.Get(paramCode) != upstreamCode || u.used[upstreamCode] ||
			s256(r.PostForm.Get("code_verifier")) != u.challenge || r.PostForm.Get(paramClientID) != upstreamClientID {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"upstream says no"}`)
			return
		}
		u.used[upstreamCode] = true
		_, _ = io.WriteString(w, `{"access_token":"`+accessToken+`","refresh_token":"`+refreshToken+`","expires_in":900,"scope":"openid offline_access","token_type":"Bearer"}`)
	case grantRefresh:
		if r.PostForm.Get(grantRefresh) != refreshToken {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"ACCESS-2","refresh_token":"REFRESH-2","expires_in":900,"token_type":"Bearer"}`)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func (u *fakeUpstream) sent() []url.Values {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]url.Values(nil), u.forms...)
}

// rig is a front on a test server, a fake upstream, a movable clock and a log capture.
type rig struct {
	t        *testing.T
	srv      *httptest.Server
	up       *fakeUpstream
	cfg      Config
	now      time.Time
	logs     *strings.Builder
	logMu    sync.Mutex
	client   *http.Client
	handlers []SignIn
}

func newRig(t *testing.T, mutate func(*Config)) *rig {
	t.Helper()
	r := &rig{t: t, up: newFakeUpstream(t), now: time.Unix(1_800_000_000, 0), logs: &strings.Builder{}}
	var current http.Handler
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { current.ServeHTTP(w, req) }))
	t.Cleanup(r.srv.Close)
	r.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r.cfg = Config{
		PublicURL:            r.srv.URL,
		UpstreamAuthorizeURL: r.up.srv.URL + "/authorize",
		UpstreamTokenURL:     r.up.srv.URL + "/token",
		UpstreamClientID:     upstreamClientID,
		SealKeys:             testKey(t, "k1"),
		Now:                  func() time.Time { return r.now },
		Logger: funcr.New(func(prefix, args string) {
			r.logMu.Lock()
			defer r.logMu.Unlock()
			r.logs.WriteString(prefix + " " + args + "\n")
		}, funcr.Options{Verbosity: 9}),
	}
	if mutate != nil {
		mutate(&r.cfg)
	}
	current = r.replica(r.cfg)
	return r
}

// replica builds another front from cfg that shares nothing with the others.
func (r *rig) replica(cfg Config) http.Handler {
	f, err := New(&cfg)
	if err != nil {
		r.t.Fatal(err)
	}
	r.handlers = append(r.handlers, f)
	return f.Handler()
}

func (r *rig) logText() string {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	return r.logs.String()
}

func (r *rig) do(req *http.Request) *http.Response {
	r.t.Helper()
	resp, err := r.client.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (r *rig) get(path string, cookies ...*http.Cookie) *http.Response {
	r.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, r.srv.URL+path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return r.do(req)
}

func (r *rig) postToken(form url.Values) *http.Response {
	r.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+pathToken, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r.do(req)
}

func (r *rig) registerClient() string {
	r.t.Helper()
	redirects := []string{appRedirect}
	body, _ := json.Marshal(map[string]any{keyRedirectURIs: redirects, paramClientName: "Test App"})
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+pathRegister, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp := r.do(req)
	if resp.StatusCode != http.StatusCreated {
		r.t.Fatalf("register: %d", resp.StatusCode)
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	decode(r.t, resp, &out)
	return out.ClientID
}

func decode(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func location(t *testing.T, resp *http.Response) *url.URL {
	t.Helper()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("want a redirect, got %d: %s", resp.StatusCode, body(t, resp))
	}
	u, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// appVerifier is the PKCE pair an app uses.
func appVerifier() (verifier, challenge string) {
	verifier = "app-verifier-0123456789-0123456789-0123456789-abc"
	return verifier, s256(verifier)
}

func (r *rig) authorizeQuery(clientID, challenge string, extra url.Values) string {
	q := url.Values{
		paramResponseType: {responseCode}, paramClientID: {clientID}, paramRedirect: {appRedirect},
		paramCodeChallenge: {challenge}, paramChallengeMethod: {methodS256}, paramState: {appState},
	}
	for k, vs := range extra {
		q[k] = vs
	}
	return pathAuthorize + "?" + q.Encode()
}

// signIn runs authorize and the upstream's return, giving back the app's redirect.
func (r *rig) signIn(clientID, challenge string, extra url.Values) (appRedirectURL *url.URL, upstreamAuth *url.URL) {
	r.t.Helper()
	resp := r.get(r.authorizeQuery(clientID, challenge, extra))
	upstreamAuth = location(r.t, resp)
	r.up.mu.Lock()
	r.up.challenge = upstreamAuth.Query().Get(paramCodeChallenge)
	r.up.mu.Unlock()
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if strings.HasPrefix(c.Name, stateCookiePrefix) {
			cookie = c
		}
	}
	if cookie == nil {
		r.t.Fatal("no binding cookie")
	}
	back := r.get(pathCallback+"?"+url.Values{paramCode: {upstreamCode}, paramState: {upstreamAuth.Query().Get(paramState)}}.Encode(), cookie)
	return location(r.t, back), upstreamAuth
}

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

func assertOAuthError(t *testing.T, resp *http.Response, status int, code string) {
	t.Helper()
	var e oauthError
	decode(t, resp, &e)
	if resp.StatusCode != status || e.Code != code {
		t.Errorf("want %d %s, got %d %s", status, code, resp.StatusCode, e.Code)
	}
}
