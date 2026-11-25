package signin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

var (
	formField  = regexp.MustCompile(`name="(req|csrf)" value="([^"]+)"`)
	janeClaims = map[string]any{claimSub: "jane-1", claimNameKey: "Jane Doe", defaultGroupsClaim: []any{"team-a", "system:masters"}}
)

// register registers a chat app by dynamic client registration and returns its client id.
func (r *doorRig) register(name string) string {
	r.t.Helper()
	body, _ := json.Marshal(map[string]any{keyRedirectURIs: []string{appRedirect}, paramClientName: name})
	resp, err := http.Post(r.srv.URL+pathRegister, "application/json", strings.NewReader(string(body)))
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		ClientID string `json:"client_id"`
	}
	decode(r.t, resp, &out)
	if resp.StatusCode != http.StatusCreated || out.ClientID == "" {
		r.t.Fatalf("register: %d", resp.StatusCode)
	}
	return out.ClientID
}

func (r *doorRig) authorizeQuery(clientID, challenge string, override url.Values) string {
	q := url.Values{
		paramResponseType: {responseCode}, paramClientID: {clientID}, paramRedirect: {appRedirect},
		paramCodeChallenge: {challenge}, paramChallengeMethod: {methodS256}, paramState: {appState}, paramResource: {r.cfg.Resource()},
	}
	for k, vs := range override {
		q[k] = vs
	}
	return pathAuthorize + "?" + q.Encode()
}

// signIn is the browser going to the door and being sent to the issuer, then the issuer sending the
// person back. It returns the answer to the callback: the consent page, or the app's redirect.
func (r *doorRig) signIn(b *browser, clientID, challenge string, claims map[string]any) *http.Response {
	r.t.Helper()
	to := location(r.t, b.get(r.authorizeQuery(clientID, challenge, nil)))
	code := r.idp.approve(to.String(), claims)
	return b.get(pathCallback + "?" + url.Values{paramCode: {code}, paramState: {to.Query().Get(paramState)}}.Encode())
}

// consentFields reads the hidden fields of the consent page.
func consentFields(t *testing.T, resp *http.Response) url.Values {
	t.Helper()
	return fieldsOf(t, body(t, resp))
}

// fieldsOf reads the hidden fields out of the text of a consent page.
func fieldsOf(t *testing.T, page string) url.Values {
	t.Helper()
	form := url.Values{}
	for _, m := range formField.FindAllStringSubmatch(page, -1) {
		form.Set(m[1], htmlUnescape(m[2]))
	}
	if form.Get("req") == "" || form.Get("csrf") == "" {
		t.Fatalf("no consent form in %q", page)
	}
	return form
}

func htmlUnescape(s string) string {
	return strings.NewReplacer("&#43;", "+", "&amp;", "&", "&#34;", `"`).Replace(s)
}

func (b *browser) postConsent(form url.Values, headers map[string]string) *http.Response {
	return b.do(http.MethodPost, pathConsent, form, headers)
}

// allow answers the consent page with Allow and returns the app's redirect.
func (r *doorRig) allow(b *browser, page *http.Response) *url.URL {
	r.t.Helper()
	form := consentFields(r.t, page)
	form.Set("decision", "allow")
	return location(r.t, b.postConsent(form, nil))
}

func TestFullSignInThroughTheIdentityProvider(t *testing.T) {
	r := newDoorRig(t, nil)
	b := newBrowser(t, r.srv.URL)
	clientID := r.register("Test App")
	verifier, challenge := appVerifier()

	page := r.signIn(b, clientID, challenge, janeClaims)
	if page.StatusCode != http.StatusOK || !strings.Contains(page.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("want the consent page, got %d", page.StatusCode)
	}
	back := r.allow(b, page)
	checkAppRedirect(t, back)
	resp := r.postToken(url.Values{
		paramGrantType: {grantAuthCode}, paramClientID: {clientID}, paramCode: {back.Query().Get(paramCode)},
		paramCodeVerifier: {verifier}, paramRedirect: {appRedirect}, paramResource: {r.cfg.Resource()},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token: %d %s", resp.StatusCode, body(t, resp))
	}
	var tok tokenResponse
	decode(t, resp, &tok)
	id, err := r.door.Verify(bearerRequest(tok.AccessToken))
	if err != nil || id.Subject != userJane {
		t.Fatalf("the person = %+v, %v", id, err)
	}
	// Every group carries the install's prefix, so the issuer cannot name system:masters.
	if len(id.Groups) != 2 || id.Groups[0] != groupTeamA || id.Groups[1] != "oidc:system:masters" {
		t.Errorf("groups = %v", id.Groups)
	}
}

func checkAppRedirect(t *testing.T, back *url.URL) {
	t.Helper()
	if back.Scheme+"://"+back.Host+back.Path != appRedirect {
		t.Errorf("redirected to %s", back)
	}
	if back.Query().Get(paramState) != appState || back.Query().Get("iss") != doorPublicURL {
		t.Errorf("state or issuer missing from %s", back)
	}
}

func TestTheLoginRequestToTheIssuerIsAFreshAuthorizationCodeFlow(t *testing.T) {
	r := newDoorRig(t, nil)
	b := newBrowser(t, r.srv.URL)
	_, challenge := appVerifier()
	to := location(t, b.get(r.authorizeQuery(r.register("Test App"), challenge, nil)))
	q := to.Query()
	for key, want := range map[string]string{
		"response_type": "code", paramClientID: idpClientID, "redirect_uri": doorPublicURL + pathCallback,
		"scope": "openid profile", "code_challenge_method": "S256",
	} {
		if q.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, q.Get(key), want)
		}
	}
	for _, key := range []string{"state", "nonce", paramCodeChallenge} {
		if q.Get(key) == "" {
			t.Errorf("no %s", key)
		}
	}
	if q.Get(paramCodeChallenge) == challenge || q.Get(paramResource) != "" {
		t.Error("the app's challenge or resource leaked to the issuer")
	}
	checkCookie(t, findCookie(b.last, stateCookiePrefix), "")
}

