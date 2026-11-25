package signin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"time"
)

// The browser's own state at the door is two sealed cookies and nothing on the server.
const (
	sessionCookie = "kubemoot_door_session"
	consentCookie = "kubemoot_door_consent"

	sessionIdle     = 10 * time.Minute
	sessionAbsolute = 8 * time.Hour
	consentLifetime = 180 * 24 * time.Hour
	csrfLifetime    = 15 * time.Minute
	maxConsents     = 40
	cookiePath      = "/oauth"
)

// session is the person signed in at this browser. It slides: each use renews the idle time,
// but never past the absolute one.
type session struct {
	Who    person `json:"p"`
	Issued int64  `json:"i"`
	Last   int64  `json:"l"`
}

// consentRecord is the apps one person allowed at this browser, each as a digest of the app and
// the host it returns to, so a changed redirect host asks again.
type consentRecord struct {
	User string   `json:"u"`
	Seen []string `json:"s"`
}

// csrfToken binds a consent form to the person and the one request it decides.
type csrfToken struct {
	User    string `json:"u"`
	Request string `json:"r"`
	Expires int64  `json:"e"`
}

func (d *door) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: cookiePath, MaxAge: maxAge,
		HttpOnly: true, Secure: d.base.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
}

// session returns the signed-in person of this browser, or false when there is none, the
// cookie is not ours, or it has idled or aged out.
func (d *door) session(r *http.Request) (session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return session{}, false
	}
	var s session
	now := d.now().Unix()
	if d.sealer.Open(purposeSession, c.Value, &s) != nil || s.Who.User == "" {
		return session{}, false
	}
	if now-s.Last > int64(sessionIdle.Seconds()) || now-s.Issued > int64(sessionAbsolute.Seconds()) {
		return session{}, false
	}
	return s, true
}

// startSession writes the cookie of a person who has just signed in.
func (d *door) startSession(w http.ResponseWriter, who person) session {
	now := d.now().Unix()
	s := session{Who: who, Issued: now, Last: now}
	d.writeSession(w, s)
	return s
}

// touchSession renews the idle time of an existing session.
func (d *door) touchSession(w http.ResponseWriter, s session) session {
	s.Last = d.now().Unix()
	d.writeSession(w, s)
	return s
}

func (d *door) writeSession(w http.ResponseWriter, s session) {
	blob, err := d.sealer.Seal(purposeSession, s)
	if err != nil {
		return
	}
	remaining := int(sessionAbsolute.Seconds()) - int(d.now().Unix()-s.Issued)
	d.setCookie(w, sessionCookie, blob, max(remaining, 1))
}

func digest(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// consentKey identifies an app as the person sees it: who it is and where it sends them back.
func consentKey(req authRequest) string {
	host := ""
	if u, err := url.Parse(req.Redirect); err == nil {
		host = u.Host
	}
	return digest(req.ClientID, host)[:32]
}

// consented reports whether this browser's person already allowed the app.
func (d *door) consented(r *http.Request, user string, req authRequest) bool {
	c, err := r.Cookie(consentCookie)
	if err != nil {
		return false
	}
	var rec consentRecord
	if d.sealer.Open(purposeConsent, c.Value, &rec) != nil || rec.User != user {
		return false
	}
	key := consentKey(req)
	for _, seen := range rec.Seen {
		if subtle.ConstantTimeCompare([]byte(seen), []byte(key)) == 1 {
			return true
		}
	}
	return false
}

// rememberConsent adds the app to the browser's record for this person, dropping the oldest
// entries past a bound. A record of another person is replaced: consent is never shared.
func (d *door) rememberConsent(w http.ResponseWriter, r *http.Request, user string, req authRequest) {
	rec := consentRecord{User: user}
	if c, err := r.Cookie(consentCookie); err == nil {
		var old consentRecord
		if d.sealer.Open(purposeConsent, c.Value, &old) == nil && old.User == user {
			rec = old
		}
	}
	rec.Seen = append(rec.Seen, consentKey(req))
	if len(rec.Seen) > maxConsents {
		rec.Seen = rec.Seen[len(rec.Seen)-maxConsents:]
	}
	if blob, err := d.sealer.Seal(purposeConsent, rec); err == nil {
		d.setCookie(w, consentCookie, blob, int(consentLifetime.Seconds()))
	}
}

// newCSRF is the token a consent form carries for this person and this request.
func (d *door) newCSRF(user, requestBlob string) (string, error) {
	return d.sealer.Seal(purposeCSRF, csrfToken{
		User: user, Request: digest(requestBlob), Expires: d.now().Add(csrfLifetime).Unix(),
	})
}

// validCSRF reports whether token was made for this person and this request and is still fresh.
func (d *door) validCSRF(token, user, requestBlob string) bool {
	var t csrfToken
	if d.sealer.Open(purposeCSRF, token, &t) != nil || d.now().Unix() > t.Expires {
		return false
	}
	return t.User == user && subtle.ConstantTimeCompare([]byte(t.Request), []byte(digest(requestBlob))) == 1
}

// sameOrigin is the second line against cross-site posts, after the token and SameSite: a
// browser that says the post came from elsewhere is refused. A request that says nothing is not
// refused here, because the token still has to match.
func (d *door) sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return false
	}
	origin := r.Header.Get("Origin")
	return origin == "" || origin == d.cfg.PublicURL
}
