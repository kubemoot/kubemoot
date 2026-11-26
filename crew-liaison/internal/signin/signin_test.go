package signin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func tokenForm(clientID, code, verifier string) url.Values {
	return url.Values{
		paramGrantType: {grantAuthCode}, paramClientID: {clientID}, paramCode: {code},
		paramCodeVerifier: {verifier}, paramRedirect: {appRedirect},
	}
}

func TestFullFlow(t *testing.T) {
	r := newRig(t, nil)
	clientID := r.registerClient()
	verifier, challenge := appVerifier()

	back, up := r.signIn(clientID, challenge, nil)
	checkUpstreamAuthorize(t, r, up, challenge)
	if back.Scheme+"://"+back.Host+back.Path != appRedirect || back.Query().Get(paramState) != appState {
		t.Fatalf("app redirect %s", back)
	}

	resp := r.postToken(tokenForm(clientID, back.Query().Get(paramCode), verifier))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token: %d %s", resp.StatusCode, body(t, resp))
	}
	var tok map[string]any
	decode(t, resp, &tok)
	if tok["access_token"] != accessToken || tok[grantRefresh] != refreshToken || tok["expires_in"] != float64(900) {
		t.Errorf("the token was not relayed as received: %v", tok)
	}
	if resp.Header.Get("Cache-Control") != noStore {
		t.Error("token response must be no-store")
	}
	checkUpstreamTokenCall(t, r)
}

func checkUpstreamTokenCall(t *testing.T, r *rig) {
	t.Helper()
	sent := r.up.sent()
	if len(sent) != 1 || sent[0].Get(paramClientID) != upstreamClientID || sent[0].Get(paramRedirect) != r.srv.URL+pathCallback {
		t.Errorf("upstream request: %v", sent)
	}
}

// checkUpstreamAuthorize checks the request the person is sent to the upstream with.
func checkUpstreamAuthorize(t *testing.T, r *rig, up *url.URL, appChallenge string) {
	t.Helper()
	uq := up.Query()
	if got := up.Scheme + "://" + up.Host + up.Path; got != r.cfg.UpstreamAuthorizeURL {
		t.Errorf("sent to %s", got)
	}
	for k, want := range map[string]string{
		paramClientID: upstreamClientID, paramRedirect: r.srv.URL + pathCallback, paramResponseType: responseCode,
		paramChallengeMethod: methodS256, paramScopeKey: "openid offline_access",
	} {
		if uq.Get(k) != want {
			t.Errorf("upstream %s = %q, want %q", k, uq.Get(k), want)
		}
	}
	if uq.Get(paramCodeChallenge) == appChallenge {
		t.Error("the upstream got the app's challenge instead of a fresh one")
	}
}