func findCookie(cookies []*http.Cookie, prefix string) *http.Cookie {
	for _, c := range cookies {
		if strings.HasPrefix(c.Name, prefix) {
			return c
		}
	}
	return nil
}

// checkCookie checks the attributes every cookie of the door carries.
func checkCookie(t *testing.T, c *http.Cookie, path string) {
	t.Helper()
	if c == nil {
		t.Fatal("no cookie was set")
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie %s = %+v", c.Name, c)
	}
	if path != "" && c.Path != path {
		t.Errorf("cookie %s path = %q", c.Name, c.Path)
	}
}

func TestConsentIsAskedOncePerApp(t *testing.T) {
	r := newDoorRig(t, nil)
	b := newBrowser(t, r.srv.URL)
	app := r.register("Test App")
	_, challenge := appVerifier()
	r.allow(b, r.signIn(b, app, challenge, janeClaims))

	// The same person, still signed in, asks again: straight back to the app with a code.
	again := location(t, b.get(r.authorizeQuery(app, challenge, nil)))
	if again.Query().Get(paramCode) == "" || again.Host != appHost {
		t.Fatalf("a second request was not answered with a code: %s", again)
	}
	// A different app needs its own consent.
	other := b.get(r.authorizeQuery(r.register("Another App"), challenge, nil))
	if other.StatusCode != http.StatusOK {
		t.Fatalf("a new app was not asked about: %d", other.StatusCode)
	}
	// A browser that has not seen the consent asks again even for the same app.
	fresh := newBrowser(t, r.srv.URL)
	if page := r.signIn(fresh, app, challenge, janeClaims); page.StatusCode != http.StatusOK {
		t.Fatalf("a new browser was not asked: %d", page.StatusCode)
	}
}

func TestOnePersonsConsentIsNotAnothers(t *testing.T) {
	r := newDoorRig(t, nil)
	b := newBrowser(t, r.srv.URL)
	app := r.register("Test App")
	_, challenge := appVerifier()
	r.allow(b, r.signIn(b, app, challenge, janeClaims))
	// The session ends and Joe signs in at the same browser: Jane's consent is not his.
	r.advance(sessionIdle + time.Minute)
	if page := r.signIn(b, app, challenge, map[string]any{claimSub: "joe-1"}); page.StatusCode != http.StatusOK {
		t.Fatalf("joe inherited jane's consent: %d", page.StatusCode)
	}
}

func TestSessionCookieAttributesAndLifetimes(t *testing.T) {
	r := newDoorRig(t, nil)
	b := newBrowser(t, r.srv.URL)
	app := r.register("Test App")
	_, challenge := appVerifier()
	r.signIn(b, app, challenge, janeClaims)
	cookie := findCookie(b.last, sessionCookie)
	checkCookie(t, cookie, cookiePath)
	if strings.Contains(cookie.Value, "jane") {
		t.Error("the session cookie is readable")
	}
	r.advance(sessionIdle - time.Minute) // used within the idle time: it slides
	if resp := b.get(r.authorizeQuery(app, challenge, nil)); resp.StatusCode != http.StatusOK {
		t.Fatalf("a live session was not recognised: %d", resp.StatusCode)
	}
	r.advance(sessionIdle + time.Minute) // idle too long: the issuer is asked again
	if to := location(t, b.get(r.authorizeQuery(app, challenge, nil))); !strings.HasPrefix(to.String(), r.idp.srv.URL) {
		t.Errorf("an idle session was kept: %s", to)
	}
}

