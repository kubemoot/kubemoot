package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/api"
	"github.com/kubemoot/kubemoot/discussion-gateway/internal/grants"
)

// The environment of the credential lease. The grant store is on only when a verifier of
// ACCESS_SERVICE_TOKENS has passToTools; the lease listener runs only when, besides that,
// the one ServiceAccount allowed to lease is named. The installer sets both on the gateway's
// Deployment until the operator writes them.
const (
	// envBrokerSubject is the full user name of the MCP gateway's ServiceAccount.
	envBrokerSubject = "ACCESS_BROKER_SUBJECT"
	// envInternalPort is the port of the lease listener; it is never behind a Service
	// the public reaches.
	envInternalPort = "INTERNAL_PORT"
	// envMaxGrants bounds how many conversations hold a token at once.
	envMaxGrants = "ACCESS_MAX_GRANTS"

	defaultInternalPort = "8081"
	sweepInterval       = time.Minute
)

// credentialConfig is what the environment says about keeping tokens.
type credentialConfig struct {
	// issuers are the verifiers whose callers' tokens are kept.
	issuers       []string
	brokerSubject string
	internalPort  string
	maxGrants     int
}

// credentialConfigFromEnv reads the settings. An error means the service token list is invalid
// (the identity setup reports it too).
func credentialConfigFromEnv(getenv func(string) string) (credentialConfig, error) {
	configs, err := identity.ParseServiceTokens(getenv(identity.EnvServiceTokens))
	if err != nil {
		return credentialConfig{}, err
	}
	c := credentialConfig{brokerSubject: getenv(envBrokerSubject), internalPort: getenv(envInternalPort)}
	for _, cfg := range configs {
		if cfg.PassToTools {
			c.issuers = append(c.issuers, cfg.Name)
		}
	}
	if c.internalPort == "" {
		c.internalPort = defaultInternalPort
	}
	if n, err := strconv.Atoi(getenv(envMaxGrants)); err == nil && n > 0 {
		c.maxGrants = n
	}
	return c, nil
}

// keepsTokens reports whether any caller's token is kept.
func (c credentialConfig) keepsTokens() bool { return len(c.issuers) > 0 }

// leases reports whether the lease listener is to run.
func (c credentialConfig) leases() bool { return c.keepsTokens() && c.brokerSubject != "" }

// leaseServer is the internal HTTP server of the lease route.
func (c credentialConfig) leaseServer(store grants.Store, client kubernetes.Interface) *http.Server {
	reviewer := identity.Reviewer{Client: client, Audiences: []string{api.BrokerAudience}}
	mux := http.NewServeMux()
	api.NewLeaseServer(store, reviewer, c.brokerSubject).RegisterRoutes(mux)
	return &http.Server{
		Addr: ":" + c.internalPort, Handler: mux,
		ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second,
	}
}

// leaseClient is the in-cluster client the lease listener checks ServiceAccount tokens with.
func leaseClient() (kubernetes.Interface, error) {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}