func TestTwoReplicasShareNothing(t *testing.T) {
	r := newRig(t, nil)
	// A second front built from the same configuration finishes what the first started.
	second := r.replica(r.cfg)
	clientID := r.registerClient()
	verifier, challenge := appVerifier()
	back, _ := r.signIn(clientID, challenge, nil)

	req, _ := http.NewRequest(http.MethodPost, "http://x"+pathToken, strings.NewReader(tokenForm(clientID, back.Query().Get(paramCode), verifier).Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := newRecorder()
	second.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("the second replica refused: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPKCEAndClientChecks(t *testing.T) {
	r := newRig(t, nil)
	clientID := r.registerClient()
	otherClient := r.registerClient()
	verifier, challenge := appVerifier()

	cases := map[string]func(form url.Values){
		"wrong verifier":     func(f url.Values) { f.Set("code_verifier", verifier+"x") },
		"missing verifier":   func(f url.Values) { f.Del("code_verifier") },
		"wrong redirect":     func(f url.Values) { f.Set(paramRedirect, evilRedirect) },
		"different client":   func(f url.Values) { f.Set(paramClientID, otherClient) },
		"code from nowhere":  func(f url.Values) { f.Set(paramCode, "garbage") },
		"unknown grant type": func(f url.Values) { f.Set(paramGrantType, "password") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			back, _ := r.signIn(clientID, challenge, nil)
			form := tokenForm(clientID, back.Query().Get(paramCode), verifier)
			mutate(form)
			resp := r.postToken(form)
			if resp.StatusCode < 400 {
				t.Fatalf("accepted: %d", resp.StatusCode)
			}
		})
	}
	if n := len(r.up.sent()); n != 0 {
		t.Errorf("a refused request reached the upstream %d times", n)
	}
}

func TestAuthorizeRefusals(t *testing.T) {
	r := newRig(t, nil)
	clientID := r.registerClient()
	_, challenge := appVerifier()

	// An unusable client or redirect is shown, never redirected to.
	for name, q := range map[string]string{
		"unregistered redirect": r.authorizeQuery(clientID, challenge, url.Values{paramRedirect: {evilRedirect}}),
		"unknown client":        r.authorizeQuery("not-sealed", challenge, nil),
		"forged client":         r.authorizeQuery(clientID[:len(clientID)-2]+"AA", challenge, nil),
	} {
		resp := r.get(q)
		if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
			t.Errorf("%s: %d %q", name, resp.StatusCode, resp.Header.Get("Location"))
		}
	}

	// A request problem goes back to the app's own redirect with the app's state.
	for name, q := range map[string]string{
		"no challenge":    r.authorizeQuery(clientID, "", nil),
		"plain challenge": r.authorizeQuery(clientID, challenge, url.Values{paramChallengeMethod: {"plain"}}),
		"short challenge": r.authorizeQuery(clientID, "abc", nil),
		"token flow":      r.authorizeQuery(clientID, challenge, url.Values{paramResponseType: {formToken}}),
	} {
		loc := location(t, r.get(q))
		if loc.Host != appHost || loc.Query().Get("error") == "" || loc.Query().Get(paramState) != appState {
			t.Errorf("%s: %s", name, loc)
		}
	}
}

func TestOnlyRegisteredRedirectWithOneRegistered(t *testing.T) {
	r := newRig(t, nil)
	clientID := r.registerClient()
	_, challenge := appVerifier()
	q := url.Values{paramResponseType: {responseCode}, paramClientID: {clientID}, paramCodeChallenge: {challenge}, paramChallengeMethod: {methodS256}}
	loc := location(t, r.get(pathAuthorize+"?"+q.Encode()))
	if !strings.HasPrefix(loc.String(), r.cfg.UpstreamAuthorizeURL) {
		t.Errorf("a single registered redirect should be the default: %s", loc)
	}
}

func TestExpiredCodeAndState(t *testing.T) {
	r := newRig(t, nil)
	clientID := r.registerClient()
	verifier, challenge := appVerifier()

	back, _ := r.signIn(clientID, challenge, nil)
	r.now = r.now.Add(codeLifetime + time.Second)
	resp := r.postToken(tokenForm(clientID, back.Query().Get(paramCode), verifier))
	assertOAuthError(t, resp, http.StatusBadRequest, "invalid_grant")

	// An expired state is refused at the callback.
	r.now = time.Unix(1_800_000_000, 0)
	auth := r.get(r.authorizeQuery(clientID, challenge, nil))
	state := location(t, auth).Query().Get(paramState)
	r.now = r.now.Add(stateLifetime + time.Second)
	cb := r.get(pathCallback+"?"+url.Values{paramCode: {upstreamCode}, paramState: {state}}.Encode(), auth.Cookies()...)
	if cb.StatusCode != http.StatusBadRequest {
		t.Errorf("expired state: %d", cb.StatusCode)
	}
}

func TestReplayedCodeFailsAtTheUpstream(t *testing.T) {
	r := newRig(t, nil)
	clientID := r.registerClient()
	verifier, challenge := appVerifier()
	back, _ := r.signIn(clientID, challenge, nil)
	form := tokenForm(clientID, back.Query().Get(paramCode), verifier)

	if resp := r.postToken(form); resp.StatusCode != http.StatusOK {
		t.Fatalf("first use: %d", resp.StatusCode)
	}
	assertOAuthError(t, r.postToken(form), http.StatusBadRequest, "invalid_grant")
}

func TestTamperedBlobsAreRefused(t *testing.T) {
	r := newRig(t, nil)
	clientID := r.registerClient()
	verifier, challenge := appVerifier()
	back, up := r.signIn(clientID, challenge, nil)
	code, state := back.Query().Get(paramCode), up.Query().Get(paramState)

	if resp := r.postToken(tokenForm(clientID, flip(code), verifier)); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("tampered code: %d", resp.StatusCode)
	}
	if resp := r.postToken(tokenForm(flip(clientID), code, verifier)); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("tampered client id: %d", resp.StatusCode)
	}
	cb := r.get(pathCallback + "?" + url.Values{paramCode: {upstreamCode}, paramState: {flip(state)}}.Encode())
	if cb.StatusCode != http.StatusBadRequest {
		t.Errorf("tampered state: %d", cb.StatusCode)
	}
	// A blob is only good where it was sealed for: a client id is not a code.
	if resp := r.postToken(tokenForm(clientID, clientID, verifier)); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("client id used as a code: %d", resp.StatusCode)
	}
}

