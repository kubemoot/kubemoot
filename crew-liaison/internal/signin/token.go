package signin

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// codeUsedFor is how long a used code's id is remembered: the code's own lifetime with room
// for clock difference between replicas.
const codeUsedFor = 2 * codeLifetime

// token is the app's token endpoint. It answers with the door's own tokens: an access token
// for /mcp and a refresh token that works once. No call goes to the upstream from here, and
// the upstream's tokens are never in an answer.
func (f *front) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "the body is not a form")
		return
	}
	var reg registration
	if f.sealer.Open(purposeClient, r.PostForm.Get(paramClientID), &reg) != nil {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "unknown client")
		return
	}
	if !f.resourceIsDoor(r.PostForm.Get(paramResource)) {
		writeOAuthError(w, http.StatusBadRequest, errInvalidTarget, "this server issues tokens for its own /mcp only")
		return
	}
	switch r.PostForm.Get(paramGrantType) {
	case grantAuthCode:
		f.exchangeCode(w, r)
	case grantRefresh:
		f.refresh(w, r)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token")
	}
}

func (f *front) exchangeCode(w http.ResponseWriter, r *http.Request) {
	var code authCode
	if f.sealer.Open(purposeCode, r.PostForm.Get(paramCode), &code) != nil || f.now().Unix() > code.Expires {
		f.event("code refused", "reason", "invalid or expired")
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "the code is not valid or has expired")
		return
	}
	if msg := checkCodeUse(r.PostForm, code); msg != "" {
		f.event("code refused", "reason", "mismatch")
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, msg)
		return
	}
	// A code works once: the first request to record its id wins, on every replica.
	err := f.store.Create(r.Context(), "code."+code.ID, []byte{1}, codeUsedFor)
	if errors.Is(err, tokenstore.ErrExists) {
		f.event("code refused", "reason", "used before")
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "the code was used before")
		return
	}
	if err != nil {
		f.storeFailed(w, err)
		return
	}
	sessionID, err := randomString(16)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, errServerError, "")
		return
	}
	f.answerTokens(w, r, sessionID, session{Subject: code.Subject, ClientID: code.ClientID})
}

// checkCodeUse returns why the request may not use the code, or "".
func checkCodeUse(form url.Values, code authCode) string {
	if form.Get(paramClientID) != code.ClientID {
		return "the code was issued to a different client"
	}
	ru := form.Get(paramRedirect)
	if (ru != "" || code.RedirectSent) && ru != code.Redirect {
		return "redirect_uri does not match the authorization request"
	}
	verifier := form.Get("code_verifier")
	got := s256(verifier)
	if len(verifier) < 43 || len(verifier) > 128 || subtle.ConstantTimeCompare([]byte(got), []byte(code.Challenge)) != 1 {
		return "code_verifier does not match the code_challenge"
	}
	return ""
}

// refresh answers a refresh token with a new token pair. A refresh token works once: one
// that is presented again ends the session, because either the app or someone who copied
// the token is replaying it. The person must still have an upstream token kept, or there is
// nothing the door could do for them.
func (f *front) refresh(w http.ResponseWriter, r *http.Request) {
	var grant refreshGrant
	if f.sealer.Open(purposeRefresh, r.PostForm.Get(grantRefresh), &grant) != nil ||
		grant.ClientID != r.PostForm.Get(paramClientID) {
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "the refresh token is not valid for this client")
		return
	}
	ctx := r.Context()
	s, err := f.loadSession(ctx, grant.Session)
	if errors.Is(err, tokenstore.ErrNotFound) {
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "the sign-in ended, sign in again")
		return
	}
	if err != nil {
		f.storeFailed(w, err)
		return
	}
	if subtle.ConstantTimeCompare([]byte(s.Refresh), []byte(grant.ID)) != 1 {
		f.endSession(ctx, grant.Session)
		f.event("refresh refused", "reason", "used before")
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "the refresh token was used before, sign in again")
		return
	}
	if _, err := f.loadAccount(ctx, s.Subject); err != nil {
		f.refuseRefresh(w, r, grant.Session, err)
		return
	}
	f.answerTokens(w, r, grant.Session, s)
}

// refuseRefresh answers a refresh for a person whose upstream token could not be read: gone
// ends the session, a store that could not be asked is a server error.
func (f *front) refuseRefresh(w http.ResponseWriter, r *http.Request, sessionID string, err error) {
	if !errors.Is(err, identity.ErrInvalid) {
		f.storeFailed(w, err)
		return
	}
	f.endSession(r.Context(), sessionID)
	f.event("refresh refused", "reason", "no kept token")
	writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "the sign-in ended, sign in again")
}

func (f *front) answerTokens(w http.ResponseWriter, r *http.Request, sessionID string, s session) {
	tokens, err := f.issue(r.Context(), sessionID, s)
	if err != nil {
		f.storeFailed(w, err)
		return
	}
	f.event("door tokens issued")
	writeJSON(w, http.StatusOK, tokens)
}

// storeFailed answers a request the token store could not serve. The cause is logged by
// kind only.
func (f *front) storeFailed(w http.ResponseWriter, err error) {
	f.event("token store failed", "error", err.Error())
	writeOAuthError(w, http.StatusServiceUnavailable, errUnavailable, "the sign-in could not be completed, try again")
}
