package main

import (
	"errors"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/accounts"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/liaison"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/signin"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
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
	return &accounts.Service{
		Vault: vault, Decls: liaison.NewDeclarations(reader), Regs: cfg.Registrations,
		Client: accounts.NewClient(nil), Now: time.Now,
	}, nil
}

// errAccountsNeedDoor is the refusal of connected accounts without the door's own sign-in.
var errAccountsNeedDoor = errors.New(accounts.EnvEnabled + " needs the door's own identity (" + signin.EnvIdentity +
	"): a connection belongs to the person the door signed in")
