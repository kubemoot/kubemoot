package tokenstore

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/operator/pkg/natsconn"
)

// Environment the liaison reads to choose where it keeps what it must remember.
const (
	EnvBackend          = "LIAISON_STATE_BACKEND" // memory (default), nats or openbao
	EnvOpenBaoAddr      = "LIAISON_OPENBAO_ADDR"
	EnvOpenBaoMount     = "LIAISON_OPENBAO_MOUNT"
	EnvOpenBaoPrefix    = "LIAISON_OPENBAO_PREFIX"
	EnvOpenBaoTokenFile = "LIAISON_OPENBAO_TOKEN_FILE"
	EnvOpenBaoCAFile    = "LIAISON_OPENBAO_CA_FILE"
)

// Backend names.
const (
	BackendMemory  = "memory"
	BackendNATS    = "nats"
	BackendOpenBao = "openbao"
)

// Config is the choice of backend. The NATS connection is the one every Kubemoot component
// reads (NATS_URL and friends).
type Config struct {
	Backend string
	NATS    natsconn.Config

	OpenBaoAddr      string
	OpenBaoMount     string
	OpenBaoPrefix    string
	OpenBaoTokenFile string
	OpenBaoCAFile    string
}

// ConfigFromEnv reads the choice. An unknown backend, or a backend missing what it needs, is
// an error that names the variable.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	get := func(name string) string { return strings.TrimSpace(getenv(name)) }
	c := Config{
		Backend:          strings.ToLower(get(EnvBackend)),
		NATS:             natsconn.FromEnv(getenv),
		OpenBaoAddr:      get(EnvOpenBaoAddr),
		OpenBaoMount:     get(EnvOpenBaoMount),
		OpenBaoPrefix:    get(EnvOpenBaoPrefix),
		OpenBaoTokenFile: get(EnvOpenBaoTokenFile),
		OpenBaoCAFile:    get(EnvOpenBaoCAFile),
	}
	if c.Backend == "" {
		c.Backend = BackendMemory
	}
	return c, c.validate()
}

func (c Config) validate() error {
	switch c.Backend {
	case BackendMemory:
		return nil
	case BackendNATS:
		if c.NATS.URL == "" {
			return fmt.Errorf("%s=nats needs %s", EnvBackend, natsconn.EnvURL)
		}
		return c.NATS.Validate()
	case BackendOpenBao:
		if c.OpenBaoAddr == "" || c.OpenBaoTokenFile == "" {
			return fmt.Errorf("%s=openbao needs %s and %s", EnvBackend, EnvOpenBaoAddr, EnvOpenBaoTokenFile)
		}
		return nil
	}
	return fmt.Errorf("%s must be memory, nats or openbao, got %q", EnvBackend, c.Backend)
}

// Shared reports whether replicas see the same values.
func (c Config) Shared() bool { return c.Backend != BackendMemory }

// Opener holds the one connection a chosen backend needs and makes a Backend for each
// purpose (the door's single-use marks and the connected accounts are kept apart).
type Opener struct {
	cfg  Config
	conn *nats.Conn
	mem  map[string]*Memory
}

// Open connects when the backend needs a connection.
func (c Config) Open() (*Opener, error) {
	o := &Opener{cfg: c, mem: map[string]*Memory{}}
	if c.Backend == BackendNATS {
		conn, err := c.NATS.Connect(nats.Name("crew-liaison"))
		if err != nil {
			return nil, err
		}
		o.conn = conn
	}
	return o, nil
}

// Shared reports whether the backend's values are seen by every replica.
func (o *Opener) Shared() bool { return o.cfg.Shared() }

// Close ends the connection.
func (o *Opener) Close() {
	if o.conn != nil {
		o.conn.Close()
	}
}

// Backend returns the backend for one purpose. purpose names the NATS bucket and the OpenBao
// path below the prefix; maxTTL is the longest any value there is kept.
func (o *Opener) Backend(purpose string, maxTTL time.Duration) (Backend, error) {
	switch o.cfg.Backend {
	case BackendNATS:
		return NewNATS(o.conn, "kubemoot_liaison_"+purpose, maxTTL)
	case BackendOpenBao:
		return o.openBao(purpose)
	}
	if o.mem[purpose] == nil {
		o.mem[purpose] = NewMemory(nil)
	}
	return o.mem[purpose], nil
}

func (o *Opener) openBao(purpose string) (Backend, error) {
	tokenFile := o.cfg.OpenBaoTokenFile
	client, err := baoClient(o.cfg.OpenBaoCAFile)
	if err != nil {
		return nil, err
	}
	prefix := strings.Trim(o.cfg.OpenBaoPrefix, "/")
	if prefix == "" {
		prefix = "kubemoot-liaison"
	}
	return NewOpenBao(OpenBaoConfig{
		Address: o.cfg.OpenBaoAddr, Mount: o.cfg.OpenBaoMount, Prefix: prefix + "/" + purpose, Client: client,
		Token: func() (string, error) {
			raw, err := os.ReadFile(tokenFile)
			return strings.TrimSpace(string(raw)), err
		},
	})
}

// baoClient trusts the install's own CA for OpenBao when one is given.
func baoClient(caFile string) (*http.Client, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	if caFile == "" {
		return client, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvOpenBaoCAFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New(EnvOpenBaoCAFile + " holds no PEM certificate")
	}
	client.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	return client, nil
}
