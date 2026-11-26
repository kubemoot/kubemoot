package signin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/accounts"
)

// ConnectedAccounts is what the connect pages need from the accounts layer. A door without
// it has no connect routes and keeps nothing for anyone.
type ConnectedAccounts interface {
	// Offered returns the account as it can be connected, or accounts.ErrNotAllowed.
	Offered(ctx context.Context, name string) (accounts.Offer, error)
	// Complete trades the service's code for tokens and keeps them for the person.
	Complete(
		ctx context.Context, principal string, offer accounts.Offer, consented []string, code, verifier, redirect string,
	) error
	// Disconnect revokes at the service when it can and deletes the record either way.
	Disconnect(ctx context.Context, principal, account string) (accounts.Outcome, error)
	// Overview lists the accounts the person's crews could use, with their state.
	Overview(ctx context.Context, principal string) ([]accounts.Entry, error)
	// SignOut deletes every connection the person has.
	SignOut(ctx context.Context, principal string) (int, error)
}

// ErrConnectOff is the answer of a door that keeps no connected accounts.
var ErrConnectOff = errors.New("connected accounts are not turned on")

// Paths of the connect flow, all under /oauth so the door's cookies and routes cover them.
const (
	pathConnect    = "/oauth/connect/"
	pathAccounts   = "/oauth/accounts"
	pathDisconnect = "/oauth/accounts/disconnect"
	pathSignOut    = "/oauth/signout"
	pathRevoke     = "/oauth/revoke"
)

const (
	purposeConnectHandle = "door-connect-handle"
	purposeConnectState  = "door-connect-state"
	handleLifetime       = 10 * time.Minute
	paramHandle          = "c"

	expiredWords = "It has expired. Ask your chat app again for a new one."
	changedWords = "Your sign-in changed. Start again from your chat app."
)

// connectHandle is what a connect link carries. It grants nothing alone: opening it needs the
// door session of the person it names.
type connectHandle struct {
	Principal string `json:"p"`
	Account   string `json:"a"`
	Crew      string `json:"c"`
	ID        string `json:"j"`
	Expires   int64  `json:"e"`
}

// connectState is sealed into the service's `state`, so the callback needs no store.
type connectState struct {
	Principal string   `json:"p"`
	Account   string   `json:"a"`
	Crews     []string `json:"c"`
	Verifier  string   `json:"v"`
	Expires   int64    `json:"e"`
}

// ConnectURL is the link that lets the person connect the account for a crew. It is made by
// the liaison and handed back in its own result, never produced by a model.
func (d *door) ConnectURL(principal, account, crew string) (string, error) {
	if d.cfg.Accounts == nil {
		return "", ErrConnectOff
	}
	id, err := randomString(16)
	if err != nil {
		return "", err
	}
	blob, err := d.sealer.Seal(purposeConnectHandle, connectHandle{
		Principal: principal, Account: account, Crew: crew, ID: id, Expires: d.now().Add(handleLifetime).Unix(),
	})
	if err != nil {
		return "", err
	}
	return d.cfg.PublicURL + pathConnect + url.PathEscape(account) + "?" + paramHandle + "=" + blob, nil
}

func (d *door) connectRoutes() {
	if d.cfg.Accounts == nil {
		return
	}
	d.mux.HandleFunc("GET "+pathConnect+"{account}", d.connectPage)
	d.mux.HandleFunc("POST "+pathConnect+"{account}", d.connectStart)
	d.mux.HandleFunc("GET "+pathAccounts, d.accountsPage)
	d.mux.HandleFunc("POST "+pathDisconnect, d.disconnect)
	d.mux.HandleFunc("POST "+pathSignOut, d.signOut)
	d.mux.HandleFunc("POST "+pathRevoke, d.revoke)
}

func principalOf(s session) string { return accounts.PrincipalID(s.Who.User) }

// signedIn returns the browser's person. With none, a page is sent to the identity provider
// and back (a form post is refused), and false is returned.
func (d *door) signedIn(w http.ResponseWriter, r *http.Request) (session, bool) {
	if s, ok := d.session(r); ok {
		return d.touchSession(w, s), true
	}
	if r.Method != http.MethodGet {
		d.notice(w, http.StatusUnauthorized, "Your sign-in ended", "Open the link from your chat app again.")
		return session{}, false
	}
	d.beginLogin(w, r, doorState{Return: r.URL.RequestURI()}, func(_, desc string) {
		d.notice(w, http.StatusBadGateway, "Sign-in is not available", orElse(desc, "Try again in a moment."))
	})
	return session{}, false
}

