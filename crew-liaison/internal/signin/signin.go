// Package signin is the liaison's stateless sign-in front. To a chat app the liaison is
// the authorization server for its MCP door. Behind that, the front sends the person to an
// upstream sign-in without ever forwarding the app's `resource`, and hands the upstream's
// own token back to the app unchanged. Nothing is stored: client ids, state and codes are
// sealed (encrypted) strings, so two replicas behave as one and a restart loses nothing.
//
// No token or code is ever logged; the logger receives event names and error kinds only.
package signin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// SignIn is the seam: anything that answers the OAuth routes for the liaison's door can
// replace this implementation.
type SignIn interface {
	// Handler serves the metadata documents and the /oauth routes.
	Handler() http.Handler
}

// OAuth vocabulary the front reads and writes.
const (
	grantAuthCode        = "authorization_code"
	grantRefresh         = "refresh_token"
	methodS256           = "S256"
	paramClientID        = "client_id"
	paramCode            = "code"
	paramGrantType       = "grant_type"
	paramChallengeMethod = "code_challenge_method"
	paramState           = "state"
	paramRedirect        = "redirect_uri"
	paramResponseType    = "response_type"
	keyRedirectURIs      = "redirect_uris"
	responseCode         = "code"
	authMethodNone       = "none"
	schemeHTTPS          = "https"
)

const (
	pathProtectedResource = "/.well-known/oauth-protected-resource/mcp"
	pathAuthServer        = "/.well-known/oauth-authorization-server"
	pathRegister          = "/oauth/register"
	pathAuthorize         = "/oauth/authorize"
	pathCallback          = "/oauth/callback"
	pathToken             = "/oauth/token"

	stateLifetime = 10 * time.Minute
	codeLifetime  = 60 * time.Second
	maxBody       = 64 << 10
	maxUpstream   = 1 << 20
	upstreamWait  = 15 * time.Second
)

// ResourceMetadataURL is the address the 401 challenge on /mcp names, for a front whose
// public base URL is publicURL.
func ResourceMetadataURL(publicURL string) string { return publicURL + pathProtectedResource }

type front struct {
	cfg    *Config
	sealer *Sealer
	now    func() time.Time
	http   *http.Client
	mux    *http.ServeMux
}

// New builds the front from cfg.
func New(cfg *Config) (SignIn, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	sealer, err := ParseKeys(cfg.SealKeys)
	if err != nil {
		return nil, err
	}
	if len(cfg.AllowedRedirectHosts) == 0 {
		cfg.Logger.Info("sign-in allows redirect addresses on any host; set " + EnvRedirectHosts + " on a shared deployment")
	}
	f := &front{cfg: cfg, sealer: sealer, now: cfg.Now, http: cfg.HTTPClient}
	if f.now == nil {
		f.now = time.Now
	}
	if f.http == nil {
		f.http = &http.Client{
			Timeout:       upstreamWait,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	f.mux = http.NewServeMux()
	f.mux.Handle("GET "+pathProtectedResource, protectedResourceHandler(cfg.PublicURL))
	f.mux.HandleFunc("GET "+pathAuthServer, f.serveAuthServerMetadata)
	f.mux.HandleFunc("POST "+pathRegister, f.register)
	f.mux.HandleFunc("GET "+pathAuthorize, f.authorize)
	f.mux.HandleFunc("GET "+pathCallback, f.callback)
	f.mux.HandleFunc("POST "+pathToken, f.token)
	return f, nil
}

func (f *front) Handler() http.Handler { return f.mux }

// event logs what happened, never a value from the request or the upstream.
func (f *front) event(name string, kv ...any) { f.cfg.Logger.Info(name, kv...) }

// oauthError is the JSON error body of RFC 6749 section 5.2.
type oauthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, oauthError{Code: code, Description: description})
}

// redirectWith sends the browser to base with params added to its query.
func redirectWith(w http.ResponseWriter, r *http.Request, base string, params url.Values) {
	u, err := url.Parse(base)
	if err != nil {
		http.Error(w, "invalid redirect", http.StatusBadRequest)
		return
	}
	q := u.Query()
	for k, vs := range params {
		for _, v := range vs {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, u.String(), http.StatusFound)
}
