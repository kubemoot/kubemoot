/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"context"
	"slices"

	authorizationv1 "k8s.io/api/authorization/v1"
)

// SubscriberAuthorizer decides what a subscriber may do. A subscriber has no Kubernetes
// identity, so RBAC cannot speak for them; the crew's own configuration does. They may use
// the crew (the access UseAccess describes) when a service token verifier named as their
// issuer is configured for it and either accepts every person its service accepted or lists
// their subject. They may do nothing else: reading other callers' conversations and
// controlling them stay Kubernetes rights.
type SubscriberAuthorizer struct {
	byName map[string]ServiceTokenConfig
}

// NewSubscriberAuthorizer returns the authorizer of the configured verifiers.
func NewSubscriberAuthorizer(configs []ServiceTokenConfig) SubscriberAuthorizer {
	m := make(map[string]ServiceTokenConfig, len(configs))
	for _, c := range configs {
		m[c.Name] = c
	}
	return SubscriberAuthorizer{byName: m}
}

// Allowed implements Authorizer for a subscriber identity. An issuer with no configured
// verifier, or any access other than using the crew, is refused.
func (a SubscriberAuthorizer) Allowed(
	_ context.Context, id *Identity, attrs authorizationv1.ResourceAttributes,
) (bool, error) {
	if !isUseAccess(attrs) {
		return false, nil
	}
	cfg, ok := a.byName[id.Issuer]
	if !ok {
		return false, nil
	}
	if cfg.Authorize == AuthorizeSubjects {
		return slices.Contains(cfg.Subjects, id.Subject), nil
	}
	return true, nil
}

// isUseAccess reports whether attrs is the access of using a crew: get on the Crew itself.
func isUseAccess(attrs authorizationv1.ResourceAttributes) bool {
	return attrs.Name != "" && attrs == UseAccess(attrs.Namespace, attrs.Name)
}

// RoutedAuthorizer sends a Kubernetes identity to the Kubernetes authorizer (RBAC) and a
// subscriber to the subscriber authorizer. Either may be nil, which refuses that kind.
type RoutedAuthorizer struct {
	Kubernetes  Authorizer
	Subscribers Authorizer
}

// Allowed implements Authorizer.
func (r RoutedAuthorizer) Allowed(
	ctx context.Context, id *Identity, attrs authorizationv1.ResourceAttributes,
) (bool, error) {
	target := r.Subscribers
	if id.Issuer == IssuerKubernetes {
		target = r.Kubernetes
	}
	if target == nil {
		return false, nil
	}
	return target.Allowed(ctx, id, attrs)
}
