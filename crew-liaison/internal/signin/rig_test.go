package signin

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const (
	appHost          = "app.example.test"
	errDenied        = "access_denied"
	appState         = "app-state"
	appCallback      = "https://app.example.test/cb"
	upstreamClientID = "front-upstream-client"
	appRedirect      = "https://app.example.test/callback"
	upstreamCode     = "UPSTREAM-CODE-1"
	accessToken      = "ACCESS-TOKEN-SECRET"
	refreshToken     = "REFRESH-TOKEN-SECRET"
	rotatedAccess    = "ACCESS-2"
	rotatedRefresh   = "REFRESH-2"
	personAnn        = "0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a"
	personBob        = "0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b"
	bobAccess        = "BOB-ACCESS-TOKEN"
	bobRefresh       = "BOB-REFRESH-TOKEN"
	codePrefix       = "UPSTREAM-CODE-"
	keyAccessToken   = "access_token"
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
// what it was sent and honours single-use codes and PKCE. It signs in whoever `issues`
// names (an access and a refresh token), and renews a refresh token it knows once.
type fakeUpstream struct {
	srv *httptest.Server

	mu        sync.Mutex
	forms     []url.Values
	challenge string
	used      map[string]bool
	issues    [2]string
	answer    string // the JSON tail of a sign-in answer after the tokens
	renewals  map[string][2]string
	codes     int
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	u := &fakeUpstream{
		used: map[string]bool{}, issues: [2]string{accessToken, refreshToken},
		answer:   `"expires_in":900,"scope":"openid offline_access","token_type":"Bearer"`,
		renewals: map[string][2]string{refreshToken: {rotatedAccess, rotatedRefresh}},
	}
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
		code := r.PostForm.Get(paramCode)
		if !strings.HasPrefix(code, codePrefix) || u.used[code] ||
			s256(r.PostForm.Get("code_verifier")) != u.challenge || r.PostForm.Get(paramClientID) != upstreamClientID {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"upstream says no"}`)
			return
		}
		u.used[code] = true
		_, _ = io.WriteString(w, `{"access_token":"`+u.issues[0]+`","refresh_token":"`+u.issues[1]+`",`+u.answer+`}`)
	case grantRefresh:
		next, ok := u.renewals[r.PostForm.Get(grantRefresh)]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		delete(u.renewals, r.PostForm.Get(grantRefresh))
		_, _ = io.WriteString(w, `{"access_token":"`+next[0]+`","refresh_token":"`+next[1]+`","expires_in":900,"token_type":"Bearer"}`)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

// signsIn makes the next sign-in at the upstream issue these tokens.
func (u *fakeUpstream) signsIn(access, refresh string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.issues = [2]string{access, refresh}
}

// nextCode is a fresh authorization code the upstream will accept once.
func (u *fakeUpstream) nextCode() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.codes++
	return codePrefix + strconv.Itoa(u.codes)
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
	store    *tokenstore.Memory

	peopleMu sync.Mutex
	people   map[string]string // upstream access token -> principal id
}

// identify stands in for the door's verifiers: it knows which person each upstream access
// token belongs to, and refuses a token it does not know.
func (r *rig) identify(_ *http.Request, token string) (identity.Caller, error) {
	r.peopleMu.Lock()
	defer r.peopleMu.Unlock()
	id, ok := r.people[token]
	if !ok {
		return identity.Caller{}, identity.ErrInvalid
	}
	return identity.Caller{
		Principal: bus.Principal{Kind: bus.PrincipalKindSubscriber, Issuer: "example", ID: id},
		Identity:  &identity.Identity{Issuer: "example", Subject: "account-of-" + id, Credential: identity.NewCredential(token)},
	}, nil
}

// knows sets (or with id "" removes) the person an upstream access token names.
func (r *rig) knows(token, id string) {
	r.peopleMu.Lock()
	defer r.peopleMu.Unlock()
	if id == "" {
		delete(r.people, token)
		return
	}
	r.people[token] = id
}

func newRig(t *testing.T, mutate func(*Config)) *rig {
	t.Helper()
	r := &rig{t: t, up: newFakeUpstream(t), now: time.Unix(1_800_000_000, 0), logs: &strings.Builder{}}
	r.people = map[string]string{accessToken: personAnn, rotatedAccess: personAnn, bobAccess: personBob}
	r.store = tokenstore.NewMemory(func() time.Time { return r.now })
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
		Identify:             r.identify,
		Store:                r.store,
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

// replica builds another front from cfg. It shares the token store with the others and
// nothing else.
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
	body, _ := json.Marshal(map[string]any{keyRedirectURIs: redirects, "client_name": "Test App"})
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
		"code_challenge": {challenge}, paramChallengeMethod: {methodS256}, paramState: {appState},
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
	r.up.challenge = upstreamAuth.Query().Get("code_challenge")
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
	back := r.get(pathCallback+"?"+url.Values{paramCode: {r.up.nextCode()}, paramState: {upstreamAuth.Query().Get(paramState)}}.Encode(), cookie)
	return location(r.t, back), upstreamAuth
}

// doorTokens is the token endpoint's answer.
type doorTokens struct {
	Access    string `json:"access_token"`
	Refresh   string `json:"refresh_token"`
	Type      string `json:"token_type"`
	ExpiresIn int64  `json:"expires_in"`
}

// signedIn runs a whole sign-in for a new app and returns its client id and door tokens.
func (r *rig) signedIn() (clientID string, tokens doorTokens) {
	r.t.Helper()
	clientID = r.registerClient()
	verifier, challenge := appVerifier()
	back, _ := r.signIn(clientID, challenge, nil)
	resp := r.postToken(tokenForm(clientID, back.Query().Get(paramCode), verifier))
	if resp.StatusCode != http.StatusOK {
		r.t.Fatalf("token: %d %s", resp.StatusCode, body(r.t, resp))
	}
	decode(r.t, resp, &tokens)
	return clientID, tokens
}

// resolve presents bearer at the door, as the liaison does for a request to /mcp.
func (r *rig) resolve(bearer string) (identity.Caller, string, error) {
	req := httptest.NewRequest(http.MethodPost, pathDoor, nil)
	return r.handlers[0].Resolve(req, bearer)
}

func (r *rig) refreshForm(clientID, refresh string) url.Values {
	return url.Values{paramGrantType: {grantRefresh}, paramClientID: {clientID}, grantRefresh: {refresh}}
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
