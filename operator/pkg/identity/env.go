/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"fmt"
	"net/http"
	"strings"

	"k8s.io/client-go/kubernetes"
)

// The environment a door reads its access settings from. The operator writes it for a
// crew's gateway from spec.discussion.access and the chart writes it for the liaison, so
// producer and consumer share these names. A door with none of it authenticates no one.
const (
	EnvEnforce        = "ACCESS_ENFORCE"
	EnvTokenReview    = "ACCESS_TOKEN_REVIEW"
	EnvAllowAnonymous = "ACCESS_ALLOW_ANONYMOUS"
	EnvTokenAudiences = "ACCESS_TOKEN_AUDIENCES"
	// EnvExposeLabel makes a door stamp a Kubernetes caller's user name on the principal of
	// the questions it queues, as the label the crew's agents may be told. Off, a person has
	// no label on the bus. The operator sets it from spec.discussion.identity.exposeToAgents.
	EnvExposeLabel = "ACCESS_EXPOSE_LABEL"
	// EnvExposeCaller is the agent-side switch, from the same Crew field: the agent puts the
	// caller in its turn context. It is the agents' own variable, not a door's.
	EnvExposeCaller = "KUBEMOOT_EXPOSE_CALLER"
	// EnvTrue is the value that turns a boolean setting on.
	EnvTrue = "true"
)

// Settings is what a door needs to know callers: who verifies them, and who decides what a
// verified caller may do. Authorizer is nil when no token is checked.
type Settings struct {
	Authenticator *Authenticator
	Authorizer    Authorizer
}

// FromEnv reads the access settings. newClient is called only when tokens are to be checked,
// so a door that enforces nothing needs no Kubernetes credentials. An error means the door
// must not start: serving open while verification was asked for would be the wrong failure.
// ACCESS_SERVICE_TOKENS (a JSON list, see ServiceTokenConfig) adds a verifier per service,
// after the Kubernetes one so a Kubernetes token never goes to a service; it needs
// ACCESS_HASH_KEY, because a subscriber is never named without the install key.
func FromEnv(getenv func(string) string, newClient func() (kubernetes.Interface, error)) (Settings, error) {
	if getenv(EnvEnforce) != EnvTrue {
		return Settings{Authenticator: AnonymousAuthenticator()}, nil
	}
	allowAnonymous := getenv(EnvAllowAnonymous) == EnvTrue
	var verifiers []Verifier
	routed := RoutedAuthorizer{}
	if getenv(EnvTokenReview) == EnvTrue {
		client, err := newClient()
		if err != nil {
			return Settings{}, fmt.Errorf("token review needs a Kubernetes client: %w", err)
		}
		reviewer := Reviewer{Client: client, Audiences: splitList(getenv(EnvTokenAudiences))}
		verifiers = append(verifiers, TokenReviewVerifier{Reviewer: reviewer})
		routed.Kubernetes = reviewer
	}
	hasher, serviceVerifiers, subscribers, err := serviceTokensFromEnv(getenv)
	if err != nil {
		return Settings{}, err
	}
	verifiers = append(verifiers, serviceVerifiers...)
	routed.Subscribers = subscribers
	auth := NewAuthenticator(hasher, allowAnonymous, verifiers...)
	return Settings{Authenticator: auth, Authorizer: authorizerOf(routed)}, nil
}

// authorizerOf is the authorizer of what FromEnv configured: nil when nothing is checked,
// the Kubernetes authorizer alone when there are no subscribers, as before they existed.
func authorizerOf(r RoutedAuthorizer) Authorizer {
	switch {
	case r.Subscribers == nil && r.Kubernetes == nil:
		return nil
	case r.Subscribers == nil:
		return r.Kubernetes
	}
	return r
}

// serviceTokensFromEnv builds the service token verifiers, the hasher for subscriber ids
// and the subscriber authorizer. With no service tokens configured it returns the plain
// hasher and nothing else.
func serviceTokensFromEnv(getenv func(string) string) (Hasher, []Verifier, Authorizer, error) {
	configs, err := ParseServiceTokens(getenv(EnvServiceTokens))
	if err != nil || len(configs) == 0 {
		return Hasher{}, nil, nil, err
	}
	key := []byte(getenv(EnvHashKey))
	if len(key) == 0 {
		return Hasher{}, nil, nil, fmt.Errorf("%s is set and %s is not: %w", EnvServiceTokens, EnvHashKey, ErrNoHashKey)
	}
	chain, err := NewServiceTokenChain(configs, key, nil)
	if err != nil {
		return Hasher{}, nil, nil, err
	}
	return chain.Hasher, chain.Verifiers, chain.Authorizer, nil
}

// ServiceTokenChain is what a door needs to know a crew's subscribers: a verifier per
// service, the keyed hasher that names them, and the authorizer of what they may do.
type ServiceTokenChain struct {
	Hasher     Hasher
	Verifiers  []Verifier
	Authorizer Authorizer
}

// NewServiceTokenChain builds the chain of a crew's service token verifiers. The gateway
// builds it from its environment and the operator's report service from the Crew, so the
// two verify, name and authorize a subscriber by the same code. configs are validated as
// the CRD does. client, nil for the default, is what asks the services. Without the install's
// key it is ErrNoHashKey, because a subscriber is
// never named, and so never owns or reads anything, without it.
func NewServiceTokenChain(configs []ServiceTokenConfig, key []byte, client *http.Client) (ServiceTokenChain, error) {
	if len(key) == 0 {
		return ServiceTokenChain{}, ErrNoHashKey
	}
	list := append([]ServiceTokenConfig(nil), configs...)
	if err := NormalizeServiceTokens(list); err != nil {
		return ServiceTokenChain{}, err
	}
	verifiers := make([]Verifier, 0, len(list))
	for _, c := range list {
		v, err := NewServiceTokenVerifier(c, key, client)
		if err != nil {
			return ServiceTokenChain{}, err
		}
		verifiers = append(verifiers, v)
	}
	return ServiceTokenChain{
		Hasher: Hasher{Key: key}, Verifiers: verifiers, Authorizer: NewSubscriberAuthorizer(list),
	}, nil
}

// splitList splits a comma separated list, dropping blanks.
func splitList(v string) []string {
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// Refusal is how a door answers an authentication or authorization error: the status, the
// message to send, and the WWW-Authenticate challenge ("" when there is none). The cause of a
// check that could not be made is never in the message.
func Refusal(err error, realm string) (code int, message, challenge string) {
	code, needsChallenge := Status(err)
	message = err.Error()
	if code == 503 {
		message = "could not check the caller's access"
	}
	if needsChallenge {
		challenge = `Bearer realm="` + realm + `"`
	}
	return code, message, challenge
}