func orElse(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// finishReturnLogin completes a sign-in that began at a page of the door and sends the person
// back to it.
func (d *door) finishReturnLogin(w http.ResponseWriter, r *http.Request, st doorState) {
	who, _, _ := d.identify(r, st)
	if who.User == "" || !safeReturn(st.Return) {
		d.notice(w, http.StatusForbidden, "Sign-in did not complete", "Start again from your chat app.")
		return
	}
	d.event("signed in")
	d.startSession(w, who)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, st.Return, http.StatusFound)
}

// safeReturn accepts only an address of the door's own connect pages.
func safeReturn(p string) bool {
	if strings.HasPrefix(p, "//") || strings.Contains(p, "\\") {
		return false
	}
	return strings.HasPrefix(p, pathConnect) || strings.HasPrefix(p, pathAccounts)
}

// openHandle reads and checks the link's handle for the account in the path.
func (d *door) openHandle(blob, account string) (connectHandle, bool) {
	var h connectHandle
	if d.sealer.Open(purposeConnectHandle, blob, &h) != nil || d.now().Unix() > h.Expires || h.Account != account {
		return connectHandle{}, false
	}
	return h, true
}

// connectPage shows what connecting does, to the person the link was made for.
func (d *door) connectPage(w http.ResponseWriter, r *http.Request) {
	s, ok := d.signedIn(w, r)
	if !ok {
		return
	}
	blob := r.URL.Query().Get(paramHandle)
	h, ok := d.openHandle(blob, r.PathValue("account"))
	if !ok {
		d.notice(w, http.StatusBadRequest, "This link is not valid", expiredWords)
		return
	}
	if h.Principal != principalOf(s) {
		d.event("connect refused", "reason", "another person")
		d.notice(w, http.StatusForbidden, "This link was made for another person",
			"You are signed in as someone else. Ask in your own chat app for your own link.")
		return
	}
	offer, err := d.cfg.Accounts.Offered(r.Context(), h.Account)
	if err != nil {
		d.notice(w, http.StatusNotFound, "This account is not available", "No crew you can use offers it on this install.")
		return
	}
	csrf, err := d.newCSRF(s.Who.User, blob)
	if err != nil {
		d.notice(w, http.StatusInternalServerError, "The page could not be made", "Start again from your chat app.")
		return
	}
	page(w, http.StatusOK)
	_ = connectTemplate.Execute(w, d.connectView(s, offer, blob, csrf))
}

