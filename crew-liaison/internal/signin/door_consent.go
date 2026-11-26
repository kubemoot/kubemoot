package signin

import (
	"crypto/sha256"
	"encoding/base64"
	"html/template"
	"net/http"
	"net/url"
)

// consentStyle is the page's only style. The page's policy allows exactly this text by its hash,
// so nothing injected into the page can add style or script.
const consentStyle = `body{font:16px/1.5 system-ui,sans-serif;margin:0;padding:2rem 1rem;` +
	`background:#f6f7f9;color:#1b1f24}` +
	`main{max-width:32rem;margin:0 auto;background:#fff;padding:1.5rem;border-radius:8px;` +
	`box-shadow:0 1px 3px #0003}` +
	`h1{font-size:1.25rem;margin-top:0}code{word-break:break-all}` +
	`button{font:inherit;padding:.5rem 1rem;margin-right:.5rem;border-radius:6px;` +
	`border:1px solid #6b7280;background:#fff;cursor:pointer}` +
	`button.allow{background:#1d4ed8;color:#fff;border-color:#1d4ed8}`

var consentPolicy = func() string {
	sum := sha256.Sum256([]byte(consentStyle))
	return "default-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) +
		"'; frame-ancestors 'none'; base-uri 'none'"
}()

var consentPage = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Allow {{.Client}}?</title><style>` + consentStyle + `</style></head>
<body><main>
<h1>Allow {{.Client}} to use {{.Door}}?</h1>
<p>You are signed in as <strong>{{.User}}</strong>.</p>
<p>{{.Client}} says it is an app. If you allow it, it can ask crews questions as you, and it will send you back to
<code>{{.RedirectHost}}</code>.{{if .ClientHost}} It is identified by <code>{{.ClientHost}}</code>.{{end}}
Allow it only if you started this from that app.</p>
<form method="post" action="` + pathConsent + `">
<input type="hidden" name="req" value="{{.Request}}"><input type="hidden" name="csrf" value="{{.CSRF}}">
<button class="allow" type="submit" name="decision" value="allow">Allow</button>
<button type="submit" name="decision" value="deny">Deny</button>
</form></main></body></html>
`))

type consentView struct {
	Client, Door, User, RedirectHost, ClientHost, Request, CSRF string
}

// page writes headers every page of the door carries: not cached, not framed (a hostile page
// could not make a click on Allow), no referrer, and a policy that allows nothing but its style.
func page(w http.ResponseWriter, status int) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", consentPolicy)
	w.WriteHeader(status)
}

func (d *door) showConsent(w http.ResponseWriter, blob string, req authRequest, s session) {
	csrf, err := d.newCSRF(s.Who.User, blob)
	if err != nil {
		http.Error(w, "the page could not be made, start again from the app", http.StatusInternalServerError)
		return
	}
	view := consentView{
		Client: appName(req), Door: d.cfg.displayName(), User: userLabel(s.Who), Request: blob, CSRF: csrf,
		RedirectHost: hostOf(req.Redirect), ClientHost: clientHost(req.ClientID),
	}
	page(w, http.StatusOK)
	_ = consentPage.Execute(w, view)
}

func appName(req authRequest) string {
	if req.ClientName != "" {
		return req.ClientName
	}
	return "An app"
}

func userLabel(who person) string {
	if who.Name != "" {
		return who.Name
	}
	return who.User
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}
	return ""
}

// clientHost is the host of a client id that is itself an address, so the person sees who published it.
func clientHost(clientID string) string {
	if u, err := url.Parse(clientID); err == nil && u.Scheme == schemeHTTPS {
		return u.Host
	}
	return ""
}

// consent receives the person's answer. It needs the signed-in person's cookie, a token made for
// that person and that one request, and a post that is not from another site.
func (d *door) consent(w http.ResponseWriter, r *http.Request) {
	if !d.sameOrigin(r) {
		http.Error(w, "this form was not submitted from this site", http.StatusForbidden)
		return
	}
	s, ok := d.session(r)
	if !ok {
		http.Error(w, "your sign-in ended, start again from the app", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form is not valid", http.StatusBadRequest)
		return
	}
	blob := r.PostForm.Get("req")
	var req authRequest
	if d.sealer.Open(purposeAuthReq, blob, &req) != nil || d.now().Unix() > req.Expires ||
		!d.validCSRF(r.PostForm.Get("csrf"), s.Who.User, blob) {
		d.event("consent refused", "reason", "token")
		http.Error(w, "this form is not valid or has expired, start again from the app", http.StatusForbidden)
		return
	}
	s = d.touchSession(w, s)
	switch r.PostForm.Get("decision") {
	case "allow":
		d.rememberConsent(w, r, s.Who.User, req)
		d.issueCode(w, r, req, s)
	case "deny":
		d.refuse(w, r, req, errAccessDenied, "the person did not allow this app")
	default:
		http.Error(w, "choose Allow or Deny", http.StatusBadRequest)
	}
}