// flip changes one character in the middle of a blob.
func flip(s string) string {
	i := len(s) / 2
	c := byte('A')
	if s[i] == 'A' {
		c = 'B'
	}
	return s[:i] + string(c) + s[i+1:]
}

func TestCallbackNeedsTheSameBrowser(t *testing.T) {
	r := newRig(t, nil)
	clientID := r.registerClient()
	_, challenge := appVerifier()
	auth := r.get(r.authorizeQuery(clientID, challenge, nil))
	state := location(t, auth).Query().Get(paramState)
	target := pathCallback + "?" + url.Values{paramCode: {upstreamCode}, paramState: {state}}.Encode()

	if resp := r.get(target); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no cookie: %d", resp.StatusCode)
	}
	if resp := r.get(target, &http.Cookie{Name: stateCookieName(state), Value: "not-it"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("wrong cookie: %d", resp.StatusCode)
	}
	if resp := r.get(target, auth.Cookies()...); resp.StatusCode != http.StatusFound {
		t.Errorf("right cookie: %d", resp.StatusCode)
	}
}

func TestVerifierLengthAndRedirectRequirement(t *testing.T) {
	r := newRig(t, nil)
	clientID := r.registerClient()
	_, challenge := appVerifier()

	// An authorize request that sent redirect_uri needs it again at the token step.
	back, _ := r.signIn(clientID, challenge, nil)
	form := tokenForm(clientID, back.Query().Get(paramCode), "short")
	assertOAuthError(t, r.postToken(form), http.StatusBadRequest, "invalid_grant")
	verifier, _ := appVerifier()
	form = tokenForm(clientID, back.Query().Get(paramCode), verifier)
	form.Del(paramRedirect)
	assertOAuthError(t, r.postToken(form), http.StatusBadRequest, "invalid_grant")
}

func TestUpstreamErrorCodeIsConstrained(t *testing.T) {
	if safeErrorCode(errDenied) != errDenied || safeErrorCode("bad code\n<x>") != "server_error" {
		t.Error("error code filter")
	}
}

func TestUpstreamRedirectIsNotRelayed(t *testing.T) {
	redir := newRedirectingUpstream(t)
	r2 := newRig(t, func(c *Config) { c.UpstreamTokenURL = redir.URL })
	cid := r2.registerClient()
	assertOAuthError(t, r2.postToken(url.Values{paramGrantType: {grantRefresh}, paramClientID: {cid}, grantRefresh: {"x"}}),
		http.StatusBadGateway, "temporarily_unavailable")
}

func TestResourceIsNeverForwarded(t *testing.T) {
	r := newRig(t, nil)
	clientID := r.registerClient()
	verifier, challenge := appVerifier()
	res := url.Values{paramResource: {"https://liaison.example.test/mcp"}}

	back, up := r.signIn(clientID, challenge, res)
	if _, found := up.Query()[paramResource]; found || strings.Contains(up.String(), "liaison.example.test") {
		t.Errorf("resource reached the upstream authorize request: %s", up)
	}
	form := tokenForm(clientID, back.Query().Get(paramCode), verifier)
	form.Set(paramResource, "https://liaison.example.test/mcp")
	if resp := r.postToken(form); resp.StatusCode != http.StatusOK {
		t.Fatalf("token: %d", resp.StatusCode)
	}
	rf := url.Values{paramGrantType: {grantRefresh}, paramClientID: {clientID}, grantRefresh: {refreshToken}, paramResource: {"x"}}
	r.postToken(rf)
	for _, f := range r.up.sent() {
		if _, found := f[paramResource]; found {
			t.Errorf("resource reached the upstream token endpoint: %v", f)
		}
	}
}

