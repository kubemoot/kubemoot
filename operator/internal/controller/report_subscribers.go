/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// subscriberDirectory identifies a crew's subscribers on the report service: a person whose
// own token a service the crew trusts accepts (Crew.spec.discussion.access.serviceTokens).
// It builds each crew's verifier chain from the Crew and its hash key Secret with the same
// code the crew's gateway uses (identity.NewServiceTokenChain), and keeps the chain so the
// verifier's cache, single-flight and rate limit hold across requests. A change to the Crew's
// verifiers or to the key builds a new chain at the next request.
type subscriberDirectory struct {
	reader client.Reader
	// client asks the services; nil is the default, a test sets its own.
	client *http.Client
	mu     sync.Mutex
	chains map[types.NamespacedName]*crewChain
}

// crewChain is a crew's chain and what it was built from.
type crewChain struct {
	configs []byte
	key     []byte
	chain   identity.ServiceTokenChain
	auth    *identity.Authenticator
}

// NewSubscriberDirectory returns the directory that reads crews and their keys with reader.
func NewSubscriberDirectory(reader client.Reader) *subscriberDirectory {
	return &subscriberDirectory{reader: reader, chains: map[types.NamespacedName]*crewChain{}}
}

// Authorize identifies the caller by their service token and checks that the crew lets them
// make the access. A request with no token, a crew that does not exist and a crew that
// accepts no service token know no subscribers, so the caller is unauthenticated; a crew
// whose key cannot be read cannot name them, so the check cannot be made (never an
// acceptance). The access must be the use of the crew, which is all the crew's own
// configuration can grant a subscriber.
func (d *subscriberDirectory) Authorize(r *http.Request, attrs authorizationv1.ResourceAttributes) (*identity.Identity, error) {
	if identity.BearerToken(r) == "" {
		return nil, errUnauthenticated
	}
	c, err := d.crewChain(r.Context(), types.NamespacedName{Namespace: attrs.Namespace, Name: attrs.Name})
	if err != nil {
		return nil, err
	}
	caller, err := c.auth.Authenticate(r)
	switch {
	case errors.Is(err, identity.ErrInvalid), errors.Is(err, identity.ErrRequired):
		return nil, errUnauthenticated
	case err != nil:
		return nil, err
	}
	allowed, err := c.chain.Authorizer.Allowed(r.Context(), caller.Identity, attrs)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, errForbidden
	}
	return caller.Identity, nil
}

// crewChain reads the crew and its key and returns the chain they describe. A crew that is
// gone or accepts no service token drops any chain kept for it.
func (d *subscriberDirectory) crewChain(ctx context.Context, name types.NamespacedName) (*crewChain, error) {
	crew := &kubemootv1alpha1.Crew{}
	err := d.reader.Get(ctx, name, crew)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("read the crew: %w", err)
	}
	var configs []identity.ServiceTokenConfig
	if err == nil {
		configs = serviceTokenConfigs(discussionAccess(crew))
	}
	if len(configs) == 0 {
		d.forget(name)
		return nil, errUnauthenticated
	}
	key, err := crewHashKey(ctx, d.reader, crew)
	if err != nil {
		return nil, err
	}
	return d.chainFor(name, configs, key)
}

func (d *subscriberDirectory) forget(name types.NamespacedName) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.chains, name)
}

// chainFor returns the crew's chain, built again when the verifiers or the key changed.
func (d *subscriberDirectory) chainFor(name types.NamespacedName, configs []identity.ServiceTokenConfig, key []byte) (*crewChain, error) {
	raw, err := json.Marshal(configs)
	if err != nil { // a list of strings and numbers always marshals
		return nil, err
	}
	// Building a chain makes no network call, so holding the lock here is cheap; keep I/O out of it.
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.chains[name]; ok && bytes.Equal(c.configs, raw) && bytes.Equal(c.key, key) {
		return c, nil
	}
	chain, err := identity.NewServiceTokenChain(configs, key, d.client)
	if err != nil {
		delete(d.chains, name)
		return nil, err
	}
	c := &crewChain{configs: raw, key: key, chain: chain, auth: identity.NewAuthenticator(chain.Hasher, false, chain.Verifiers...)}
	d.chains[name] = c
	return c, nil
}
