package signin

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/go-logr/logr"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// Environment the front reads. It is off unless EnvUpstream is set.
const (
	EnvUpstream      = "LIAISON_SIGNIN_UPSTREAM"                // the upstream authorization endpoint
	EnvUpstreamToken = "LIAISON_SIGNIN_UPSTREAM_TOKEN_URL"      // the upstream token endpoint
	EnvUpstreamID    = "LIAISON_SIGNIN_UPSTREAM_CLIENT_ID"      // the front's own registered client id
	EnvUpstreamScope = "LIAISON_SIGNIN_UPSTREAM_SCOPES"         // default "openid offline_access"
	EnvUpstreamName  = "LIAISON_SIGNIN_UPSTREAM_NAME"           // the account's name in the store, default "upstream"
	EnvUpstreamKeep  = "LIAISON_SIGNIN_UPSTREAM_TOKEN_LIFETIME" // the longest the upstream lets a sign-in last, optional
	EnvPublicURL     = "LIAISON_SIGNIN_PUBLIC_URL"              // the liaison's public base URL
	EnvSealKeys      = "LIAISON_SIGNIN_SEAL_KEYS"               // "id:base64key" entries
	EnvSealKeysFile  = "LIAISON_SIGNIN_SEAL_KEYS_FILE"          // a mounted Secret file, same form
	EnvRedirectHosts = "LIAISON_SIGNIN_ALLOWED_REDIRECT_HOSTS"  // comma separated, optional
)

const (
	defaultScopes       = "openid offline_access"
	defaultUpstreamName = "upstream"
)

// upstreamNameForm is a DNS label: the name is part of a store key.
var upstreamNameForm = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Identifier names the person a token of the upstream belongs to, by the door's own
// verifiers. r is the request the token arrived with or was looked up for; only its
// context and its peer address are used. A token that names nobody is identity.ErrInvalid.
type Identifier func(r *http.Request, token string) (identity.Caller, error)

// Config is everything the front needs.
type Config struct {
	PublicURL            string
	UpstreamAuthorizeURL string
	UpstreamTokenURL     string
	UpstreamClientID     string
	UpstreamScopes       string
	// UpstreamName names the upstream account in the token store ("upstream" when empty).
	UpstreamName string
	// UpstreamTokenLifetime is the longest the upstream lets one sign-in last, counted from
	// the sign-in. A person's token is never kept past it. Zero means the upstream's own
	// answer decides, and a token it stops accepting is deleted when that is found.
	UpstreamTokenLifetime time.Duration
	SealKeys              string
	AllowedRedirectHosts  []string

	// Identify names the person an upstream token belongs to. Required.
	Identify Identifier
	// Store keeps each person's upstream token and door sign-in, sealed. Nil is a store in
	// this process's memory.
	Store tokenstore.Store

	Logger     logr.Logger
	HTTPClient *http.Client     // for the upstream token endpoint; the default follows no redirects
	Now        func() time.Time // the clock; time.Now by default
}

// ConfigFromEnv reads the front's settings. It returns nil, nil when the front is not
// enabled (EnvUpstream empty), and an error when it is enabled but incomplete or unsafe.
func ConfigFromEnv(getenv func(string) string, readFile func(string) ([]byte, error)) (*Config, error) {
	authorize := getenv(EnvUpstream)
	if authorize == "" {
		return nil, nil
	}
	if readFile == nil {
		readFile = os.ReadFile
	}
	cfg := &Config{
		PublicURL:            strings.TrimRight(getenv(EnvPublicURL), "/"),
		UpstreamAuthorizeURL: authorize,
		UpstreamTokenURL:     getenv(EnvUpstreamToken),
		UpstreamClientID:     getenv(EnvUpstreamID),
		UpstreamScopes:       getenv(EnvUpstreamScope),
		UpstreamName:         getenv(EnvUpstreamName),
		SealKeys:             getenv(EnvSealKeys),
		AllowedRedirectHosts: splitList(getenv(EnvRedirectHosts)),
	}
	if path := getenv(EnvSealKeysFile); path != "" {
		data, err := readFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", EnvSealKeysFile, err)
		}
		cfg.SealKeys = string(data)
	}
	if raw := getenv(EnvUpstreamKeep); raw != "" {
		keep, err := time.ParseDuration(raw)
		if err != nil || keep <= 0 {
			return nil, fmt.Errorf("%s must be a duration above zero, such as 720h", EnvUpstreamKeep)
		}
		cfg.UpstreamTokenLifetime = keep
	}
	return cfg, cfg.validate()
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (c *Config) validate() error {
	if c.UpstreamName == "" {
		c.UpstreamName = defaultUpstreamName
	}
	if !upstreamNameForm.MatchString(c.UpstreamName) {
		return fmt.Errorf("%s must be a DNS label (lower case letters, digits and \"-\")", EnvUpstreamName)
	}
	if c.UpstreamClientID == "" {
		return fmt.Errorf("%s is required when sign-in is enabled", EnvUpstreamID)
	}
	if c.SealKeys == "" {
		return fmt.Errorf("%s or %s is required when sign-in is enabled", EnvSealKeys, EnvSealKeysFile)
	}
	for name, raw := range map[string]string{
		EnvPublicURL: c.PublicURL, EnvUpstream: c.UpstreamAuthorizeURL, EnvUpstreamToken: c.UpstreamTokenURL,
	} {
		if err := checkURL(raw); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// checkURL accepts an absolute https URL, or http only for a loopback host.
func checkURL(raw string) error {
	if raw == "" {
		return errors.New("is required when sign-in is enabled")
	}
	if _, ok := validWebURL(raw); !ok {
		return errors.New("must be an absolute https URL (http only for localhost) without a fragment or credentials")
	}
	return nil
}

// validWebURL is the one rule for every URL the front is given or sends people to: absolute,
// with a host, no fragment, no credentials, bounded length, and https (http only on loopback).
func validWebURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.Fragment != "" || u.User != nil || len(raw) > 2048 {
		return nil, false
	}
	if u.Scheme == schemeHTTPS || (u.Scheme == "http" && isLoopback(u.Hostname())) {
		return u, true
	}
	return nil, false
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
