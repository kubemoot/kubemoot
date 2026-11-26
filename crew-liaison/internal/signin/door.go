package signin

import (
	"net/http"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const pathConsent = "/oauth/consent"

// Purposes of the values only the door seals.
const (
	purposeDoorCode = "door-code"
	purposeRefresh  = "door-refresh"
	purposeSession  = "door-session"
	purposeConsent  = "door-consent"
	purposeCSRF     = "door-csrf"
)

// Door is the liaison's own authorization server for its /mcp resource. The person proves who
// they are at an identity provider the install names, and the door issues the chat app its own
// signed access token and a sealed refresh token. It keeps no record of a person: only keys, and a
// replay memory (process-local, or shared through the token store) that makes a code single use
// and a refresh token rotate.
type Door interface {
	// Handler serves the metadata documents and the /oauth routes.
	Handler() http.Handler
	// Verifier checks a door access token on /mcp.
	Verifier() identity.Verifier
	// HopSigner asserts a person to a crew's gateway; nil when no hop key is configured.
	HopSigner() *identity.HopSigner
	// ResourceMetadataURL is the address the 401 on /mcp names.
	ResourceMetadataURL() string
}

type door struct {
	cfg     *DoorConfig
	base    *front
	sealer  *Sealer
	now     func() time.Time
	keys    *tokenKeys
	idp     identityProvider
	fetcher *metadataFetcher
	hop     *identity.HopSigner
	replay  ReplaySet
	mux     *http.ServeMux
}

// NewDoor builds the door from cfg.
func NewDoor(cfg *DoorConfig) (Door, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.RefreshMaxAge <= 0 {
		cfg.RefreshMaxAge = defaultRefreshMax
	}
	return newDoor(cfg, newOIDCProvider(cfg))
}

func newDoor(cfg *DoorConfig, idp identityProvider) (*door, error) {
	base, err := newFront(&Config{
		PublicURL: cfg.PublicURL, SealKeys: cfg.SealKeys, AllowedRedirectHosts: cfg.AllowedRedirectHosts,
		Logger: cfg.Logger, HTTPClient: cfg.HTTPClient, Now: cfg.Now,
	})
	if err != nil {
		return nil, err
	}
	signing, err := ParseSigningKeys(cfg.SigningKeys)
	if err != nil {
		return nil, err
	}
	keys, err := newTokenKeys(signing)
	if err != nil {
		return nil, err
	}
	d := &door{
		cfg: cfg, base: base, sealer: base.sealer, now: base.now, keys: keys, idp: idp,
		replay: cfg.Replay, fetcher: newMetadataFetcher(cfg),
	}
	if d.replay == nil {
		d.replay = newReplayCache()
	}
	if err := d.loadHop(); err != nil {
		return nil, err
	}
	if len(cfg.AllowedRedirectHosts) == 0 {
		cfg.Logger.Info("sign-in allows redirect addresses on any host; set " + EnvRedirectHosts + " on a shared deployment")
	}
	d.routes()
	return d, nil
}

func (d *door) loadHop() error {
	if d.cfg.HopKeys == "" {
		return nil
	}
	hopKeys, err := ParseSigningKeys(d.cfg.HopKeys)
	if err != nil {
		return err
	}
	d.hop, err = identity.NewHopSigner(hopKeys[0], hopIssuer, d.now)
	return err
}

func (d *door) routes() {
	d.mux = http.NewServeMux()
	d.mux.Handle("GET "+pathProtectedResource, protectedResourceHandler(d.cfg.PublicURL))
	d.mux.HandleFunc("GET "+pathAuthServer, d.serveMetadata)
	d.mux.HandleFunc("POST "+pathRegister, d.base.register)
	d.mux.HandleFunc("GET "+pathAuthorize, d.authorize)
	d.mux.HandleFunc("GET "+pathCallback, d.callback)
	d.mux.HandleFunc("POST "+pathConsent, d.consent)
	d.mux.HandleFunc("POST "+pathToken, d.token)
}

func (d *door) Handler() http.Handler          { return d.mux }
func (d *door) Verifier() identity.Verifier    { return d }
func (d *door) HopSigner() *identity.HopSigner { return d.hop }
func (d *door) ResourceMetadataURL() string    { return ResourceMetadataURL(d.cfg.PublicURL) }
func (d *door) event(name string, kv ...any)   { d.cfg.Logger.Info(name, kv...) }

func (d *door) serveMetadata(w http.ResponseWriter, _ *http.Request) {
	base := d.cfg.PublicURL
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, authServerMetadata{
		Issuer:                        base,
		AuthorizationEndpoint:         base + pathAuthorize,
		TokenEndpoint:                 base + pathToken,
		RegistrationEndpoint:          base + pathRegister,
		ResponseTypesSupported:        []string{responseCode},
		GrantTypesSupported:           []string{grantAuthCode, grantRefresh},
		CodeChallengeMethodsSupported: []string{methodS256},
		TokenEndpointAuthMethods:      []string{authMethodNone},
		IssParameterSupported:         true,
		ClientIDMetadataDocuments:     true,
	})
}
