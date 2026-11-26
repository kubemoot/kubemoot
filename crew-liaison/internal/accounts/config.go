package accounts

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
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
	reg := Registration{
		ClientID: r.ClientID, AllowedCrews: r.AllowedCrews, Issuer: r.Issuer,
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

// Allows reports whether the install offers the account to the crew ("namespace/name").
func (r Registration) Allows(crew string) bool {
	if len(r.AllowedCrews) == 0 {
		return true
	}
	_, name, _ := strings.Cut(crew, "/")
	for _, allowed := range r.AllowedCrews {
		if allowed == crew || allowed == name {
			return true
		}
	}
	return false
}