// connectStart begins the connection at the service: a post with the person's session, a token
// made for this handle, and the same person the link names.
func (d *door) connectStart(w http.ResponseWriter, r *http.Request) {
	s, h, offer, ok := d.checkConnectPost(w, r)
	if !ok {
		return
	}
	// A link starts one connection: a second post with the same handle is refused.
	if d.replay.Remember("connect:"+h.ID, d.now(), time.Unix(h.Expires, 0)) {
		d.notice(w, http.StatusBadRequest, "This link was already used", "Ask your chat app again for a new one.")
		return
	}
	verifier, err1 := randomString(32)
	state, err2 := d.sealer.Seal(purposeConnectState, connectState{
		Principal: principalOf(s), Account: h.Account, Crews: offer.Crews, Verifier: verifier,
		Expires: d.now().Add(stateLifetime).Unix(),
	})
	if err1 != nil || err2 != nil {
		d.notice(w, http.StatusInternalServerError, "The connection could not start", "Start again from your chat app.")
		return
	}
	to, err := upstreamURL(offer, d.cfg.PublicURL+pathCallback, state, verifier)
	if err != nil {
		d.notice(w, http.StatusBadGateway, "The connection could not start", "The service's address is not valid.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: stateCookieName(state), Value: stateBinding(state), Path: pathCallback, MaxAge: int(stateLifetime.Seconds()),
		HttpOnly: true, Secure: d.base.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
	d.event("connect started", "account", h.Account)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, to, http.StatusFound)
}

// checkConnectPost runs every check of the consent post and answers the page itself on a failure.
func (d *door) checkConnectPost(w http.ResponseWriter, r *http.Request) (session, connectHandle, accounts.Offer, bool) {
	var none accounts.Offer
	if !d.sameOrigin(r) {
		d.notice(w, http.StatusForbidden, "Not from this site", "This form was not submitted from this site.")
		return session{}, connectHandle{}, none, false
	}
	s, ok := d.signedIn(w, r)
	if !ok {
		return session{}, connectHandle{}, none, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	blob := ""
	if r.ParseForm() == nil {
		blob = r.PostForm.Get("req")
	}
	h, ok := d.openHandle(blob, r.PathValue("account"))
	if !ok || h.Principal != principalOf(s) || !d.validCSRF(r.PostForm.Get("csrf"), s.Who.User, blob) {
		d.event("connect refused", "reason", "token")
		d.notice(w, http.StatusForbidden, "This form is not valid", "It has expired. Ask your chat app again for a new link.")
		return session{}, connectHandle{}, none, false
	}
	offer, err := d.cfg.Accounts.Offered(r.Context(), h.Account)
	if err != nil {
		d.notice(w, http.StatusNotFound, "This account is not available", "No crew you can use offers it on this install.")
		return session{}, connectHandle{}, none, false
	}
	return s, h, offer, true
}

// upstreamURL is the service's authorization request: the install's client, PKCE S256, the
// scopes the crews declared, and the sealed state. No resource the chat app sent is passed on.
func upstreamURL(o accounts.Offer, redirect, state, verifier string) (string, error) {
	u, err := url.Parse(o.Declaration.AuthorizeURL)
	if err != nil || u.Scheme != schemeHTTPS || u.Host == "" {
		return "", errors.New("the authorization address is not https")
	}
	q := u.Query()
	q.Set("response_type", responseCode)
	q.Set("client_id", o.Registration.ClientID)
	q.Set("redirect_uri", redirect)
	q.Set(paramState, state)
	q.Set(paramCodeChallenge, s256(verifier))
	q.Set(paramChallengeMethod, methodS256)
	if len(o.Declaration.Scopes) > 0 {
		q.Set("scope", strings.Join(o.Declaration.Scopes, " "))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// connectCallback receives the person back from the service.
func (d *door) connectCallback(w http.ResponseWriter, r *http.Request, cs connectState) {
	q := r.URL.Query()
	state := q.Get(paramState)
	if d.now().Unix() > cs.Expires || !d.base.cookieMatches(r, state) {
		d.event("connect callback refused", "reason", "cookie or expiry")
		d.notice(w, http.StatusBadRequest, "The connection could not finish", "Start again from your chat app.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: stateCookieName(state), Path: pathCallback, MaxAge: -1, HttpOnly: true, Secure: d.base.secureCookies(),
	})
	s, ok := d.session(r)
	if !ok || principalOf(s) != cs.Principal {
		d.event("connect callback refused", "reason", "person")
		d.notice(w, http.StatusForbidden, "The connection could not finish", changedWords)
		return
	}
	offer, err := d.cfg.Accounts.Offered(r.Context(), cs.Account)
	if err != nil {
		d.notice(w, http.StatusNotFound, "This account is not available", "No crew you can use offers it on this install.")
		return
	}
	d.completeConnect(w, r, cs, offer)
}

// completeConnect checks what the service sent and keeps the tokens.
func (d *door) completeConnect(w http.ResponseWriter, r *http.Request, cs connectState, offer accounts.Offer) {
	q := r.URL.Query()
	if iss := q.Get(paramIss); iss != "" && !accounts.IssuerAccepted(iss, offer.Registration, offer.Declaration) {
		d.event("connect callback refused", "reason", "issuer")
		d.notice(w, http.StatusBadRequest, "The connection could not finish", "The answer came from an unexpected service.")
		return
	}
	if q.Get("error") != "" {
		d.event("connect refused by the service", "account", cs.Account)
		d.notice(w, http.StatusOK, "Not connected", offer.Declaration.DisplayName+" did not connect your account.")
		return
	}
	err := d.cfg.Accounts.Complete(
		r.Context(), cs.Principal, offer, cs.Crews, q.Get(paramCode), cs.Verifier, d.cfg.PublicURL+pathCallback,
	)
	if err != nil {
		d.event("connect failed", "account", cs.Account)
		d.notice(w, http.StatusBadGateway, "Not connected",
			offer.Declaration.DisplayName+" could not complete the connection. Try again.")
		return
	}
	d.event("account connected", "account", cs.Account)
	d.notice(w, http.StatusOK, "Connected",
		"Your "+offer.Declaration.DisplayName+" account is connected. Return to your chat and ask again.")
}
