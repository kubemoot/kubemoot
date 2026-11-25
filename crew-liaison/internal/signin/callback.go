package signin

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"regexp"
)

// authCode is sealed into the code the app receives. It carries the upstream code, so the
// front keeps nothing between the redirect and the token request.
type authCode struct {
	UpstreamCode string `json:"u"`
	Verifier     string `json:"v"`
	Challenge    string `json:"c"`
	ClientID     string `json:"i"`
	Redirect     string `json:"r"`
	RedirectSent bool   `json:"p,omitempty"`
	Expires      int64  `json:"e"`
}

// callback receives the person back from the upstream and passes them on to the app.
func (f *front) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var st loginState
	if f.sealer.Open(purposeState, q.Get(paramState), &st) != nil || f.now().Unix() > st.Expires {
		http.Error(w, "the sign-in expired or is not valid, start again from the app", http.StatusBadRequest)
		return
	}
	if !f.cookieMatches(r, q.Get(paramState)) {
		f.event("callback refused", "reason", "cookie")
		http.Error(w, "this sign-in was not started in this browser, start again from the app", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: stateCookieName(q.Get(paramState)), Path: pathCallback, MaxAge: -1, HttpOnly: true, Secure: f.secureCookies(),
	})
	if upstreamErr := q.Get("error"); upstreamErr != "" {
		f.event("upstream refused sign-in")
		p := url.Values{paramError: {safeErrorCode(upstreamErr)}}
		if d := q.Get("error_description"); d != "" {
			p.Set("error_description", truncate(d, 200))
		}
		withAppState(p, st)
		redirectWith(w, r, st.Redirect, p)
		return
	}
	f.finishCallback(w, r, st, q.Get(paramCode))
}

func (f *front) finishCallback(w http.ResponseWriter, r *http.Request, st loginState, upstreamCode string) {
	p := url.Values{}
	withAppState(p, st)
	if upstreamCode == "" {
		p.Set(paramError, "server_error")
		redirectWith(w, r, st.Redirect, p)
		return
	}
	code, err := f.sealer.Seal(purposeCode, authCode{
		UpstreamCode: upstreamCode, Verifier: st.Verifier, Challenge: st.Challenge, ClientID: st.ClientID,
		Redirect: st.Redirect, RedirectSent: st.RedirectSent, Expires: f.now().Add(codeLifetime).Unix(),
	})
	if err != nil {
		f.event("callback failed")
		p.Set(paramError, "server_error")
		redirectWith(w, r, st.Redirect, p)
		return
	}
	p.Set(paramCode, code)
	f.event("sign-in completed")
	redirectWith(w, r, st.Redirect, p)
}

func withAppState(p url.Values, st loginState) {
	if st.AppState != "" {
		p.Set(paramState, st.AppState)
	}
}

func (f *front) cookieMatches(r *http.Request, state string) bool {
	c, err := r.Cookie(stateCookieName(state))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(stateBinding(state))) == 1
}

var errorCodeForm = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// safeErrorCode passes an upstream error code only when it has the RFC 6749 shape.
func safeErrorCode(code string) string {
	if errorCodeForm.MatchString(code) {
		return code
	}
	return "server_error"
}
