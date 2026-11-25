package signin

import (
	"crypto/subtle"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// token is the app's token endpoint. It returns the upstream's response as received, so the
// app ends up holding the upstream's own token and the front holds none. It never reads a
// `resource` parameter, so none is forwarded.
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
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "the code is not valid or has expired")
		return
	}
	if msg := checkCodeUse(r.PostForm, code); msg != "" {
		f.event("code refused", "reason", "mismatch")
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", msg)
		return
	}
	f.callUpstream(w, r, url.Values{
		paramGrantType:    {grantAuthCode},
		paramCode:         {code.UpstreamCode},
		paramRedirect:     {f.cfg.PublicURL + pathCallback},
		paramClientID:     {f.cfg.UpstreamClientID},
		paramCodeVerifier: {code.Verifier},
	})
}

// checkCodeUse returns why the request may not use the code, or "".
func checkCodeUse(form url.Values, code authCode) string {
	return checkCodeBinding(form, code.ClientID, code.Redirect, code.RedirectSent, code.Challenge)
}

// checkCodeBinding returns why a token request may not redeem a code bound to a client, a redirect
// address and a PKCE challenge, or "". Both fronts redeem codes through it.
func checkCodeBinding(form url.Values, clientID, redirect string, redirectSent bool, challenge string) string {
	if form.Get(paramClientID) != clientID {
		return "the code was issued to a different client"
	}
	ru := form.Get(paramRedirect)
	if (ru != "" || redirectSent) && ru != redirect {
		return "redirect_uri does not match the authorization request"
	}
	if !verifierMatches(form.Get(paramCodeVerifier), challenge) {
		return "code_verifier does not match the code_challenge"
	}
	return ""
}

// verifierMatches reports whether a PKCE code_verifier of a valid length hashes to the challenge.
func verifierMatches(verifier, challenge string) bool {
	got := s256(verifier)
	return len(verifier) >= 43 && len(verifier) <= 128 && subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

func (f *front) refresh(w http.ResponseWriter, r *http.Request) {
	rt := r.PostForm.Get(grantRefresh)
	if rt == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}
	f.callUpstream(w, r, url.Values{
		paramGrantType: {grantRefresh},
		grantRefresh:   {rt},
		paramClientID:  {f.cfg.UpstreamClientID},
	})
}

// callUpstream posts form to the upstream token endpoint and relays its status and body
// untouched. The body is never logged or kept.
func (f *front) callUpstream(w http.ResponseWriter, r *http.Request, form url.Values) {
	payload := strings.NewReader(form.Encode())
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, f.cfg.UpstreamTokenURL, payload)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := f.http.Do(req)
	if err != nil {
		f.event("upstream token call failed")
		writeOAuthError(w, http.StatusBadGateway, "temporarily_unavailable", "the sign-in service could not be reached")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstream))
	if err != nil {
		writeOAuthError(w, http.StatusBadGateway, "temporarily_unavailable", "the sign-in service did not answer in full")
		return
	}
	f.event("upstream token call", "grant", form.Get(paramGrantType), "status", resp.StatusCode)
	if resp.StatusCode < 200 || (resp.StatusCode >= 300 && resp.StatusCode < 400) {
		writeOAuthError(w, http.StatusBadGateway, "temporarily_unavailable", "the sign-in service gave an unexpected answer")
		return
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}
