package signin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestAuthorizeRefusals separates what is shown to the browser (the redirect is not trusted yet)
// from what goes back to the app.
func TestDoorAuthorizeRefusals(t *testing.T) {
	r := newDoorRig(t, nil)
	app := r.register("Test App")
	_, challenge := appVerifier()
	browserErrors := map[string]url.Values{
		"an unknown client":        {paramClientID: {"nobody"}},
		"an unregistered redirect": {paramRedirect: {"https://app.example.test/elsewhere"}},
	}
	for name, override := range browserErrors {
		resp := newBrowser(t, r.srv.URL).get(r.authorizeQuery(app, challenge, override))
		if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
			t.Errorf("%s: %d, location %q", name, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	appErrors := map[string]struct {
		override url.Values
		code     string
	}{
		"another response type":    {url.Values{paramResponseType: {"token"}}, "unsupported_response_type"},
		"no challenge":             {url.Values{paramCodeChallenge: {""}}, "invalid_request"},
		"a plain challenge method": {url.Values{paramChallengeMethod: {"plain"}}, "invalid_request"},
		"no resource":              {url.Values{paramResource: {""}}, "invalid_target"},
		"another resource":         {url.Values{paramResource: {"https://api.example.test/mcp"}}, "invalid_target"},
	}
	for name, c := range appErrors {
		back := location(t, newBrowser(t, r.srv.URL).get(r.authorizeQuery(app, challenge, c.override)))
		q := back.Query()
		if q.Get("error") != c.code || q.Get(paramState) != appState || q.Get("iss") != doorPublicURL || back.Host != appHost {
			t.Errorf("%s: redirect %s", name, back)
		}
	}
}

// callbackRig starts sign-ins and answers them as the issuer would, for the callback tests.
type callbackRig struct {
	t              *testing.T
	r              *doorRig
	app, challenge string
}

func newCallbackRig(t *testing.T) *callbackRig {
	r := newDoorRig(t, nil)
	_, challenge := appVerifier()
	return &callbackRig{t: t, r: r, app: r.register("Test App"), challenge: challenge}
}

func (c *callbackRig) start() (*browser, *url.URL) {
	b := newBrowser(c.t, c.r.srv.URL)
	return b, location(c.t, b.get(c.r.authorizeQuery(c.app, c.challenge, nil)))
}

func (c *callbackRig) callback(b *browser, to *url.URL, extra url.Values) *http.Response {
	q := url.Values{paramState: {to.Query().Get(paramState)}, paramCode: {c.r.idp.approve(to.String(), janeClaims)}}
	for k, v := range extra {
		q[k] = v
	}
	return b.get(pathCallback + "?" + q.Encode())
}

func TestDoorCallbackRefusesStatesThatAreNotValid(t *testing.T) {
	c := newCallbackRig(t)
	b, to := c.start()
	if resp := b.get(pathCallback + "?state=garbage&code=" + c.r.idp.approve(to.String(), janeClaims)); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a state that is not ours: %d", resp.StatusCode)
	}
	_, to = c.start()
	resp := c.callback(newBrowser(t, c.r.srv.URL), to, nil)
	if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
		t.Errorf("a login finished in a browser that did not start it: %d", resp.StatusCode)
	}
	b, to = c.start()
	c.r.advance(stateLifetime + time.Minute)
	if resp := c.callback(b, to, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("an expired sign-in: %d", resp.StatusCode)
	}
}

func TestDoorCallbackClearsTheBindingCookieSoAStateIsNotReused(t *testing.T) {
	c := newCallbackRig(t)
	b, to := c.start()
	c.callback(b, to, nil)
	// The binding cookie was cleared by the first use.
	if resp := c.callback(b, to, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a state was used twice: %d", resp.StatusCode)
	}
}

func TestDoorCallbackPassesAnIssuersRefusalBackToTheApp(t *testing.T) {
	c := newCallbackRig(t)
	b, to := c.start()
	back := location(t, b.get(pathCallback+"?"+url.Values{paramState: {to.Query().Get(paramState)}, paramError: {errDenied}}.Encode()))
	if back.Query().Get(paramError) != errDenied || back.Host != appHost || back.Query().Get(paramCode) != "" {
		t.Errorf("redirect %s", back)
	}
	b, to = c.start()
	back = location(t, c.callback(b, to, url.Values{"iss": {"https://evil.example.test"}}))
	if back.Query().Get(paramError) != errDenied || back.Query().Get(paramCode) != "" {
		t.Errorf("another issuer answered: %s", back)
	}
}

