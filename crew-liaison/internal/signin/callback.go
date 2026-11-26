package signin

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"regexp"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// authCode is sealed into the code the app receives. It names the person who signed in and
// the request the code answers; the upstream's code and tokens are not in it.
type authCode struct {
	ID           string `json:"j"`
	Subject      string `json:"s"`
	Challenge    string `json:"c"`
	ClientID     string `json:"i"`
	Redirect     string `json:"r"`
	RedirectSent bool   `json:"p,omitempty"`
	Expires      int64  `json:"e"`
}

// errNoPerson means the upstream signed someone in and the door's verifiers cannot name them.
var errNoPerson = errors.New("the sign-in did not name a person")

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
		p := url.Values{"error": {safeErrorCode(upstreamErr)}}
		if d := q.Get("error_description"); d != "" {
			p.Set("error_description", truncate(d, 200))
		}
		withAppState(p, st)
		redirectWith(w, r, st.Redirect, p)
		return
	}
	f.finishCallback(w, r, st, q.Get(paramCode))
}

// finishCallback exchanges the upstream's code here, in the liaison, keeps the upstream's
// token for the person it names, and sends the app a code of the door's own.
func (f *front) finishCallback(w http.ResponseWriter, r *http.Request, st loginState, upstreamCode string) {
	p := url.Values{}
	withAppState(p, st)
	if upstreamCode == "" {
		p.Set("error", errServerError)
		redirectWith(w, r, st.Redirect, p)
		return
	}
	code, err := f.connect(r, st, upstreamCode)
	if err != nil {
		f.event("sign-in failed", "reason", signInErrorCode(err))
		p.Set("error", signInErrorCode(err))
		redirectWith(w, r, st.Redirect, p)
		return
	}
	p.Set(paramCode, code)
	f.event("sign-in completed")
	redirectWith(w, r, st.Redirect, p)
}

// connect redeems the upstream's code, names the person, keeps their upstream token and
// returns the door's code for the app.
func (f *front) connect(r *http.Request, st loginState, upstreamCode string) (string, error) {
	tokens, err := f.redeemCode(r.Context(), upstreamCode, st.Verifier)
	if err != nil {
		return "", err
	}
	caller, err := f.cfg.Identify(r, tokens.Access)
	if err != nil {
		return "", err
	}
	if caller.Anonymous() || caller.Principal.ID == "" {
		return "", errNoPerson
	}
	if err := f.saveAccount(r.Context(), caller.Principal.ID, f.newAccount(tokens)); err != nil {
		return "", err
	}
	id, err := randomString(16)
	if err != nil {
		return "", err
	}
	return f.sealer.Seal(purposeCode, authCode{
		ID: id, Subject: caller.Principal.ID, Challenge: st.Challenge, ClientID: st.ClientID,
		Redirect: st.Redirect, RedirectSent: st.RedirectSent, Expires: f.now().Add(codeLifetime).Unix(),
	})
}

// signInErrorCode is the OAuth error the app is told when a sign-in could not be finished.
// It never carries the cause.
func signInErrorCode(err error) string {
	switch {
	case errors.Is(err, errUpstreamRefused), errors.Is(err, identity.ErrInvalid), errors.Is(err, errNoPerson):
		return errAccessDenied
	case errors.Is(err, errUpstreamDown):
		return errUnavailable
	default:
		return errServerError
	}
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
	return errServerError
}
