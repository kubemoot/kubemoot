package signin

import (
	"html/template"
	"net/http"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/accounts"
)

type connectView struct {
	Door, User, Service, Purpose, Request, CSRF, Action, Retention string
	Scopes, Crews                                                  []string
}

func (d *door) connectView(s session, o accounts.Offer, blob, csrf string) connectView {
	return connectView{
		Door: d.cfg.displayName(), User: userLabel(s.Who), Service: o.Declaration.DisplayName,
		Purpose: o.Declaration.Purpose, Scopes: o.Declaration.Scopes, Crews: o.Crews,
		Request: blob, CSRF: csrf, Action: pathConnect + o.Declaration.Name,
		Retention: "a sealed token, until you disconnect, until you sign out of " + d.cfg.displayName() +
			", after 30 days unused, or when " + o.Declaration.DisplayName + " ends it",
	}
}

var connectTemplate = template.Must(template.New("connect").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Connect {{.Service}}</title><style>` + consentStyle + `</style></head>
<body><main>
<h1>Connect your {{.Service}} account to {{.Door}}?</h1>
<p>You are signed in as <strong>{{.User}}</strong>.</p>
{{if .Purpose}}<p>{{.Purpose}}</p>{{end}}
<p>{{.Door}} will use your account only when you ask, for these crews:</p>
<ul>{{range .Crews}}<li><code>{{.}}</code></li>{{end}}</ul>
{{if .Scopes}}<p>It asks {{.Service}} for:
{{range $i, $s := .Scopes}}{{if $i}}, {{end}}<code>{{$s}}</code>{{end}}.</p>{{end}}
<p>{{.Door}} keeps {{.Retention}}. You can disconnect at any time on your accounts page.</p>
<form method="post" action="{{.Action}}">
<input type="hidden" name="req" value="{{.Request}}"><input type="hidden" name="csrf" value="{{.CSRF}}">
<button class="allow" type="submit">Connect</button>
</form></main></body></html>
`))

type accountRowView struct {
	Account, Name, Purpose, State, CSRF, ConnectURL string
	Crews                                           []string
	Disconnect                                      bool
}

type accountsView struct {
	Door, User, SignOutCSRF string
	Accounts                []accountRowView
}

var accountsTemplate = template.Must(template.New("accounts").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Your accounts</title><style>` + consentStyle + `</style></head>
<body><main>
<h1>Your accounts on {{.Door}}</h1>
<p>You are signed in as <strong>{{.User}}</strong>.</p>
{{range .Accounts}}<section><h2>{{.Name}}</h2>
<p>{{.State}}.{{if .Purpose}} {{.Purpose}}{{end}}</p>
{{if .Disconnect}}<form method="post" action="` + pathDisconnect + `">
<input type="hidden" name="account" value="{{.Account}}"><input type="hidden" name="csrf" value="{{.CSRF}}">
<button type="submit">Disconnect</button></form>{{end}}
{{if .ConnectURL}}<p><a href="{{.ConnectURL}}">{{if .Disconnect}}Connect again{{else}}Connect{{end}}</a></p>{{end}}
</section>{{else}}<p>No crew you can use needs an account of yours.</p>{{end}}
{{if .SignOutCSRF}}<form method="post" action="` + pathSignOut + `">
<input type="hidden" name="csrf" value="{{.SignOutCSRF}}">
<button type="submit">Sign out and delete everything kept</button></form>{{end}}
</main></body></html>
`))

var noticeTemplate = template.Must(template.New("notice").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}}</title><style>` + consentStyle + `</style></head>
<body><main><h1>{{.Title}}</h1><p>{{.Message}}</p></main></body></html>
`))

// notice shows a plain result page. Nothing on it is a token, an address with a credential, or
// text from a service.
func (d *door) notice(w http.ResponseWriter, status int, title, message string) {
	page(w, status)
	_ = noticeTemplate.Execute(w, struct{ Title, Message string }{title, message})
}
