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

// hopDirectory identifies, on the report service, a person a trusted hop asserted for a crew:
// someone signed in at the crew liaison's door, who has no Kubernetes token. The assertion is
// the one the door signs for the crew's gateway (its audience names that crew), verified with
// the public keys the Crew lists in spec.discussion.access.trustedHops.
//
// It identifies and nothing more. The person reaches only the routes that narrow a caller to
// what they own: the conversation export checks the owner, and the person is never asked of
// the API server, so exporting is limited to their own conversations whatever the askers
// rule says. A crew whose people are Kubernetes users (kubernetesRBAC) is not served here:
// those people carry Kubernetes tokens.
type hopDirectory struct {
	reader client.Reader
	mu     sync.Mutex
	hops   map[types.NamespacedName]*crewHops
}

// crewHops is a crew's verifier and the configuration it was built from.
type crewHops struct {
	configs  []byte
	verifier *identity.HopVerifier
}

// NewHopDirectory returns the directory that reads crews with reader.
func NewHopDirectory(reader client.Reader) *hopDirectory {
	return &hopDirectory{reader: reader, hops: map[types.NamespacedName]*crewHops{}}
}

// Authorize identifies the caller by their assertion for this crew. A request with no
// assertion, a crew that does not exist, one that trusts no hop, and one whose rule maps its
// people to Kubernetes know no such person, so the caller is unauthenticated.
func (d *hopDirectory) Authorize(r *http.Request, attrs authorizationv1.ResourceAttributes) (*identity.Identity, error) {
	if identity.BearerToken(r) == "" {
		return nil, errUnauthenticated
	}
	name := types.NamespacedName{Namespace: attrs.Namespace, Name: attrs.Name}
	verifier, err := d.verifierFor(r.Context(), name)
	if err != nil {
		return nil, err
	}
	id, err := verifier.Verify(r)
	switch {
	case errors.Is(err, identity.ErrInvalid), errors.Is(err, identity.ErrNoCredential):
		return nil, errUnauthenticated
	case err != nil:
		return nil, err
	}
	return id, nil
}

// verifierFor builds the crew's hop verifier from the Crew, again when its hops changed.
func (d *hopDirectory) verifierFor(ctx context.Context, name types.NamespacedName) (*identity.HopVerifier, error) {
	crew := &kubemootv1alpha1.Crew{}
	err := d.reader.Get(ctx, name, crew)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("read the crew: %w", err)
	}
	var configs []identity.TrustedHopConfig
	if err == nil {
		configs = hopConfigsOf(crew)
	}
	if len(configs) == 0 {
		d.forget(name)
		return nil, errUnauthenticated
	}
	return d.verifier(name, configs)
}

// hopConfigsOf is the crew's trusted hops with the audience of its own gateway, or nothing
// when the crew trusts none, they cannot be applied, or its people are Kubernetes users.
func hopConfigsOf(crew *kubemootv1alpha1.Crew) []identity.TrustedHopConfig {
	access := discussionAccess(crew)
	if access == nil || len(access.TrustedHops) == 0 || validateTrustedHops(crew, access) != nil ||
		access.Askers == nil || access.Askers.KubernetesRBAC != nil {
		return nil
	}
	out := make([]identity.TrustedHopConfig, 0, len(access.TrustedHops))
	for _, h := range access.TrustedHops {
		out = append(out, identity.TrustedHopConfig{
			Name: h.Name, Audience: identity.HopAudience(crew.Namespace, crew.Name), PublicKeys: h.PublicKeys,
		})
	}
	return out
}

func (d *hopDirectory) forget(name types.NamespacedName) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.hops, name)
}

func (d *hopDirectory) verifier(name types.NamespacedName, configs []identity.TrustedHopConfig) (*identity.HopVerifier, error) {
	raw, err := json.Marshal(configs)
	if err != nil { // lists of strings always marshal
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.hops[name]; ok && bytes.Equal(c.configs, raw) {
		return c.verifier, nil
	}
	v, err := identity.NewHopVerifier(configs, nil)
	if err != nil {
		delete(d.hops, name)
		return nil, err
	}
	d.hops[name] = &crewHops{configs: raw, verifier: v}
	return v, nil
}
