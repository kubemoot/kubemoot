package main

import (
	"net/http"
	"strconv"
	"strings"
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
	// crew and accountBroker are set when the gateway may lease a person's connected account
	// token: the crew it serves, and the address of the liaison's account broker.
	crew          string
	accountBroker string
}

// credentialConfigFromEnv reads the settings. An error means the service token list is invalid
// (the identity setup reports it too).
func credentialConfigFromEnv(getenv func(string) string) (credentialConfig, error) {
	configs, err := identity.ParseServiceTokens(getenv(identity.EnvServiceTokens))
	if err != nil {
		return credentialConfig{}, err
	}
	c := credentialConfig{
		brokerSubject: getenv(envBrokerSubject), internalPort: getenv(envInternalPort),
		crew:          strings.TrimSpace(getenv(identity.EnvAccountCrew)),
		accountBroker: strings.TrimSpace(getenv(identity.EnvAccountBrokerURL)),
	}
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

// leasesAccounts reports whether the gateway may lease a connected account's token: it knows
// its crew and the broker that holds the tokens.
func (c credentialConfig) leasesAccounts() bool { return c.crew != "" && c.accountBroker != "" }

// leases reports whether the lease listener is to run: the ServiceAccount allowed to lease is
// named, and there are tokens to lease, kept for the turn or connected accounts'.
func (c credentialConfig) leases() bool {
	return c.brokerSubject != "" && (c.keepsTokens() || c.leasesAccounts())
}

// leaseServer is the internal HTTP server of the lease route. accounts and owners are nil
// unless the gateway leases connected accounts.
func (c credentialConfig) leaseServer(store grants.Store, client kubernetes.Interface,
	accounts identity.AccountLeaser, owners api.OwnerReader) *http.Server {
	reviewer := identity.Reviewer{Client: client, Audiences: []string{api.BrokerAudience}}
	mux := http.NewServeMux()
	api.NewLeaseServer(store, reviewer, c.brokerSubject).WithAccounts(accounts, owners).RegisterRoutes(mux)
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
