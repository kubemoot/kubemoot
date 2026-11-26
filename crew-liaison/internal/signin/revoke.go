package signin

import "net/http"

// revoke is the door's sign-out (RFC 7009). An app presents its refresh token or its access
// token; the session ends and the upstream token kept for that person is deleted at once.
// As the RFC asks, a token that is not valid is answered 200 like one that was revoked.
func (f *front) revoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "the body is not a form")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	sessionID, subject, clientID := f.sessionOf(r.PostForm.Get(paramToken))
	if sessionID == "" || clientID != r.PostForm.Get(paramClientID) {
		w.WriteHeader(http.StatusOK)
		return
	}
	f.endSession(r.Context(), sessionID)
	f.forgetAccount(r.Context(), subject, "signed out")
	f.event("signed out")
	w.WriteHeader(http.StatusOK)
}

// sessionOf reads the session, person and client a door token of either kind names, or "".
func (f *front) sessionOf(token string) (sessionID, subject, clientID string) {
	var grant refreshGrant
	if f.sealer.Open(purposeRefresh, token, &grant) == nil {
		return grant.Session, grant.Subject, grant.ClientID
	}
	var access doorToken
	if f.sealer.Open(purposeDoor, token, &access) == nil && f.current(access) {
		return access.Session, access.Subject, access.ClientID
	}
	return "", "", ""
}
