package signin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"slices"
)

// loginState is sealed into the upstream `state`, so the callback needs no store.
type loginState struct {
	Redirect     string `json:"r"`
	AppState     string `json:"s"`
	Challenge    string `json:"c"`
	ClientID     string `json:"i"`
	Verifier     string `json:"v"`
	RedirectSent bool   `json:"p,omitempty"`
	Expires      int64  `json:"e"`
}

const stateCookiePrefix = "kubemoot_signin_"

// stateCookieName is per sign-in, so two sign-ins in one browser do not overwrite each other.
func stateCookieName(state string) string { return stateCookiePrefix + stateBinding(state)[:12] }

// authorizeRequest is a validated request from the app.
type authorizeRequest struct {
	clientID, redirect, state, challenge string
	redirectSent                         bool
}

// authorize starts a sign-in. A problem with the client or redirect is shown to the
// browser (never redirected, the redirect is not trusted yet); anything after is returned to
// the app's redirect. The request's `resource` is not read and so never reaches the upstream.
func (f *front) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req, ok := f.checkClient(w, q)
	if !ok {
		return
	}
	req.state = q.Get(paramState)
	if q.Get(paramResponseType) != responseCode {
		f.refuseAuthorize(w, r, req, "unsupported_response_type", "only response_type=code is supported")
		return
	}
	if q.Get(paramChallengeMethod) != methodS256 || !validChallenge(q.Get("code_challenge")) {
		f.refuseAuthorize(w, r, req, "invalid_request", "a PKCE code_challenge with method S256 is required")
		return
	}
	req.challenge = q.Get("code_challenge")
	f.startUpstream(w, r, req)
}

// checkClient unseals the client id and resolves the redirect, answering the browser itself
// when either is unusable.
func (f *front) checkClient(w http.ResponseWriter, q url.Values) (authorizeRequest, bool) {
	var reg registration
	clientID := q.Get(paramClientID)
	if f.sealer.Open(purposeClient, clientID, &reg) != nil {
		http.Error(w, "unknown client", http.StatusBadRequest)
		return authorizeRequest{}, false
	}
	redirect := q.Get(paramRedirect)
	if redirect == "" && len(reg.RedirectURIs) == 1 {
		redirect = reg.RedirectURIs[0]
	}
	if !slices.Contains(reg.RedirectURIs, redirect) || !f.redirectAllowed(redirect) {
		http.Error(w, "redirect_uri is not registered for this client", http.StatusBadRequest)
		return authorizeRequest{}, false
	}
	return authorizeRequest{clientID: clientID, redirect: redirect, redirectSent: q.Get(paramRedirect) != ""}, true
}

func (f *front) refuseAuthorize(w http.ResponseWriter, r *http.Request, req authorizeRequest, code, desc string) {
	p := url.Values{"error": {code}, "error_description": {desc}}
	if req.state != "" {
		p.Set(paramState, req.state)
	}
	redirectWith(w, r, req.redirect, p)
}

// startUpstream sends the person to the upstream sign-in with the front's own client id,
// its own callback, a fresh PKCE pair and no resource.
func (f *front) startUpstream(w http.ResponseWriter, r *http.Request, req authorizeRequest) {
	verifier, err := randomString(32)
	if err != nil {
		f.refuseAuthorize(w, r, req, "server_error", "")
		return
	}
	sealed, err := f.sealer.Seal(purposeState, loginState{
		Redirect: req.redirect, AppState: req.state, Challenge: req.challenge, ClientID: req.clientID,
		Verifier: verifier, RedirectSent: req.redirectSent, Expires: f.now().Add(stateLifetime).Unix(),
	})
	if err != nil {
		f.event("authorize failed")
		f.refuseAuthorize(w, r, req, "server_error", "")
		return
	}
	scopes := f.cfg.UpstreamScopes
	if scopes == "" {
		scopes = defaultScopes
	}
	params := url.Values{
		paramResponseType:    {responseCode},
		paramClientID:        {f.cfg.UpstreamClientID},
		paramRedirect:        {f.cfg.PublicURL + pathCallback},
		"scope":              {scopes},
		"code_challenge":     {s256(verifier)},
		paramChallengeMethod: {methodS256},
		paramState:           {sealed},
	}
	http.SetCookie(w, &http.Cookie{
		Name: stateCookieName(sealed), Value: stateBinding(sealed), Path: pathCallback, MaxAge: int(stateLifetime.Seconds()),
		HttpOnly: true, Secure: f.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
	f.event("sign-in started")
	redirectWith(w, r, f.cfg.UpstreamAuthorizeURL, params)
}

func (f *front) secureCookies() bool {
	u, err := url.Parse(f.cfg.PublicURL)
	return err == nil && u.Scheme == schemeHTTPS
}

// stateBinding is what the cookie holds: a hash of the state, so only the browser that
// started the sign-in can finish it.
func stateBinding(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

// s256 is the PKCE S256 transform of a verifier.
func s256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func validChallenge(c string) bool {
	if len(c) != 43 {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	return err == nil && len(b) == sha256.Size
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