// TestIDTokenRefusals: a token the issuer signed for another client, or another sign-in, or with no
// subject, never makes a session.
func TestIDTokenRefusals(t *testing.T) {
	_, challenge := appVerifier()
	cases := map[string]func(r *doorRig) map[string]any{
		"another audience": func(r *doorRig) map[string]any { r.idp.audience = "some-other-client"; return janeClaims },
		"another nonce":    func(r *doorRig) map[string]any { r.idp.nonce = "replayed-nonce"; return janeClaims },
		"no subject":       func(*doorRig) map[string]any { return map[string]any{claimNameKey: "Nobody"} },
		"a system user": func(r *doorRig) map[string]any {
			r.cfg.OIDC.UserPrefix = ""
			r.door.idp = newOIDCProvider(&r.cfg)
			return map[string]any{claimSub: "system:admin"}
		},
		"an expired token": func(*doorRig) map[string]any {
			return map[string]any{claimSub: "x", "exp": time.Now().Add(-time.Hour).Unix()}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			r := newDoorRig(t, nil)
			b := newBrowser(t, r.srv.URL)
			claims := setup(r)
			resp := r.signIn(b, r.register("Test App"), challenge, claims)
			back := location(t, resp)
			if back.Query().Get("error") != errDenied || back.Query().Get(paramCode) != "" {
				t.Fatalf("redirect %s", back)
			}
			for _, c := range b.last {
				if c.Name == sessionCookie && c.MaxAge >= 0 {
					t.Error("a session was made from a token that is not valid")
				}
			}
			if strings.Contains(r.logText(), "idp-access") {
				t.Error("the issuer's token reached the log")
			}
		})
	}
}

func TestSignInWhenTheIssuerIsDownIsAnErrorToTheAppNotACrash(t *testing.T) {
	r := newDoorRig(t, nil)
	r.idp.srv.Close()
	_, challenge := appVerifier()
	back := location(t, newBrowser(t, r.srv.URL).get(r.authorizeQuery(r.register("Test App"), challenge, nil)))
	if back.Query().Get("error") != "temporarily_unavailable" || back.Host != appHost {
		t.Errorf("redirect %s", back)
	}
}

func TestPersonFromReadsTheClaims(t *testing.T) {
	cfg := OIDCConfig{UserPrefix: defaultUserPrefix, GroupsClaim: defaultGroupsClaim}
	who, err := personFrom(cfg, map[string]any{
		claimSub: "u1", "preferred_username": "jane", defaultGroupsClaim: []any{"a", "", 7, strings.Repeat("g", 300)},
	})
	if err != nil || who.User != "oidc:u1" || who.Name != "jane" || len(who.Groups) != 1 || who.Groups[0] != "oidc:a" {
		t.Errorf("person = %+v, %v", who, err)
	}
	if who, _ := personFrom(cfg, map[string]any{claimSub: "u", defaultGroupsClaim: "solo"}); len(who.Groups) != 1 || who.Groups[0] != "oidc:solo" {
		t.Errorf("a single group claim: %+v", who)
	}
	if _, err := personFrom(cfg, map[string]any{claimSub: 42}); err == nil {
		t.Error("a numeric subject was accepted")
	}
}

func TestPersonFromBoundsTheGroups(t *testing.T) {
	cfg := OIDCConfig{UserPrefix: defaultUserPrefix, GroupsClaim: defaultGroupsClaim}
	many := make([]any, 200)
	for i := range many {
		many[i] = "g"
	}
	if who, _ := personFrom(cfg, map[string]any{claimSub: "u", defaultGroupsClaim: many}); len(who.Groups) != maxClaimGroups {
		t.Errorf("%d groups kept", len(who.Groups))
	}
}

func TestPersonFromNeverNamesAKubernetesSystemIdentity(t *testing.T) {
	bare := OIDCConfig{GroupsClaim: defaultGroupsClaim}
	who, _ := personFrom(bare, map[string]any{claimSub: "u", defaultGroupsClaim: []any{"system:masters", "ok"}})
	if len(who.Groups) != 1 || who.Groups[0] != "ok" {
		t.Errorf("a system group survived with no prefix: %v", who.Groups)
	}
	if _, err := personFrom(bare, map[string]any{claimSub: "system:kube-admin"}); err == nil {
		t.Error("a system user was accepted")
	}
}
