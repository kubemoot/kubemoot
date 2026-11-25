package signin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	idpClientID     = "door"
	idpClientSecret = "s3cret"
)

// idpGrant is what the fake issuer remembers of one authorization request it approved.
type idpGrant struct {
	nonce, challenge, redirect string
	claims                     map[string]any
	used                       bool
}

// fakeIDP is an OpenID Connect issuer: discovery, keys, and a token endpoint that holds codes
// single use, checks the client secret and the PKCE verifier, and signs ID tokens.
type fakeIDP struct {
	t   *testing.T
	srv *httptest.Server
	key *ecdsa.PrivateKey

	mu     sync.Mutex
	grants map[string]*idpGrant
	// audience and nonce, when set, override what the next ID tokens carry, to test refusals.
	audience, nonce string
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIDP{t: t, key: key, grants: map[string]*idpGrant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("GET /keys", f.keys)
	mux.HandleFunc("POST /token", f.token)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIDP) discovery(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		keyIssuer: f.srv.URL, keyAuthEndpoint: f.srv.URL + "/authorize", keyTokenEndpoint: f.srv.URL + "/token",
		"jwks_uri": f.srv.URL + "/keys", "id_token_signing_alg_values_supported": []string{"ES256"},
		"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
	})
}

func (f *fakeIDP) keys(w http.ResponseWriter, _ *http.Request) {
	jwk := jose.JSONWebKey{Key: &f.key.PublicKey, KeyID: "idp-1", Algorithm: "ES256", Use: "sig"}
	_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
}

// approve is the person signing in at the issuer: it takes the authorization URL the door sent the
// browser to and returns the code the issuer would send back, for a person with these claims.
func (f *fakeIDP) approve(authorizeURL string, claims map[string]any) string {
	f.t.Helper()
	u, err := url.Parse(authorizeURL)
	if err != nil || u.String()[:len(f.srv.URL)] != f.srv.URL {
		f.t.Fatalf("not sent to the issuer: %s", authorizeURL)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != methodS256 || q.Get("response_type") != responseCode || q.Get("client_id") != idpClientID {
		f.t.Fatalf("authorization request = %v", q)
	}
	code, _ := randomString(12)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grants[code] = &idpGrant{nonce: q.Get("nonce"), challenge: q.Get(paramCodeChallenge), redirect: q.Get(paramRedirect), claims: claims}
	return code
}

func (f *fakeIDP) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get(paramClientID), r.PostForm.Get("client_secret")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.grants[r.PostForm.Get(paramCode)]
	if id != idpClientID || secret != idpClientSecret || g == nil || g.used || g.redirect != r.PostForm.Get(paramRedirect) ||
		s256(r.PostForm.Get(paramCodeVerifier)) != g.challenge {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	g.used = true
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "idp-access", "token_type": "Bearer", "id_token": f.idToken(g)})
}

func (f *fakeIDP) idToken(g *idpGrant) string {
	aud, nonce := idpClientID, g.nonce
	if f.audience != "" {
		aud = f.audience
	}
	if f.nonce != "" {
		nonce = f.nonce
	}
	claims := map[string]any{
		"iss": f.srv.URL, "aud": aud, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": nonce,
	}
	for k, v := range g.claims {
		claims[k] = v
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: jose.JSONWebKey{Key: f.key, KeyID: "idp-1"}}, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	tok, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		f.t.Fatal(err)
	}
	return tok
}

// browser is one person's browser: it keeps the cookies the door sets and never follows redirects,
// so a test sees each answer.
type browser struct {
	t       *testing.T
	base    string
	cookies map[string]*http.Cookie
	last    []*http.Cookie
}

func newBrowser(t *testing.T, base string) *browser {
	return &browser{t: t, base: base, cookies: map[string]*http.Cookie{}}
}

func (b *browser) do(method, path string, form url.Values, headers map[string]string) *http.Response {
	b.t.Helper()
	var req *http.Request
	if form != nil {
		req, _ = http.NewRequest(method, b.base+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req, _ = http.NewRequest(method, b.base+path, nil)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, c := range b.cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	b.t.Cleanup(func() { _ = resp.Body.Close() })
	b.last = resp.Cookies()
	for _, c := range b.last {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
			continue
		}
		b.cookies[c.Name] = c
	}
	return resp
}

func (b *browser) get(path string) *http.Response { return b.do(http.MethodGet, path, nil, nil) }