func TestSessionEndsAtItsAbsoluteTimeEvenWhenUsedSteadily(t *testing.T) {
	r := newDoorRig(t, nil)
	b := newBrowser(t, r.srv.URL)
	app := r.register("Test App")
	_, challenge := appVerifier()
	r.signIn(b, app, challenge, janeClaims)
	for elapsed := time.Duration(0); elapsed < sessionAbsolute; elapsed += sessionIdle - time.Minute {
		r.advance(sessionIdle - time.Minute)
		b.get(r.authorizeQuery(app, challenge, nil))
	}
	r.advance(time.Minute)
	if to := location(t, b.get(r.authorizeQuery(app, challenge, nil))); !strings.HasPrefix(to.String(), r.idp.srv.URL) {
		t.Errorf("a session outlived its absolute time: %s", to)
	}
}

func TestConsentPageIsEscapedAndNotFrameable(t *testing.T) {
	r := newDoorRig(t, nil)
	b := newBrowser(t, r.srv.URL)
	app := r.register(`<script>alert(1)</script>`)
	_, challenge := appVerifier()
	page := r.signIn(b, app, challenge, map[string]any{claimSub: "x", claimNameKey: `"><img src=x onerror=alert(2)>`})
	text := body(t, page)
	if strings.Contains(text, "<script>alert") || strings.Contains(text, "<img src=x") {
		t.Errorf("the page does not escape what an app or a person supplied: %s", text)
	}
	h := page.Header
	for key, want := range map[string]string{
		"X-Frame-Options": "DENY", "Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff",
	} {
		if h.Get(key) != want {
			t.Errorf("%s = %q", key, h.Get(key))
		}
	}
	csp := h.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") || strings.Contains(csp, "unsafe") {
		t.Errorf("policy = %q", csp)
	}
}

func TestConsentRefusals(t *testing.T) {
	r := newDoorRig(t, nil)
	app := r.register("Test App")
	_, challenge := appVerifier()
	newPage := func() (*browser, url.Values) {
		b := newBrowser(t, r.srv.URL)
		form := consentFields(t, r.signIn(b, app, challenge, janeClaims))
		form.Set("decision", "allow")
		return b, form
	}
	cases := map[string]func(b *browser, form url.Values) *http.Response{
		"no token": func(b *browser, form url.Values) *http.Response {
			form.Del("csrf")
			return b.postConsent(form, nil)
		},
		"a token for another request": func(b *browser, form url.Values) *http.Response {
			other := newPageFor(t, r, app, challenge)
			form.Set("csrf", other.Get("csrf"))
			return b.postConsent(form, nil)
		},
		"another request with this token": func(b *browser, form url.Values) *http.Response {
			other := newPageFor(t, r, app, challenge)
			form.Set("req", other.Get("req"))
			return b.postConsent(form, nil)
		},
		"this person's token for another of their requests": func(b *browser, form url.Values) *http.Response {
			second := consentFields(t, b.get(r.authorizeQuery(app, challenge, nil)))
			form.Set("req", second.Get("req"))
			return b.postConsent(form, nil)
		},
		"a post from another site": func(b *browser, form url.Values) *http.Response {
			return b.postConsent(form, map[string]string{"Origin": "https://evil.example.test"})
		},
		"a browser that says cross-site": func(b *browser, form url.Values) *http.Response {
			return b.postConsent(form, map[string]string{"Sec-Fetch-Site": "cross-site"})
		},
		"no session": func(b *browser, form url.Values) *http.Response {
			return newBrowser(t, r.srv.URL).postConsent(form, nil)
		},
		"a stale form": func(b *browser, form url.Values) *http.Response {
			// The person stays signed in (each visit renews the session) but the form is older than a request may be.
			r.advance(6 * time.Minute)
			b.get(r.authorizeQuery(app, challenge, nil))
			r.advance(6 * time.Minute)
			return b.postConsent(form, nil)
		},
	}
	for name, attempt := range cases {
		t.Run(name, func(t *testing.T) {
			b, form := newPage()
			resp := attempt(b, form)
			if resp.StatusCode < 400 {
				t.Fatalf("answered %d, want a refusal", resp.StatusCode)
			}
			if loc := resp.Header.Get("Location"); loc != "" {
				t.Errorf("a refused post redirected to %s", loc)
			}
		})
	}
}

// newPageFor makes a second consent form for another person at another browser.
func newPageFor(t *testing.T, r *doorRig, app, challenge string) url.Values {
	t.Helper()
	b := newBrowser(t, r.srv.URL)
	return consentFields(t, r.signIn(b, app, challenge, map[string]any{claimSub: "other-person"}))
}

func TestDenyGoesBackToTheAppWithAccessDenied(t *testing.T) {
	r := newDoorRig(t, nil)
	b := newBrowser(t, r.srv.URL)
	_, challenge := appVerifier()
	form := consentFields(t, r.signIn(b, r.register("Test App"), challenge, janeClaims))
	form.Set("decision", "deny")
	back := location(t, b.postConsent(form, nil))
	if back.Query().Get("error") != errDenied || back.Query().Get(paramCode) != "" || back.Query().Get(paramState) != appState {
		t.Fatalf("redirect %s", back)
	}
}
