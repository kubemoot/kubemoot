// Package natsconn is the one set of NATS connection options every Kubemoot component reads
// the same way: the URL (tls:// accepted), a CA bundle, an optional client certificate and key
// for mutual TLS, and one kind of NATS credential (a creds file, a user and password, or a
// token). With none of the options set, a component connects as it always has: plainly and
// anonymously.
//
// The names are the contract; the Java and TypeScript components read the same ones.
package natsconn

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"

	"github.com/nats-io/nats.go"
)

// Environment variable names. File variables hold a path; the path is where a Secret or
// ConfigMap is mounted.
const (
	EnvURL            = "NATS_URL"
	EnvCAFile         = "NATS_CA_FILE"
	EnvClientCertFile = "NATS_CLIENT_CERT_FILE"
	EnvClientKeyFile  = "NATS_CLIENT_KEY_FILE"
	EnvCredsFile      = "NATS_CREDS_FILE"
	EnvUser           = "NATS_USER"
	EnvPassword       = "NATS_PASSWORD"
	EnvToken          = "NATS_TOKEN"
)

// Config is a component's NATS connection settings. The zero value is a plain, anonymous
// connection.
type Config struct {
	URL            string
	CAFile         string
	ClientCertFile string
	ClientKeyFile  string
	CredsFile      string
	User           string
	Password       string
	Token          string
}

// FromEnv reads the settings through getenv (os.Getenv in a binary, a map lookup in a test).
// Blank values are absent.
func FromEnv(getenv func(string) string) Config {
	get := func(name string) string { return strings.TrimSpace(getenv(name)) }
	return Config{
		URL:            get(EnvURL),
		CAFile:         get(EnvCAFile),
		ClientCertFile: get(EnvClientCertFile),
		ClientKeyFile:  get(EnvClientKeyFile),
		CredsFile:      get(EnvCredsFile),
		User:           get(EnvUser),
		Password:       getenv(EnvPassword),
		Token:          get(EnvToken),
	}
}

// HasAuth reports whether any credential is set.
func (c Config) HasAuth() bool {
	return c.CredsFile != "" || c.User != "" || c.Token != ""
}

// ClearAuth drops every credential, for a caller that is about to supply its own.
func (c *Config) ClearAuth() {
	c.CredsFile, c.User, c.Password, c.Token = "", "", "", ""
}

// Validate reports a combination that cannot work: a client certificate without its key, a
// password without a user, or more than one kind of credential. It does not read any file.
func (c Config) Validate() error {
	if (c.ClientCertFile == "") != (c.ClientKeyFile == "") {
		return fmt.Errorf("%s and %s must be set together", EnvClientCertFile, EnvClientKeyFile)
	}
	if c.User != "" && c.Password == "" {
		return fmt.Errorf("%s is set without %s", EnvUser, EnvPassword)
	}
	if c.User == "" && c.Password != "" {
		return fmt.Errorf("%s is set without %s", EnvPassword, EnvUser)
	}
	kinds := 0
	for _, set := range []bool{c.CredsFile != "", c.User != "", c.Token != ""} {
		if set {
			kinds++
		}
	}
	if kinds > 1 {
		return fmt.Errorf("set only one of %s, %s with %s, or %s", EnvCredsFile, EnvUser, EnvPassword, EnvToken)
	}
	return nil
}

// Options turns the settings into nats.go options. It reads the CA bundle, the client
// certificate and the creds file now, so a missing or unreadable file fails at startup with
// the variable that names it instead of as a bare handshake error later.
func (c Config) Options() ([]nats.Option, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	var opts []nats.Option
	if c.CAFile != "" {
		if err := checkCA(c.CAFile); err != nil {
			return nil, err
		}
		opts = append(opts, nats.RootCAs(c.CAFile))
	}
	if c.ClientCertFile != "" {
		for _, f := range [][2]string{{EnvClientCertFile, c.ClientCertFile}, {EnvClientKeyFile, c.ClientKeyFile}} {
			if err := checkReadable(f[0], f[1]); err != nil {
				return nil, err
			}
		}
		if _, err := tls.LoadX509KeyPair(c.ClientCertFile, c.ClientKeyFile); err != nil {
			return nil, fmt.Errorf("%s and %s: %w", EnvClientCertFile, EnvClientKeyFile, err)
		}
		opts = append(opts, nats.ClientCert(c.ClientCertFile, c.ClientKeyFile))
	}
	return c.authOptions(opts)
}

func (c Config) authOptions(opts []nats.Option) ([]nats.Option, error) {
	switch {
	case c.CredsFile != "":
		if err := checkReadable(EnvCredsFile, c.CredsFile); err != nil {
			return nil, err
		}
		opts = append(opts, nats.UserCredentials(c.CredsFile))
	case c.User != "":
		opts = append(opts, nats.UserInfo(c.User, c.Password))
	case c.Token != "":
		opts = append(opts, nats.Token(c.Token))
	}
	return opts, nil
}

func checkReadable(env, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s: %w", env, err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return fmt.Errorf("%s: %s is empty", env, path)
	}
	return nil
}

func checkCA(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s: %w", EnvCAFile, err)
	}
	if !x509.NewCertPool().AppendCertsFromPEM(b) {
		return fmt.Errorf("%s: %s holds no PEM certificate", EnvCAFile, path)
	}
	return nil
}

// Connect dials Config.URL with the settings and any extra options (name, reconnect policy,
// handlers). A failure names the URL and the settings in play, so a missing CA or credential
// reads as that and not as a bare handshake error.
func (c Config) Connect(extra ...nats.Option) (*nats.Conn, error) {
	opts, err := c.Options()
	if err != nil {
		return nil, err
	}
	conn, err := nats.Connect(c.URL, append(opts, extra...)...)
	if err != nil {
		return nil, c.explain(err)
	}
	return conn, nil
}

// explain wraps a connection error with the settings that were in play.
func (c Config) explain(err error) error {
	return fmt.Errorf("connecting to NATS at %s (%s): %w", c.URL, c.Describe(), err)
}

// Describe names what is configured, never a secret value.
func (c Config) Describe() string {
	parts := []string{}
	if c.CAFile != "" {
		parts = append(parts, "ca "+c.CAFile)
	}
	if c.ClientCertFile != "" {
		parts = append(parts, "client certificate")
	}
	switch {
	case c.CredsFile != "":
		parts = append(parts, "creds file")
	case c.User != "":
		parts = append(parts, "user and password")
	case c.Token != "":
		parts = append(parts, "token")
	}
	if len(parts) == 0 {
		return "no TLS options, anonymous"
	}
	return strings.Join(parts, ", ")
}

// ForURL reads the process environment and dials url, which a component got from its own
// flag or settings. An empty url keeps the one in NATS_URL.
func ForURL(url string) Config {
	c := FromEnv(os.Getenv)
	if url != "" {
		c.URL = url
	}
	return c
}
