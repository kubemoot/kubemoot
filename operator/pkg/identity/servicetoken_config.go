/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// The environment of the service token verifiers. The operator writes EnvServiceTokens for
// a crew's gateway from spec.discussion.access.serviceTokens and the chart writes it for
// the liaison; EnvHashKey is the install's key, from a Secret, the same in every door.
const (
	EnvServiceTokens = "ACCESS_SERVICE_TOKENS"
	EnvHashKey       = "ACCESS_HASH_KEY"
	// EnvCrew is "<namespace>/<name>" of the one crew a gateway guards. The operator sets it
	// with the service tokens, and a subscriber is admitted for that crew alone. Without it
	// no subscriber is admitted.
	EnvCrew = "ACCESS_CREW"
)

// The values of ServiceTokenConfig.VerifyMethod and .Authorize.
const (
	// VerifyMcpInitialize posts a JSON-RPC initialize request to the service's MCP endpoint.
	VerifyMcpInitialize = "McpInitialize"
	// AuthorizeAnyAccepted lets every person the service accepted use the crew.
	AuthorizeAnyAccepted = "AnyAccepted"
	// AuthorizeSubjects lets only the listed subjects use the crew.
	AuthorizeSubjects = "Subjects"
)

// Bounds on the cache lifetime, in seconds, and the default.
const (
	DefaultMaxCache = 5 * time.Minute
	maxMaxCache     = 15 * time.Minute
)

var (
	namePattern  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	claimPattern = regexp.MustCompile(`^[A-Za-z0-9_]+(\.[A-Za-z0-9_]+)*$`)
)

// ServiceTokenConfig is one service whose acceptance of a person's own bearer token is the
// proof of who they are. The JSON names are the ones of Crew.spec.discussion.access.serviceTokens,
// so the operator renders the list as it is.
type ServiceTokenConfig struct {
	// Name is the issuer in the principal: a DNS label such as "oreilly".
	Name string `json:"name"`
	// VerifyURL is the service's endpoint the token is tried against. https only.
	VerifyURL string `json:"verifyURL"`
	// VerifyMethod is how the token is tried; empty means McpInitialize.
	VerifyMethod string `json:"verifyMethod,omitempty"`
	// SubjectClaim is the dotted path in the token's payload that names the person.
	SubjectClaim string `json:"subjectClaim"`
	// Audiences, when set, are the values the payload's aud must have one of.
	Audiences []string `json:"audiences,omitempty"`
	// MaxCacheSeconds bounds how long an accepted token is remembered, never beyond its
	// own exp. Empty means 300, the most is 900.
	MaxCacheSeconds int `json:"maxCacheSeconds,omitempty"`
	// PassToTools says a door keeps the accepted token in memory for the turn so a tool
	// can call the service as the person. The verifier itself only carries it in the Identity.
	PassToTools bool `json:"passToTools,omitempty"`
	// Authorize is AnyAccepted (the default) or Subjects.
	Authorize string `json:"authorize,omitempty"`
	// Subjects are the raw subjects allowed when Authorize is Subjects.
	Subjects []string `json:"subjects,omitempty"`
}

// ErrServiceTokenConfig marks an invalid ACCESS_SERVICE_TOKENS value.
var ErrServiceTokenConfig = errors.New("invalid service token configuration")

// ParseServiceTokens reads the JSON list of ACCESS_SERVICE_TOKENS. An empty value is no
// verifier. The list is validated as the CRD validates it, so a door never starts with a
// verifier the API server would have refused: names are unique DNS labels, the URL is https,
// the claim path is dotted words, the cache bound is 1 to 900 seconds.
func ParseServiceTokens(raw string) ([]ServiceTokenConfig, error) {
	if raw == "" {
		return nil, nil
	}
	var list []ServiceTokenConfig
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&list); err != nil || dec.More() {
		return nil, fmt.Errorf("%w: %s must be one JSON list", ErrServiceTokenConfig, EnvServiceTokens)
	}
	if err := NormalizeServiceTokens(list); err != nil {
		return nil, err
	}
	return list, nil
}

// NormalizeServiceTokens fills the defaults of every entry in place and validates the list as
// the CRD does. The list of a Crew's spec goes through it, as the list of the environment
// does, so every door applies one set of rules.
func NormalizeServiceTokens(list []ServiceTokenConfig) error {
	seen := map[string]bool{}
	for i := range list {
		if err := list[i].normalize(); err != nil {
			return fmt.Errorf("%w: entry %d: %v", ErrServiceTokenConfig, i, err)
		}
		if seen[list[i].Name] {
			return fmt.Errorf("%w: name %q is used twice", ErrServiceTokenConfig, list[i].Name)
		}
		seen[list[i].Name] = true
		if len(list) > 1 && len(list[i].Audiences) == 0 {
			return fmt.Errorf("%w: entry %d: with more than one service each needs audiences, "+
				"so a token is sent only to the service it was issued for", ErrServiceTokenConfig, i)
		}
	}
	return nil
}

// normalize fills the defaults and validates the entry.
func (c *ServiceTokenConfig) normalize() error {
	if c.VerifyMethod == "" {
		c.VerifyMethod = VerifyMcpInitialize
	}
	if c.Authorize == "" {
		c.Authorize = AuthorizeAnyAccepted
	}
	if c.MaxCacheSeconds == 0 {
		c.MaxCacheSeconds = int(DefaultMaxCache / time.Second)
	}
	return c.validate()
}

// validate checks the entry as the CRD does.
func (c ServiceTokenConfig) validate() error {
	u, err := url.Parse(c.VerifyURL)
	switch {
	case !namePattern.MatchString(c.Name) || c.Name == IssuerKubernetes:
		return errors.New("name must be a DNS label other than " + IssuerKubernetes)
	case err != nil || u.Scheme != schemeHTTPS || u.Host == "":
		return errors.New("verifyURL must be an https URL")
	case c.VerifyMethod != VerifyMcpInitialize:
		return fmt.Errorf("verifyMethod %q is not supported", c.VerifyMethod)
	case !claimPattern.MatchString(c.SubjectClaim):
		return errors.New("subjectClaim must be a dotted path of letters, digits and underscores")
	case c.MaxCacheSeconds < 1 || time.Duration(c.MaxCacheSeconds)*time.Second > maxMaxCache:
		return errors.New("maxCacheSeconds must be 1 to 900")
	}
	return c.validateAuthorize()
}

func (c ServiceTokenConfig) validateAuthorize() error {
	switch c.Authorize {
	case AuthorizeAnyAccepted:
		if len(c.Subjects) > 0 {
			return errors.New("subjects are only used with authorize Subjects")
		}
	case AuthorizeSubjects:
		if len(c.Subjects) == 0 {
			return errors.New("authorize Subjects needs at least one subject")
		}
	default:
		return fmt.Errorf("authorize %q is not supported", c.Authorize)
	}
	return nil
}

// maxCache is the cache bound as a duration.
func (c ServiceTokenConfig) maxCache() time.Duration {
	if c.MaxCacheSeconds <= 0 {
		return DefaultMaxCache
	}
	return time.Duration(c.MaxCacheSeconds) * time.Second
}
