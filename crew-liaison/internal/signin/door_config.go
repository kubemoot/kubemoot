package signin

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
)

// Environment the door's own identity reads. The door is off unless EnvIdentity is set; the
// public address, seal keys and redirect hosts are the same settings the upstream front reads.
const (
	EnvIdentity        = "LIAISON_SIGNIN_IDENTITY"       // "oidc" turns the door's own identity on
	EnvOIDCIssuer      = "LIAISON_SIGNIN_OIDC_ISSUER"    // the OpenID Connect issuer URL
	EnvOIDCClientID    = "LIAISON_SIGNIN_OIDC_CLIENT_ID" // the door's client id at the issuer
	EnvOIDCSecretFile  = "LIAISON_SIGNIN_OIDC_CLIENT_SECRET_FILE"
	EnvOIDCScopes      = "LIAISON_SIGNIN_OIDC_SCOPES"       // default "openid profile"
	EnvOIDCGroups      = "LIAISON_SIGNIN_OIDC_GROUPS_CLAIM" // default "groups"
	EnvOIDCUserPrefix  = "LIAISON_SIGNIN_OIDC_USER_PREFIX"  // default "oidc:"
	EnvSigningKeysFile = "LIAISON_SIGNIN_SIGNING_KEYS_FILE" // PEM, first key signs access tokens
	EnvHopKeysFile     = "LIAISON_SIGNIN_HOP_KEYS_FILE"     // PEM, first key signs hop assertions
	EnvRefreshMaxHours = "LIAISON_SIGNIN_REFRESH_MAX_HOURS" // absolute life of a sign-in, default 168
)

// IdentityOIDC is the one identity provider this version has: any standard OpenID Connect
// issuer the install names. The person signing in is the person asking.
const IdentityOIDC = "oidc"

const (
	defaultOIDCScopes     = "openid profile"
	defaultGroupsClaim    = "groups"
	defaultUserPrefix     = "oidc:"
	defaultRefreshMax     = 168 * time.Hour
	maxRefreshMaxHours    = 24 * 90
	accessTokenLifetime   = 15 * time.Minute
	refreshIdleLifetime   = 30 * 24 * time.Hour
	hopIssuer             = "liaison"
	resourcePath          = "/mcp"
	maxClientSecretLength = 1024
)

// OIDCConfig is the issuer the person signs in at.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	Scopes       string
	GroupsClaim  string
	UserPrefix   string
}

// DoorConfig is everything the door's own identity needs.
type DoorConfig struct {
	PublicURL            string
	SealKeys             string
	AllowedRedirectHosts []string
	OIDC                 OIDCConfig
	SigningKeys          string // PEM, see ParseSigningKeys
	HopKeys              string // PEM; empty means the door asserts nobody to a crew
	RefreshMaxAge        time.Duration

	DisplayName string // what the consent page calls the door; "Kubemoot" by default

	Logger     logr.Logger
	HTTPClient *http.Client     // for the issuer's endpoints; nil uses the default client
	TLSConfig  *tls.Config      // trust for fetching a client's metadata document; nil is the system's roots
	Now        func() time.Time // the clock; time.Now by default

	// The fetch of a client metadata document, for tests: how names resolve, which addresses may
	// be dialled, how long a fetch may take and how many may come at once. Zero values are the
	// system resolver, public addresses only, five seconds and a burst of ten.
	fetchLookup  func(ctx context.Context, host string) ([]netip.Addr, error)
	fetchAllow   func(netip.Addr) bool
	fetchTimeout time.Duration
	fetchBurst   int
}

