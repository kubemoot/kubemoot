package signin

import (
	"net/http"
	"net/url"
	"slices"
)

// authRequest is a validated request from a chat app, sealed so the sign-in and the consent
// page can carry it without the door keeping anything.
type authRequest struct {
	ClientID     string `json:"i"`
	ClientName   string `json:"n,omitempty"`
	Redirect     string `json:"r"`
	RedirectSent bool   `json:"p,omitempty"`
	State        string `json:"s,omitempty"`
	Challenge    string `json:"c"`
	Resource     string `json:"x"`
	Expires      int64  `json:"e"`
}

// doorState is sealed into the identity provider's `state`, so the callback needs no store.
type doorState struct {
	Request  string `json:"q"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Expires  int64  `json:"e"`
}

const (
	purposeAuthReq   = "door-request"
	purposeDoorState = "door-state"
	requestLifetime  = stateLifetime
)

// authorize starts a sign-in. A problem with the client or the redirect is shown to the browser
// and never redirected, since the redirect is not trusted yet; anything after goes back to the app.
func (d *door) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	client, err := d.resolveClient(r.Context(), q.Get(paramClientID))
	if err != nil {
		http.Error(w, "unknown client", http.StatusBadRequest)
		return
	}
	redirect := q.Get(paramRedirect)
	if redirect == "" && len(client.RedirectURIs) == 1 {
		redirect = client.RedirectURIs[0]
	}
	if !slices.Contains(client.RedirectURIs, redirect) || !d.base.redirectAllowed(redirect) {
		http.Error(w, "redirect_uri is not registered for this client", http.StatusBadRequest)
		return
	}
	req := authRequest{
		ClientID: client.ID, ClientName: client.Name, Redirect: redirect, RedirectSent: q.Get(paramRedirect) != "",
		State: q.Get(paramState), Challenge: q.Get(paramCodeChallenge), Resource: q.Get(paramResource),
		Expires: d.now().Add(requestLifetime).Unix(),
	}
	if code, desc := d.checkParams(q); code != "" {
		d.refuse(w, r, req, code, desc)
		return
	}
	blob, err := d.sealer.Seal(purposeAuthReq, req)
	if err != nil {
		d.refuse(w, r, req, "server_error", "")
		return
	}
	if s, ok := d.session(r); ok {
		d.proceed(w, r, blob, req, d.touchSession(w, s))
		return
	}
	d.startLogin(w, r, blob, req)
}

// checkParams returns the OAuth error of a request that cannot be served, or "".
func (d *door) checkParams(q url.Values) (code, description string) {
	switch {
	case q.Get(paramResponseType) != responseCode:
		return "unsupported_response_type", "only response_type=code is supported"
	case q.Get(paramChallengeMethod) != methodS256 || !validChallenge(q.Get(paramCodeChallenge)):
		return "invalid_request", "a PKCE code_challenge with method S256 is required"
	case q.Get(paramResource) != d.cfg.Resource():
		return "invalid_target", "resource must be " + d.cfg.Resource()
	}
	return "", ""
}

// refuse sends the error back to the app's redirect with the door's issuer (RFC 9207), so an
// app that talks to several authorization servers can tell which one answered.
func (d *door) refuse(w http.ResponseWriter, r *http.Request, req authRequest, code, desc string) {
	p := url.Values{paramError: {code}, "iss": {d.cfg.PublicURL}}
	if desc != "" {
		p.Set("error_description", desc)
	}
	if req.State != "" {
		p.Set(paramState, req.State)
	}
	redirectWith(w, r, req.Redirect, p)
}

// startLogin sends the person to the identity provider with a fresh nonce and PKCE pair, and
// binds the answer to this browser with a cookie.
func (d *door) startLogin(w http.ResponseWriter, r *http.Request, blob string, req authRequest) {
	verifier, err1 := randomString(32)
	nonce, err2 := randomString(24)
	if err1 != nil || err2 != nil {
		d.refuse(w, r, req, "server_error", "")
		return
	}
	state, err := d.sealer.Seal(purposeDoorState, doorState{
		Request: blob, Nonce: nonce, Verifier: verifier, Expires: d.now().Add(stateLifetime).Unix(),
	})
	if err != nil {
		d.refuse(w, r, req, "server_error", "")
		return
	}
	to, err := d.idp.loginURL(r.Context(), state, nonce, verifier, d.cfg.PublicURL+pathCallback)
	if err != nil {
		d.event("sign-in could not start")
		d.refuse(w, r, req, "temporarily_unavailable", "the identity provider could not be reached")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: stateCookieName(state), Value: stateBinding(state), Path: pathCallback, MaxAge: int(stateLifetime.Seconds()),
		HttpOnly: true, Secure: d.base.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
	d.event("sign-in started")
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, to, http.StatusFound)
}

// callback receives the person back from the identity provider.
func (d *door) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var st doorState
	if d.sealer.Open(purposeDoorState, q.Get(paramState), &st) != nil || d.now().Unix() > st.Expires {
		http.Error(w, "the sign-in expired or is not valid, start again from the app", http.StatusBadRequest)
		return
	}
	if !d.base.cookieMatches(r, q.Get(paramState)) {
		d.event("callback refused", "reason", "cookie")
		http.Error(w, "this sign-in was not started in this browser, start again from the app", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: stateCookieName(q.Get(paramState)), Path: pathCallback, MaxAge: -1,
		HttpOnly: true, Secure: d.base.secureCookies(),
	})
	var req authRequest
	if d.sealer.Open(purposeAuthReq, st.Request, &req) != nil {
		http.Error(w, "the sign-in is not valid, start again from the app", http.StatusBadRequest)
		return
	}
	d.finishLogin(w, r, st, req)
}

func (d *door) finishLogin(w http.ResponseWriter, r *http.Request, st doorState, req authRequest) {
	q := r.URL.Query()
	if iss := q.Get("iss"); iss != "" && iss != d.cfg.OIDC.Issuer {
		d.event("callback refused", "reason", "issuer")
		d.refuse(w, r, req, "access_denied", "the sign-in answered from another issuer")
		return
	}
	if q.Get("error") != "" {
		d.event("identity provider refused sign-in")
		d.refuse(w, r, req, "access_denied", "the identity provider did not sign you in")
		return
	}
	who, err := d.idp.finish(r.Context(), q.Get(paramCode), st.Verifier, st.Nonce, d.cfg.PublicURL+pathCallback)
	if err != nil {
		d.event("sign-in failed")
		d.refuse(w, r, req, "access_denied", "the sign-in could not be completed")
		return
	}
	d.event("signed in")
	d.proceed(w, r, st.Request, req, d.startSession(w, who))
}

// proceed is what follows a signed-in person's request: straight to a code when they have already
// allowed this app, else the consent page.
func (d *door) proceed(w http.ResponseWriter, r *http.Request, blob string, req authRequest, s session) {
	if d.consented(r, s.Who.User, req) {
		d.issueCode(w, r, req, s)
		return
	}
	d.showConsent(w, blob, req, s)
}

// issueCode redirects the person back to the app with a sealed, single-use code for 60 seconds.
func (d *door) issueCode(w http.ResponseWriter, r *http.Request, req authRequest, s session) {
	id, err := randomString(16)
	if err != nil {
		d.refuse(w, r, req, "server_error", "")
		return
	}
	code, err := d.sealer.Seal(purposeDoorCode, doorCode{
		Who: s.Who, ClientID: req.ClientID, Redirect: req.Redirect, RedirectSent: req.RedirectSent,
		Challenge: req.Challenge, Resource: req.Resource, ID: id, Expires: d.now().Add(codeLifetime).Unix(),
	})
	if err != nil {
		d.refuse(w, r, req, "server_error", "")
		return
	}
	p := url.Values{paramCode: {code}, "iss": {d.cfg.PublicURL}}
	if req.State != "" {
		p.Set(paramState, req.State)
	}
	d.event("code issued")
	redirectWith(w, r, req.Redirect, p)
}