func TestUpstreamErrorsPassThrough(t *testing.T) {
	r := newRig(t, nil)
	clientID := r.registerClient()
	_, challenge := appVerifier()
	auth := r.get(r.authorizeQuery(clientID, challenge, nil))
	state := location(t, auth).Query().Get(paramState)

	cb := r.get(pathCallback+"?"+url.Values{"error": {errDenied}, "error_description": {"no thanks"}, paramState: {state}}.Encode(), auth.Cookies()...)
	loc := location(t, cb)
	if loc.Host != appHost || loc.Query().Get("error") != errDenied ||
		loc.Query().Get("error_description") != "no thanks" || loc.Query().Get(paramState) != appState {
		t.Errorf("error redirect: %s", loc)
	}

	// An upstream token error is relayed with its status and body.
	back, _ := r.signIn(clientID, challenge, nil)
	resp := r.postToken(tokenForm(clientID, back.Query().Get(paramCode), "wrong-verifier-wrong-verifier-wrong-verifier-1234"))
	assertOAuthError(t, resp, http.StatusBadRequest, "invalid_grant")
}

func TestUpstreamBodyRelayedAsReceived(t *testing.T) {
	r := newRig(t, nil)
	clientID := r.registerClient()
	verifier, challenge := appVerifier()
	back, _ := r.signIn(clientID, challenge, nil)
	// Make the upstream fail after the code is sealed.
	r.up.mu.Lock()
	r.up.used[upstreamCode] = true
	r.up.mu.Unlock()
	resp := r.postToken(tokenForm(clientID, back.Query().Get(paramCode), verifier))
	var e map[string]string
	decode(t, resp, &e)
	if resp.StatusCode != http.StatusBadRequest || e["error_description"] != "upstream says no" {
		t.Errorf("%d %v", resp.StatusCode, e)
	}
}

func TestRefreshPassesThrough(t *testing.T) {
	r := newRig(t, nil)
	clientID := r.registerClient()
	resp := r.postToken(url.Values{paramGrantType: {grantRefresh}, paramClientID: {clientID}, grantRefresh: {refreshToken}})
	var tok map[string]any
	decode(t, resp, &tok)
	if resp.StatusCode != http.StatusOK || tok["access_token"] != "ACCESS-2" || tok[grantRefresh] != "REFRESH-2" {
		t.Errorf("refresh: %d %v", resp.StatusCode, tok)
	}
	sent := r.up.sent()
	if len(sent) != 1 || sent[0].Get(paramClientID) != upstreamClientID || sent[0].Get(grantRefresh) != refreshToken {
		t.Errorf("upstream got %v", sent)
	}
	assertOAuthError(t, r.postToken(url.Values{paramGrantType: {grantRefresh}, paramClientID: {clientID}, grantRefresh: {"stale"}}),
		http.StatusBadRequest, "invalid_grant")
	assertOAuthError(t, r.postToken(url.Values{paramGrantType: {grantRefresh}, paramClientID: {notPEM}, grantRefresh: {refreshToken}}),
		http.StatusUnauthorized, "invalid_client")
}

func TestUpstreamDownIsAGatewayError(t *testing.T) {
	r := newRig(t, nil)
	clientID := r.registerClient()
	r.up.srv.Close()
	resp := r.postToken(url.Values{paramGrantType: {grantRefresh}, paramClientID: {clientID}, grantRefresh: {"x"}})
	assertOAuthError(t, resp, http.StatusBadGateway, "temporarily_unavailable")
}

func TestMetadataDocuments(t *testing.T) {
	r := newRig(t, nil)
	var prm map[string]any
	resp := r.get(pathProtectedResource)
	decode(t, resp, &prm)
	if prm[paramResource] != r.srv.URL+"/mcp" || toStrings(prm["authorization_servers"])[0] != r.srv.URL {
		t.Errorf("protected resource metadata: %v", prm)
	}

	var as map[string]any
	decode(t, r.get(pathAuthServer), &as)
	for k, want := range map[string]string{
		keyIssuer: r.srv.URL, keyAuthEndpoint: r.srv.URL + pathAuthorize,
		keyTokenEndpoint: r.srv.URL + pathToken, "registration_endpoint": r.srv.URL + pathRegister,
	} {
		if as[k] != want {
			t.Errorf("%s = %v", k, as[k])
		}
	}
	if toStrings(as["code_challenge_methods_supported"])[0] != methodS256 || toStrings(as["token_endpoint_auth_methods_supported"])[0] != authMethodNone {
		t.Errorf("auth server metadata: %v", as)
	}
	if _, has := as["jwks_uri"]; has {
		t.Error("jwks_uri must be absent, nothing is signed")
	}
	if got := ResourceMetadataURL(r.srv.URL); got != r.srv.URL+pathProtectedResource {
		t.Errorf("resource metadata url %s", got)
	}
}

