package accounts

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Environment the liaison reads. Nothing is kept unless EnvEnabled is "true".
const (
	EnvEnabled      = "LIAISON_ACCOUNTS_ENABLED"
	EnvSealKeysFile = "LIAISON_ACCOUNTS_SEAL_KEYS_FILE" // id:base64key lines, apart from the door's keys
	EnvRegistered   = "LIAISON_ACCOUNTS"                // JSON object of registrations by account name
)

var nameForm = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// Config is what the install supplies.
type Config struct {
	Enabled       bool
	SealKeys      string
	Registrations map[string]Registration
}

// registered is the JSON of one registration. A client secret is a mounted file, never a value.
type registered struct {
	ClientID         string   `json:"clientId"`
	ClientSecretFile string   `json:"clientSecretFile"`
	Issuer           string   `json:"issuer"`
	Hosts            []string `json:"hosts"`
	AllowedCrews     []string `json:"allowedCrews"`
	MaxLifetimeHours int      `json:"maxLifetimeHours"`
}

// ConfigFromEnv reads the settings. It returns a disabled, empty Config (and no error) when
// EnvEnabled is not "true". When enabled, missing seal keys, an account name that is not a
// DNS label, or a registration with no client id is an error that names it.
func ConfigFromEnv(getenv func(string) string, readFile func(string) ([]byte, error)) (Config, error) {
	if strings.TrimSpace(getenv(EnvEnabled)) != "true" {
		return Config{}, nil
	}
	if readFile == nil {
		readFile = os.ReadFile
	}
	cfg := Config{Enabled: true, Registrations: map[string]Registration{}}
	keyFile := strings.TrimSpace(getenv(EnvSealKeysFile))
	if keyFile == "" {
		return Config{}, fmt.Errorf("%s needs %s", EnvEnabled, EnvSealKeysFile)
	}
	keys, err := readFile(keyFile)
	if err != nil || strings.TrimSpace(string(keys)) == "" {
		return Config{}, fmt.Errorf("%s: %s is missing or empty", EnvSealKeysFile, keyFile)
	}
	cfg.SealKeys = string(keys)
	if raw := strings.TrimSpace(getenv(EnvRegistered)); raw != "" {
		if err := cfg.readRegistrations(raw, readFile); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

func (c *Config) readRegistrations(raw string, readFile func(string) ([]byte, error)) error {
	var in map[string]registered
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return fmt.Errorf("%s is not a JSON object of registrations: %w", EnvRegistered, err)
	}
	for name, r := range in {
		reg, err := r.build(name, readFile)
		if err != nil {
			return err
		}
		c.Registrations[name] = reg
	}
	return nil
}

func (r registered) build(name string, readFile func(string) ([]byte, error)) (Registration, error) {
	if !nameForm.MatchString(name) {
		return Registration{}, fmt.Errorf("%s: account name %q is not a DNS label", EnvRegistered, name)
	}
	if r.ClientID == "" {
		return Registration{}, fmt.Errorf("%s: account %q has no clientId", EnvRegistered, name)
	}
	if r.MaxLifetimeHours < 0 {
		return Registration{}, fmt.Errorf("%s: account %q has a negative maxLifetimeHours", EnvRegistered, name)
	}
	if err := r.checkTrust(name); err != nil {
		return Registration{}, err
	}
	reg := Registration{
		ClientID: r.ClientID, AllowedCrews: r.AllowedCrews, Issuer: r.Issuer, Hosts: r.Hosts,
		MaxLifetime: time.Duration(r.MaxLifetimeHours) * time.Hour,
	}
	if r.ClientSecretFile != "" {
		secret, err := readFile(r.ClientSecretFile)
		if err != nil || strings.TrimSpace(string(secret)) == "" {
			return Registration{}, fmt.Errorf("%s: account %q: the client secret file is missing or empty", EnvRegistered, name)
		}
		reg.ClientSecret = strings.TrimSpace(string(secret))
	}
	return reg, nil
}

// checkTrust refuses a registration that would send the install's client to an address a crew
// author chooses. A crew declares the endpoints, so something must say which crews and which
// hosts the install trusts: at least one of allowedCrews and hosts, and, when the registration
// holds a client secret, hosts. The secret goes to the token endpoint, and only a pinned host
// stops an allowed crew (or a compromised one in the same namespace) from pointing it elsewhere.
// Crews are named namespace/name: a bare name would match that name in every namespace.
func (r registered) checkTrust(name string) error {
	if len(r.AllowedCrews) == 0 && len(r.Hosts) == 0 {
		return fmt.Errorf("%s: account %q needs allowedCrews or hosts: without either, any crew author could send "+
			"the install's client to an address of their choosing", EnvRegistered, name)
	}
	if r.ClientSecretFile != "" && len(r.Hosts) == 0 {
		return fmt.Errorf("%s: account %q holds a client secret, so it needs hosts: the secret goes to the token endpoint",
			EnvRegistered, name)
	}
	for _, crew := range r.AllowedCrews {
		if ns, n, ok := strings.Cut(crew, "/"); !ok || ns == "" || n == "" || strings.Contains(n, "/") {
			return fmt.Errorf("%s: account %q: allowedCrews entry %q must be namespace/name", EnvRegistered, name, crew)
		}
	}
	return nil
}

// Allows reports whether the install offers the account to the crew ("namespace/name").
func (r Registration) Allows(crew string) bool {
	if len(r.AllowedCrews) == 0 {
		return true
	}
	return slices.Contains(r.AllowedCrews, crew)
}

// httpsPort is the only port an endpoint of a pinned account may use: the tool gateway sends
// account tokens to port 443 only, so a pin names hosts, never a port.
const httpsPort = "443"

// PinsAllow reports whether the declaration's endpoints are on the hosts the registration pins,
// on port 443 (the default, or written out). A registration that pins none allows any.
func (r Registration) PinsAllow(d Declaration) bool {
	if len(r.Hosts) == 0 {
		return true
	}
	// The hosts the access token may be sent to are pinned too, not only the endpoints.
	for _, h := range d.TokenHosts {
		if !slices.Contains(r.Hosts, h) {
			return false
		}
	}
	for _, raw := range []string{d.AuthorizeURL, d.TokenURL, d.RevocationURL} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || !slices.Contains(r.Hosts, u.Hostname()) || (u.Port() != "" && u.Port() != httpsPort) {
			return false
		}
	}
	return true
}

// AllowedAmong returns the crews the registration allows, in order.
func (r Registration) AllowedAmong(crews []string) []string {
	var out []string
	for _, c := range crews {
		if r.Allows(c) {
			out = append(out, c)
		}
	}
	return out
}
