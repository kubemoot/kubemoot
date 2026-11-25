package signin

import (
	"net/http"
	"net/url"
	"time"
)

// doorCode is sealed into the authorization code the chat app receives.
type doorCode struct {
	Who          person `json:"p"`
	ClientID     string `json:"i"`
	Redirect     string `json:"r"`
	RedirectSent bool   `json:"s,omitempty"`
	Challenge    string `json:"c"`
	Resource     string `json:"x"`
	ID           string `json:"j"`
	Expires      int64  `json:"e"`
}

// doorRefresh is a refresh token: sealed, rotated on every use, and bounded by an idle expiry
// and by an absolute one that rotation never extends.
type doorRefresh struct {
	Who      person `json:"p"`
	ClientID string `json:"i"`
	Resource string `json:"x"`
	Family   string `json:"f"`
	ID       string `json:"j"`
	Idle     int64  `json:"e"`
	Abs      int64  `json:"a"`
}

// tokenResponse is the body of RFC 6749 section 5.1.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

const errInvalidGrant = "invalid_grant"

// token is the chat app's token endpoint. A `resource` that is not this door's MCP resource is
// invalid_target, so a token is only ever issued for the audience /mcp accepts.
func (d *door) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "the body is not a form")
		return
	}
	if r.PostForm.Get(paramClientID) == "" {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "client_id is required")
		return
	}
	if res := r.PostForm.Get(paramResource); res != "" && res != d.cfg.Resource() {
		writeOAuthError(w, http.StatusBadRequest, "invalid_target", "this door issues tokens for "+d.cfg.Resource()+" only")
		return
	}
	switch r.PostForm.Get(paramGrantType) {
	case grantAuthCode:
		d.exchangeCode(w, r.PostForm)
	case grantRefresh:
		d.refresh(w, r.PostForm)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token")
	}
}

func (d *door) exchangeCode(w http.ResponseWriter, form url.Values) {
	var code doorCode
	now := d.now()
	if d.sealer.Open(purposeDoorCode, form.Get(paramCode), &code) != nil || now.Unix() > code.Expires {
		d.event("code refused", "reason", "invalid or expired")
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "the code is not valid or has expired")
		return
	}
	if msg := checkDoorCodeUse(form, code); msg != "" {
		d.event("code refused", "reason", "mismatch")
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, msg)
		return
	}
	if d.replay.Remember("code:"+code.ID, now, time.Unix(code.Expires, 0)) {
		// A code used twice means it was copied: end the sign-in it started.
		d.revokeFamily(code.ID, now)
		d.event("code refused", "reason", "reused")
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "the code was already used")
		return
	}
	abs := now.Add(d.cfg.RefreshMaxAge)
	d.mint(w, code.Who, code.ClientID, code.ID, abs)
}

// checkDoorCodeUse returns why the request may not use the code, or "".
func checkDoorCodeUse(form url.Values, code doorCode) string {
	if form.Get(paramClientID) != code.ClientID {
		return "the code was issued to a different client"
	}
	ru := form.Get(paramRedirect)
	if (ru != "" || code.RedirectSent) && ru != code.Redirect {
		return "redirect_uri does not match the authorization request"
	}
	if !verifierMatches(form.Get(paramCodeVerifier), code.Challenge) {
		return "code_verifier does not match the code_challenge"
	}
	return ""
}

func (d *door) refresh(w http.ResponseWriter, form url.Values) {
	var rt doorRefresh
	now := d.now()
	if d.sealer.Open(purposeRefresh, form.Get(grantRefresh), &rt) != nil || now.Unix() > rt.Idle || now.Unix() > rt.Abs {
		d.event("refresh refused", "reason", "invalid or expired")
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "the refresh token is not valid or has expired")
		return
	}
	if form.Get(paramClientID) != rt.ClientID || d.replay.Has("revoked:"+rt.Family, now) {
		d.event("refresh refused", "reason", "client or revoked")
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "the refresh token is not valid")
		return
	}
	if d.replay.Remember("refresh:"+rt.ID, now, time.Unix(rt.Idle, 0)) {
		// A rotated token shown again was copied: end the whole sign-in, the copy and the original.
		d.revokeFamily(rt.Family, now)
		d.event("refresh refused", "reason", "reused")
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "the refresh token was already used")
		return
	}
	d.mint(w, rt.Who, rt.ClientID, rt.Family, time.Unix(rt.Abs, 0))
}

// revokeFamily refuses every refresh token of one sign-in for as long as one could live.
func (d *door) revokeFamily(family string, now time.Time) {
	d.replay.Remember("revoked:"+family, now, now.Add(d.cfg.RefreshMaxAge))
}

// mint answers with a fresh access token and the next refresh token of the family. The idle
// expiry renews with each use but never passes the absolute one.
func (d *door) mint(w http.ResponseWriter, who person, clientID, family string, abs time.Time) {
	now := d.now()
	accessID, err1 := randomString(16)
	refreshID, err2 := randomString(16)
	if err1 != nil || err2 != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	access, err := d.issueAccess(who, clientID, accessID, now)
	if err != nil {
		d.event("access token failed")
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	idle := now.Add(refreshIdleLifetime)
	if idle.After(abs) {
		idle = abs
	}
	refresh, err := d.sealer.Seal(purposeRefresh, doorRefresh{
		Who: who, ClientID: clientID, Resource: d.cfg.Resource(), Family: family, ID: refreshID,
		Idle: idle.Unix(), Abs: abs.Unix(),
	})
	if err != nil {
		d.event("refresh token failed")
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	d.event("tokens issued")
	writeJSON(w, http.StatusOK, tokenResponse{
		AccessToken: access, TokenType: "Bearer", ExpiresIn: int(accessTokenLifetime.Seconds()), RefreshToken: refresh,
	})
}