// DoorConfigFromEnv reads the door's settings. It returns nil, nil when the door's identity is
// not enabled (EnvIdentity empty), and an error when it is enabled but incomplete or unsafe.
func DoorConfigFromEnv(getenv func(string) string, readFile func(string) ([]byte, error)) (*DoorConfig, error) {
	kind := getenv(EnvIdentity)
	if kind == "" {
		return nil, nil
	}
	if kind != IdentityOIDC {
		return nil, fmt.Errorf("%s must be %q, got %q", EnvIdentity, IdentityOIDC, kind)
	}
	if getenv(EnvUpstream) != "" {
		return nil, fmt.Errorf("%s and %s are alternatives: the door signs people in itself or relays one service's sign-in",
			EnvIdentity, EnvUpstream)
	}
	if readFile == nil {
		readFile = os.ReadFile
	}
	cfg := &DoorConfig{
		PublicURL:            strings.TrimRight(getenv(EnvPublicURL), "/"),
		SealKeys:             getenv(EnvSealKeys),
		AllowedRedirectHosts: splitList(getenv(EnvRedirectHosts)),
		OIDC: OIDCConfig{
			Issuer:      getenv(EnvOIDCIssuer),
			ClientID:    getenv(EnvOIDCClientID),
			Scopes:      orDefault(getenv(EnvOIDCScopes), defaultOIDCScopes),
			GroupsClaim: orDefault(getenv(EnvOIDCGroups), defaultGroupsClaim),
			UserPrefix:  orDefault(getenv(EnvOIDCUserPrefix), defaultUserPrefix),
		},
		RefreshMaxAge: defaultRefreshMax,
	}
	if err := cfg.readFiles(getenv, readFile); err != nil {
		return nil, err
	}
	if err := cfg.readRefreshAge(getenv(EnvRefreshMaxHours)); err != nil {
		return nil, err
	}
	return cfg, cfg.validate()
}

// readRefreshAge sets the absolute life of a sign-in from a number of hours, "" keeping the default.
func (c *DoorConfig) readRefreshAge(hours string) error {
	if hours == "" {
		return nil
	}
	age, err := parseHours(hours)
	if err != nil {
		return fmt.Errorf("%s: %w", EnvRefreshMaxHours, err)
	}
	c.RefreshMaxAge = age
	return nil
}

// readFiles loads the settings that are mounted Secret files.
func (c *DoorConfig) readFiles(getenv func(string) string, readFile func(string) ([]byte, error)) error {
	files := map[string]*string{
		EnvSealKeysFile:    &c.SealKeys,
		EnvSigningKeysFile: &c.SigningKeys,
		EnvHopKeysFile:     &c.HopKeys,
		EnvOIDCSecretFile:  &c.OIDC.ClientSecret,
	}
	for env, dst := range files {
		path := getenv(env)
		if path == "" {
			continue
		}
		data, err := readFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", env, err)
		}
		*dst = string(data)
	}
	c.OIDC.ClientSecret = strings.TrimSpace(c.OIDC.ClientSecret)
	return nil
}

func parseHours(s string) (time.Duration, error) {
	hours, err := strconv.Atoi(s)
	if err != nil || hours < 1 || hours > maxRefreshMaxHours {
		return 0, fmt.Errorf("must be a whole number of hours from 1 to %d", maxRefreshMaxHours)
	}
	return time.Duration(hours) * time.Hour, nil
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func (c *DoorConfig) validate() error {
	if c.SealKeys == "" {
		return fmt.Errorf("%s or %s is required when the door's identity is enabled", EnvSealKeys, EnvSealKeysFile)
	}
	if c.SigningKeys == "" {
		return fmt.Errorf("%s is required when the door's identity is enabled", EnvSigningKeysFile)
	}
	if c.OIDC.ClientID == "" || c.OIDC.ClientSecret == "" || len(c.OIDC.ClientSecret) > maxClientSecretLength {
		return fmt.Errorf("%s and %s are required when the door's identity is enabled", EnvOIDCClientID, EnvOIDCSecretFile)
	}
	return c.validateURLs()
}

// validateURLs checks the two addresses: both https (http only on loopback), and the door's own
// with no path or query, since it is the issuer of its tokens and the origin its forms post to.
func (c *DoorConfig) validateURLs() error {
	for name, raw := range map[string]string{EnvPublicURL: c.PublicURL, EnvOIDCIssuer: c.OIDC.Issuer} {
		if err := checkURL(raw); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if u, _ := validWebURL(c.PublicURL); u.Path != "" || u.RawQuery != "" {
		return errors.New(EnvPublicURL + " must be an address with no path or query: the door's issuer and origin")
	}
	return nil
}

// displayName is what the consent page calls the door.
func (c *DoorConfig) displayName() string { return orDefault(c.DisplayName, "Kubemoot") }

// Resource is the canonical address of the door's MCP resource: the audience of its tokens.
func (c *DoorConfig) Resource() string { return c.PublicURL + resourcePath }
