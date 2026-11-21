package signin

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
)

// Environment the front reads. It is off unless EnvUpstream is set.
const (
	EnvUpstream      = "LIAISON_SIGNIN_UPSTREAM"               // the upstream authorization endpoint
	EnvUpstreamToken = "LIAISON_SIGNIN_UPSTREAM_TOKEN_URL"     // the upstream token endpoint
	EnvUpstreamID    = "LIAISON_SIGNIN_UPSTREAM_CLIENT_ID"     // the front's own registered client id
	EnvUpstreamScope = "LIAISON_SIGNIN_UPSTREAM_SCOPES"        // default "openid offline_access"
	EnvPublicURL     = "LIAISON_SIGNIN_PUBLIC_URL"             // the liaison's public base URL
	EnvSealKeys      = "LIAISON_SIGNIN_SEAL_KEYS"              // "id:base64key" entries
	EnvSealKeysFile  = "LIAISON_SIGNIN_SEAL_KEYS_FILE"         // a mounted Secret file, same form
	EnvRedirectHosts = "LIAISON_SIGNIN_ALLOWED_REDIRECT_HOSTS" // comma separated, optional
)

const defaultScopes = "openid offline_access"

// Config is everything the front needs.
type Config struct {
	PublicURL            string
	UpstreamAuthorizeURL string
	UpstreamTokenURL     string
	UpstreamClientID     string
	UpstreamScopes       string
	SealKeys             string
	AllowedRedirectHosts []string

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