func toStrings(v any) []string {
	out := make([]string, 0, len(v.([]any)))
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestRegistration(t *testing.T) {
	r := newRig(t, func(c *Config) { c.AllowedRedirectHosts = []string{appHost} })
	post := func(v any) int {
		b, _ := json.Marshal(v)
		req, _ := http.NewRequest(http.MethodPost, r.srv.URL+pathRegister, strings.NewReader(string(b)))
		return r.do(req).StatusCode
	}
	redirects := func(u ...string) map[string]any { return map[string]any{keyRedirectURIs: u} }

	for name, uris := range map[string][]string{
		"https allowed host": {appCallback},
		"loopback http":      {"http://127.0.0.1:8123/cb"},
		"localhost http":     {"http://localhost/cb"},
		"mixed":              {appCallback, "http://[::1]:9/cb"},
	} {
		if code := post(redirects(uris...)); code != http.StatusCreated {
			t.Errorf("%s: %d", name, code)
		}
	}
	for name, uris := range map[string][]string{
		authMethodNone:       nil,
		"http elsewhere":     {"http://app.example.test/cb"},
		"host not allowed":   {"https://other.example.test/cb"},
		"custom scheme":      {"myapp://cb"},
		"javascript":         {"javascript:alert(1)"},
		"fragment":           {"https://app.example.test/cb#x"},
		"relative":           {"/cb"},
		"credentials":        {"https://user:pw@app.example.test/cb"},
		"one bad among good": {appCallback, "https://other.example.test/cb"},
		"too many":           {"https://app.example.test/1", "https://app.example.test/2", "https://app.example.test/3", "https://app.example.test/4", "https://app.example.test/5", "https://app.example.test/6", "https://app.example.test/7", "https://app.example.test/8", "https://app.example.test/9", "https://app.example.test/10", "https://app.example.test/11"},
	} {
		if code := post(redirects(uris...)); code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, code)
		}
	}
	if code := post("not an object"); code != http.StatusBadRequest {
		t.Errorf("bad body: %d", code)
	}
}

func TestRegistrationResponseShape(t *testing.T) {
	r := newRig(t, nil)
	body, _ := json.Marshal(map[string]any{keyRedirectURIs: []string{appRedirect}, paramClientName: "Test App"})
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+pathRegister, strings.NewReader(string(body)))
	var out map[string]any
	decode(t, r.do(req), &out)
	if out[paramClientID] == "" || out["client_secret"] != nil || out["token_endpoint_auth_method"] != authMethodNone || out["client_id_issued_at"] == nil {
		t.Errorf("response: %v", out)
	}
}

func TestNothingSecretIsLogged(t *testing.T) {
	r := newRig(t, nil)
	clientID := r.registerClient()
	verifier, challenge := appVerifier()
	back, up := r.signIn(clientID, challenge, nil)
	code := back.Query().Get(paramCode)
	r.postToken(tokenForm(clientID, code, verifier))
	r.postToken(url.Values{paramGrantType: {grantRefresh}, paramClientID: {clientID}, grantRefresh: {refreshToken}})
	r.postToken(tokenForm(clientID, code, "wrong-verifier-wrong-verifier-wrong-verifier-1234"))

	logs := r.logText()
	if logs == "" {
		t.Fatal("expected some events to be logged")
	}
	for name, secret := range map[string]string{
		"access token": accessToken, "refresh token": refreshToken, "upstream code": upstreamCode,
		"sealed code": code, "sealed state": up.Query().Get(paramState), "client id": clientID,
		"app verifier": verifier, "app challenge": challenge, "rotated access": "ACCESS-2", "rotated refresh": "REFRESH-2",
		"upstream challenge": up.Query().Get(paramCodeChallenge),
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("the %s appears in the logs", name)
		}
	}
}

func TestCookieAttributes(t *testing.T) {
	r := newRig(t, nil)
	clientID := r.registerClient()
	_, challenge := appVerifier()
	resp := r.get(r.authorizeQuery(clientID, challenge, nil))
	var c *http.Cookie
	for _, k := range resp.Cookies() {
		if strings.HasPrefix(k.Name, stateCookiePrefix) {
			c = k
		}
	}
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != pathCallback || c.MaxAge <= 0 {
		t.Fatalf("cookie: %+v", c)
	}
}

func newRedirectingUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.example.test/", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}
