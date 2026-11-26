// Package signin is the liaison's sign-in front. To a chat app the liaison is the
// authorization server for its MCP door, and the tokens the app holds are the door's own:
// issued by the liaison, for the liaison, and good nowhere else. Behind that, the front sends
// the person to an upstream sign-in without ever forwarding the app's `resource`, exchanges
// the upstream's code itself, and keeps the upstream's token for that person in the token
// store, sealed. The upstream's token never reaches the chat app, and a token the upstream
// issued is never accepted at the door.
//
// Client ids, state, codes and door tokens are sealed (encrypted) strings. What the front
// keeps is in the token store: one record per person for the upstream account, one per
// sign-in of an app, each with its own time to live.
//
// No token or code is ever logged; the logger receives event names and error kinds only.
package signin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// SignIn is the seam: anything that answers the OAuth routes for the liaison's door and
// knows the tokens it issued can replace this implementation.
type SignIn interface {
	// Handler serves the metadata documents and the /oauth routes.
	Handler() http.Handler
	// Resolve reads a bearer token presented at the door. For a door token it returns the
	// person and the upstream token kept for them, renewing that token when it is due. A
	// bearer that is not a door token is identity.ErrNoCredential; a door token that is
	// expired, was issued for another door, or whose person has no upstream token any more
	// is identity.ErrInvalid; any other error means the check could not be made.
	Resolve(r *http.Request, bearer string) (identity.Caller, string, error)
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
	paramResource        = "resource"
	paramToken           = "token"
	errInvalidTarget     = "invalid_target"
	errInvalidGrant      = "invalid_grant"
	errServerError       = "server_error"
	errAccessDenied      = "access_denied"
	errUnavailable       = "temporarily_unavailable"
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
	pathRevoke            = "/oauth/revoke"
	pathDoor              = "/mcp"

	stateLifetime = 10 * time.Minute
	codeLifetime  = 60 * time.Second
	maxBody       = 64 << 10
	maxUpstream   = 1 << 20
	upstreamWait  = 15 * time.Second
)

// errNoIdentifier is returned by New when nothing can name the person who signed in.
var errNoIdentifier = errors.New("sign-in needs a verifier that names the person the upstream signed in")

// doorResource is the canonical address of the door: the audience of every token the front
// issues and the only `resource` an app may ask for.
func (f *front) doorResource() string { return f.cfg.PublicURL + pathDoor }

// ResourceMetadataURL is the address the 401 challenge on /mcp names, for a front whose
// public base URL is publicURL.
func ResourceMetadataURL(publicURL string) string { return publicURL + pathProtectedResource }

type front struct {
	cfg    *Config
	sealer *Sealer
	store  tokenstore.Store
	now    func() time.Time
	http   *http.Client
	mux    *http.ServeMux
	// renewals makes concurrent requests of one person share one renewal at the upstream.
	renewals singleflight.Group
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
	if cfg.Identify == nil {
		return nil, errNoIdentifier
	}
	f := &front{cfg: cfg, sealer: sealer, store: cfg.Store, now: cfg.Now, http: cfg.HTTPClient}
	if f.now == nil {
		f.now = time.Now
	}
	if f.store == nil {
		f.store = tokenstore.NewMemory(f.now)
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
	f.mux.HandleFunc("POST "+pathRevoke, f.revoke)
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
