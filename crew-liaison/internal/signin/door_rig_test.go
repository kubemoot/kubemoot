package signin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
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
	doorPublicURL = "https://liaison.example.test"
	doorClient    = "client-1"
	doorUser      = "oidc:jane"
)

const notPEM = "nope"

func privateKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// doorRig is a door with a clock the test moves, served over HTTP. Nothing here sleeps.
type doorRig struct {
	t      *testing.T
	cfg    DoorConfig
	door   *door
	srv    *httptest.Server
	client *http.Client

	mu    sync.Mutex
	now   time.Time
	logMu sync.Mutex
	logs  strings.Builder
}

func newDoorRig(t *testing.T, mutate func(*DoorConfig)) *doorRig {
	t.Helper()
	r := &doorRig{t: t, now: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)}
	r.cfg = DoorConfig{
		PublicURL: doorPublicURL, SealKeys: testKey(t, "k1"), SigningKeys: privateKeyPEM(t),
		OIDC:          OIDCConfig{Issuer: "https://idp.example.test", ClientID: "door", ClientSecret: "s3cret", Scopes: defaultOIDCScopes, GroupsClaim: "groups", UserPrefix: "oidc:"},
		RefreshMaxAge: defaultRefreshMax, AllowedRedirectHosts: []string{appHost},
		Now: func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now },
		Logger: funcr.New(func(prefix, args string) {
			r.logMu.Lock()
			defer r.logMu.Unlock()
			r.logs.WriteString(prefix + " " + args + "\n")
		}, funcr.Options{Verbosity: 9}),
	}
	if mutate != nil {
		mutate(&r.cfg)
	}
	d, err := newDoor(&r.cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.door = d
	r.srv = httptest.NewServer(d.Handler())
	t.Cleanup(r.srv.Close)
	r.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return r
}

func (r *doorRig) advance(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = r.now.Add(d)
}

func (r *doorRig) clock() time.Time { return r.cfg.Now() }

func (r *doorRig) logText() string {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	return r.logs.String()
}

func (r *doorRig) postToken(form url.Values) *http.Response {
	r.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+pathToken, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := r.client.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// code is an authorization code the door would have issued to the app for jane.
func (r *doorRig) code(challenge string) string {
	r.t.Helper()
	id, _ := randomString(16)
	blob, err := r.door.sealer.Seal(purposeDoorCode, doorCode{
		Who: person{User: doorUser, Groups: []string{"oidc:team-a"}}, ClientID: doorClient, Redirect: appRedirect, RedirectSent: true,
		Challenge: challenge, Resource: r.cfg.Resource(), ID: id, Expires: r.clock().Add(codeLifetime).Unix(),
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return blob
}

func (r *doorRig) codeForm(code, verifier string) url.Values {
	return url.Values{
		paramGrantType: {grantAuthCode}, paramClientID: {doorClient}, paramCode: {code},
		paramCodeVerifier: {verifier}, paramRedirect: {appRedirect}, paramResource: {r.cfg.Resource()},
	}
}

// tokens exchanges a fresh code and returns the pair the app would hold.
func (r *doorRig) tokens() (access, refresh string) {
	r.t.Helper()
	verifier, challenge := appVerifier()
	resp := r.postToken(r.codeForm(r.code(challenge), verifier))
	if resp.StatusCode != http.StatusOK {
		r.t.Fatalf("token: %d %s", resp.StatusCode, body(r.t, resp))
	}
	var tok tokenResponse
	decode(r.t, resp, &tok)
	return tok.AccessToken, tok.RefreshToken
}

func (r *doorRig) refreshForm(refresh string) url.Values {
	return url.Values{paramGrantType: {grantRefresh}, grantRefresh: {refresh}, paramClientID: {doorClient}}
}

func bearerRequest(token string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}
