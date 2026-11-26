package signin

import (
	"net/http"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/accounts"
)

// accountsPage lists the person's connected accounts, each with a Disconnect button, and the
// ones they could connect.
func (d *door) accountsPage(w http.ResponseWriter, r *http.Request) {
	s, ok := d.signedIn(w, r)
	if !ok {
		return
	}
	entries, err := d.cfg.Accounts.Overview(r.Context(), d.principalOf(s))
	if err != nil {
		d.notice(w, http.StatusBadGateway, "Your accounts could not be listed", "Try again in a moment.")
		return
	}
	view := accountsView{
		Door: d.cfg.displayName(), User: userLabel(s.Who), SignOutCSRF: d.csrfOrEmpty(s.Who.User, "signout"),
	}
	for _, e := range entries {
		view.Accounts = append(view.Accounts, d.accountRow(s, e))
	}
	page(w, http.StatusOK)
	_ = accountsTemplate.Execute(w, view)
}

func (d *door) csrfOrEmpty(user, bound string) string {
	token, err := d.newCSRF(user, bound)
	if err != nil {
		return ""
	}
	return token
}

// accountRow is one account of the page: a Disconnect form when it is kept, else a link that
// starts connecting it.
func (d *door) accountRow(s session, e accounts.Entry) accountRowView {
	row := accountRowView{
		Account: e.Account, Name: e.DisplayName, Purpose: e.Purpose, State: stateWords(e.State), Crews: e.Crews,
		Disconnect: e.State != accounts.StateMissing,
	}
	if row.Disconnect {
		row.CSRF = d.csrfOrEmpty(s.Who.User, "disconnect:"+e.Account)
	}
	if e.State != accounts.StateConnected && len(e.Crews) > 0 {
		row.ConnectURL = d.connectLinkFor(s, e)
	}
	return row
}

// connectLinkFor makes the link for the person who is looking at the page, so the same-person
// check holds by construction.
func (d *door) connectLinkFor(s session, e accounts.Entry) string {
	crew := ""
	if len(e.Crews) > 0 {
		crew = e.Crews[0]
	}
	link, err := d.ConnectURL(d.principalOf(s), e.Account, crew)
	if err != nil {
		return ""
	}
	return link
}

func stateWords(state string) string {
	switch state {
	case accounts.StateConnected:
		return "Connected"
	case accounts.StateNeedsReconnect:
		return "Needs to be connected again"
	}
	return "Not connected"
}

// disconnect revokes at the service when it can and deletes the record either way, then says
// which of the two happened.
func (d *door) disconnect(w http.ResponseWriter, r *http.Request) {
	s, ok := d.postedPerson(w, r)
	if !ok {
		return
	}
	account := r.PostForm.Get("account")
	if !d.validCSRF(r.PostForm.Get("csrf"), s.Who.User, "disconnect:"+account) {
		d.event("disconnect refused", "reason", "token")
		d.notice(w, http.StatusForbidden, "This form is not valid", "Open your accounts page again.")
		return
	}
	out, err := d.cfg.Accounts.Disconnect(r.Context(), d.principalOf(s), account)
	if err != nil {
		d.notice(w, http.StatusBadGateway, "Not disconnected", "The record could not be deleted. Try again.")
		return
	}
	d.event("account disconnected", "account", account, "revoked", out.Revoked)
	d.notice(w, http.StatusOK, "Disconnected", disconnectWords(out))
}

func disconnectWords(out accounts.Outcome) string {
	switch {
	case out.Revoked:
		return "Kubemoot deleted what it kept, and the service was told to revoke access."
	case out.Asked:
		return "Kubemoot deleted what it kept. The service could not be reached to revoke access; " +
			"you can revoke it in your account there."
	}
	return "Kubemoot deleted what it kept. This service has no revocation address; " +
		"you can revoke access in your account there."
}

// postedPerson is the checks every state-changing post of the accounts pages shares: the
// same site, a signed-in person, a parsed form.
func (d *door) postedPerson(w http.ResponseWriter, r *http.Request) (session, bool) {
	if !d.sameOrigin(r) {
		d.notice(w, http.StatusForbidden, "Not from this site", "This form was not submitted from this site.")
		return session{}, false
	}
	s, ok := d.signedIn(w, r)
	if !ok {
		return session{}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if r.ParseForm() != nil {
		d.notice(w, http.StatusBadRequest, "This form is not valid", "Open your accounts page again.")
		return session{}, false
	}
	return s, true
}

// signOut ends the person's door session in this browser and deletes every connection they
// have.
func (d *door) signOut(w http.ResponseWriter, r *http.Request) {
	s, ok := d.postedPerson(w, r)
	if !ok {
		return
	}
	if !d.validCSRF(r.PostForm.Get("csrf"), s.Who.User, "signout") {
		d.notice(w, http.StatusForbidden, "This form is not valid", "Open your accounts page again.")
		return
	}
	if !d.forgetPerson(r, d.principalOf(s), "signed out") {
		d.notice(w, http.StatusServiceUnavailable, "Not deleted", notDeletedWords)
		return
	}
	d.setCookie(w, sessionCookie, "", -1)
	d.notice(w, http.StatusOK, "Signed out", "Kubemoot deleted every account connection it kept for you.")
}

// notDeletedWords is what a person is told when what is kept for them could not be deleted: the
// sign-in stays, so they can press the button again.
const notDeletedWords = "What Kubemoot kept for you could not be deleted just now, " +
	"and you are still signed in. Try again."

// forgetPerson deletes everything kept for the person and reports whether it did. A failure is
// logged and is never reported as success.
func (d *door) forgetPerson(r *http.Request, principal, reason string) bool {
	n, err := d.cfg.Accounts.SignOut(r.Context(), principal)
	if err != nil {
		d.event("could not delete a person's connections", "reason", reason)
		return false
	}
	d.event("connections deleted", "reason", reason, "count", n)
	return true
}

// revoke is the door's sign-out for a chat app (RFC 7009): the app presents its refresh token,
// the sign-in's family is refused from then on, and every connection kept for the person is
// deleted. A token that is not valid is answered 200 like one that was revoked, as the RFC asks.
func (d *door) revoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "the body is not a form")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var rt doorRefresh
	known := d.sealer.Open(purposeRefresh, r.PostForm.Get("token"), &rt) == nil
	if known && rt.ClientID == r.PostForm.Get(paramClientID) && d.now().Unix() <= rt.Abs {
		if !d.forgetPerson(r, d.cfg.Accounts.PrincipalID(rt.Who.User), "app signed out") {
			// RFC 7009: the server could not do it now; the app tries again.
			w.Header().Set("Retry-After", "5")
			writeOAuthError(w, http.StatusServiceUnavailable, "temporarily_unavailable",
				"the sign-in could not be ended just now")
			return
		}
		d.revokeFamily(rt.Family, d.now())
	}
	w.WriteHeader(http.StatusOK)
}
