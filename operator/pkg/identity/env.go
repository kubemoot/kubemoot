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
func FromEnv(getenv func(string) string, newClient func() (kubernetes.Interface, error)) (Settings, error) {
	if getenv(EnvEnforce) != EnvTrue {
		return Settings{Authenticator: AnonymousAuthenticator()}, nil
	}
	allowAnonymous := getenv(EnvAllowAnonymous) == EnvTrue
	if getenv(EnvTokenReview) != EnvTrue {
		return Settings{Authenticator: NewAuthenticator(Hasher{}, allowAnonymous)}, nil
	}
	client, err := newClient()
	if err != nil {
		return Settings{}, fmt.Errorf("token review needs a Kubernetes client: %w", err)
	}
	reviewer := Reviewer{Client: client, Audiences: splitList(getenv(EnvTokenAudiences))}
	return Settings{
		Authenticator: NewAuthenticator(Hasher{}, allowAnonymous, TokenReviewVerifier{Reviewer: reviewer}),
		Authorizer:    reviewer,
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
