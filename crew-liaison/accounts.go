package main

import (
	"crypto/tls"
	"errors"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/accounts"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/liaison"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/signin"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// buildAccounts turns on connected accounts when the install asked for them
// (LIAISON_ACCOUNTS_ENABLED=true). Off, it returns nil and nothing is ever kept for anyone.
// The tokens are sealed with their own keys, apart from the door's, and live in the same
// backend as the door's single-use memory.
func buildAccounts(
	getenv func(string) string, state *tokenstore.Opener, reader client.Reader, log logr.Logger,
) (*accounts.Service, error) {
	cfg, err := accounts.ConfigFromEnv(getenv, nil)
	if err != nil || !cfg.Enabled {
		return nil, err
	}
	people := accounts.NewPrincipals([]byte(getenv(identity.EnvHashKey)))
	if !people.Keyed() {
		return nil, errors.New(accounts.EnvEnabled + " needs " + identity.EnvHashKey +
			": a person's connection is kept under the id the crews' gateways name them by")
	}
	backend, err := state.Backend("accounts", accounts.IdleLife+time.Hour)
	if err != nil {
		return nil, err
	}
	sealer, err := signin.ParseKeys(cfg.SealKeys)
	if err != nil {
		return nil, err
	}
	store, err := tokenstore.NewSealed(backend, sealer, accounts.IdleLife)
	if err != nil {
		return nil, err
	}
	vault, err := accounts.NewVault(store, cfg.Registrations, nil)
	if err != nil {
		return nil, err
	}
	if !state.Shared() {
		log.Info("connected accounts are kept in this process only: set " + tokenstore.EnvBackend +
			" to nats or openbao to keep them across restarts and share them between replicas")
	}
	decls := liaison.NewDeclarations(reader)
	upstream := accounts.NewClient(nil)
	return &accounts.Service{
		People: people, Vault: vault, Decls: decls, Regs: cfg.Registrations, Client: upstream, Now: time.Now, Log: log,
		Broker: newBroker(vault, decls, cfg.Registrations, upstream, log),
	}, nil
}

// The internal listener of the account broker.
const (
	envInternalPort     = "LIAISON_INTERNAL_PORT"
	defaultInternalPort = "8081"
)

// brokerServer is the internal HTTP server a crew's discussion gateway asks for a person's
// account token. It is never behind a Service the public reaches. The caller's ServiceAccount
// token is checked with TokenReview for the broker's audience. A non-nil tlsConfig makes it
// serve TLS only.
func brokerServer(
	svc *accounts.Service, reviewer identity.Reviewer, gateways liaison.GatewayVerifier, port string,
	tlsConfig *tls.Config,
) *http.Server {
	return &http.Server{
		Addr: ":" + port, Handler: liaison.NewAccountBroker(svc.Broker, reviewer, gateways).Handler(),
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second,
		IdleTimeout: 120 * time.Second,
	}
}

// accountReviewer checks ServiceAccount tokens made for the broker's audience.
func accountReviewer() (identity.Reviewer, error) {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return identity.Reviewer{}, err
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return identity.Reviewer{}, err
	}
	return identity.Reviewer{Client: clientset, Audiences: []string{liaison.AccountBrokerAudience}}, nil
}

// errAccountsNeedDoor is the refusal of connected accounts without the door's own sign-in.
var errAccountsNeedDoor = errors.New(accounts.EnvEnabled + " needs the door's own identity (" + signin.EnvIdentity +
	"): a connection belongs to the person the door signed in")

func newBroker(
	vault *accounts.Vault, decls accounts.Declarations, regs map[string]accounts.Registration,
	upstream *accounts.Client, log logr.Logger,
) *accounts.Broker {
	b := accounts.NewBroker(vault, decls, regs, upstream, nil)
	b.Log = log
	return b
}
