package liaison

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/signin"
)

const keyCode = "code"

// signedInRig is the liaison's whole handler with the real sign-in front, in front of a
// fake crew gateway, with a fake outside service that signs one person in and vouches for
// their token.
type signedInRig struct {
	t        *testing.T
	liaison  *httptest.Server
	gateway  *fakeGateway
	personal string // the token the outside service issues for the person
}

func newSignedInRig(t *testing.T) *signedInRig {
	t.Helper()
	rig := &signedInRig{t: t, gateway: newFakeGateway(synthesisEvents()...)}
	t.Cleanup(rig.gateway.srv.Close)
	rig.personal = personToken("acct-ann", time.Now().Add(time.Hour), "")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"service-refresh","expires_in":3600}`, rig.personal)
	}))
	t.Cleanup(upstream.Close)

	var handler http.Handler
	rig.liaison = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(rig.liaison.Close)
	auth := personAuth(t, newPersonService(t), false)
	front, err := signin.New(&signin.Config{
		PublicURL:            rig.liaison.URL,
		UpstreamAuthorizeURL: upstream.URL + "/authorize",
		UpstreamTokenURL:     upstream.URL + "/token",
		UpstreamClientID:     "liaison-at-the-service",
		SealKeys:             "k1:" + base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")),
		Identify:             PersonFrom(auth),
		Logger:               logr.Discard(),
	})
	if err != nil {
		t.Fatal(err)
	}
	handler = NewHandlerWithFront(newTestService(t, rig.gateway, 4), "", auth, &SignInFront{
		Handler: front.Handler(), ResourceMetadataURL: signin.ResourceMetadataURL(rig.liaison.URL), Tokens: front.Resolve,
	})
	return rig
}

// do sends req without following redirects.
func (rig *signedInRig) do(req *http.Request) *http.Response {
	rig.t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		rig.t.Fatal(err)
	}
	rig.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// signIn walks the OAuth flow a chat app and a person's browser walk, and returns the
// tokens the app ends up holding.
func (rig *signedInRig) signIn() (access, refresh string) {
	rig.t.Helper()
	const redirect, verifier = "http://127.0.0.1:7777/cb", "a-verifier-of-the-app-0123456789-0123456789-abc"
	base := rig.liaison.URL
	reg, _ := http.NewRequest(http.MethodPost, base+"/oauth/register", strings.NewReader(`{"redirect_uris":["`+redirect+`"]}`))
	var client struct {
		ID string `json:"client_id"`
	}
	rig.decode(rig.do(reg), &client)

	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type": {keyCode}, "client_id": {client.ID}, "redirect_uri": {redirect}, "resource": {base + "/mcp"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
	}
	authorize, _ := http.NewRequest(http.MethodGet, base+"/oauth/authorize?"+q.Encode(), nil)
	started := rig.do(authorize)
	toService, err := url.Parse(started.Header.Get("Location"))
	if err != nil || started.StatusCode != http.StatusFound {
		rig.t.Fatalf("authorize: %d %v", started.StatusCode, err)
	}
	back, _ := http.NewRequest(http.MethodGet, base+"/oauth/callback?"+url.Values{
		keyCode: {"service-code"}, "state": {toService.Query().Get("state")}}.Encode(), nil)
	for _, c := range started.Cookies() {
		back.AddCookie(c)
	}
	toApp, err := url.Parse(rig.do(back).Header.Get("Location"))
	if err != nil || toApp.Query().Get(keyCode) == "" {
		rig.t.Fatalf("callback: %v %v", toApp, err)
	}
	form := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {client.ID}, keyCode: {toApp.Query().Get(keyCode)},
		"code_verifier": {verifier}, "redirect_uri": {redirect}, "resource": {base + "/mcp"},
	}
	exchange, _ := http.NewRequest(http.MethodPost, base+"/oauth/token", strings.NewReader(form.Encode()))
	exchange.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var tokens struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	rig.decode(rig.do(exchange), &tokens)
	return tokens.Access, tokens.Refresh
}

func (rig *signedInRig) decode(resp *http.Response, v any) {
	rig.t.Helper()
	if resp.StatusCode >= 300 {
		rig.t.Fatalf("status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		rig.t.Fatal(err)
	}
}

// The chat app holds the door's token and nothing else, and the crew's gateway is shown the
// service's token the liaison keeps for the person.
func TestAfterSignInTheAppHoldsADoorTokenAndTheServicesTokenStaysInTheLiaison(t *testing.T) {
	rig := newSignedInRig(t)
	access, refresh := rig.signIn()
	if access == "" || refresh == "" || strings.Contains(access+refresh, rig.personal) {
		t.Fatal("the app does not hold a door token of the liaison's own")
	}

	ctx := context.Background()
	session := connectSession(ctx, t, rig.liaison.URL+"/mcp", access)
	out := callTool(ctx, t, session, "ask", map[string]any{argCrew: testCrew, argQuestion: testQuestion, argWaitSeconds: 5})
	if !strings.Contains(out, "answered") {
		t.Fatalf("ask with a door token: %s", out)
	}
	sent := rig.gateway.authorizations()
	if len(sent) == 0 {
		t.Fatal("the gateway was not asked")
	}
	for _, got := range sent {
		if got != "Bearer "+rig.personal {
			t.Errorf("the gateway was shown %q", got)
		}
	}
}

// Nothing but the door's access token opens the door: not the service's own token, which
// the service itself accepts, and not the door's other tokens.
func TestAfterSignInOnlyTheDoorsAccessTokenOpensTheDoor(t *testing.T) {
	rig := newSignedInRig(t)
	access, refresh := rig.signIn()
	for name, bearer := range map[string]string{
		"the service's own token": rig.personal, "the door's refresh token": refresh, "a changed door token": access + "x",
	} {
		if code, challenge := postMCP(t, rig.liaison.URL, bearer); code != http.StatusUnauthorized ||
			!strings.Contains(challenge, "resource_metadata=") {
			t.Errorf("%s: %d %q", name, code, challenge)
		}
	}
	if n := len(rig.gateway.authorizations()); n != 0 {
		t.Errorf("a refused token reached a gateway %d times", n)
	}
}
