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
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/go-logr/logr"
	authorizationv1 "k8s.io/api/authorization/v1"
)

// A trusted hop proves who is asking and nothing more. Which of those people may ask a crew is
// the crew's own rule, Crew.spec.discussion.access.askers, applied here by the one seam
// AskerAuthorizer. The kinds are alternatives: exactly one is set.
const (
	// EnvAskers is the JSON of AskersConfig the operator writes for a gateway.
	EnvAskers = "ACCESS_ASKERS"
	// EnvAskersToken is the bearer token presented to an external policy decision point.
	EnvAskersToken = "ACCESS_ASKERS_TOKEN"
	// HopIssuerPrefix starts the issuer of a person a hop asserted whose kind does not map them
	// to a Kubernetes user. The issuer is the prefix and the hop's name.
	HopIssuerPrefix = "hop:"

	maxIssuerGroups = 32
)

// ErrAskersConfig marks an invalid ACCESS_ASKERS value.
var ErrAskersConfig = errors.New("invalid askers configuration")

// AskersConfig is the rule of Crew.spec.discussion.access.askers, with the same JSON names.
type AskersConfig struct {
	KubernetesRBAC   *struct{}       `json:"kubernetesRBAC,omitempty"`
	ConnectedAccount string          `json:"connectedAccount,omitempty"`
	IssuerGroups     []string        `json:"issuerGroups,omitempty"`
	AnySignedIn      *struct{}       `json:"anySignedIn,omitempty"`
	External         *ExternalConfig `json:"external,omitempty"`
	// Namespace and Crew name the one crew this gateway guards. The operator sets them; a rule
	// admits a person only for that crew, never for another crew name in a request path.
	Namespace string `json:"namespace,omitempty"`
	Crew      string `json:"crew,omitempty"`
}

// ExternalConfig is the policy decision point of the external kind.
type ExternalConfig struct {
	URL          string `json:"url"`
	SubjectType  string `json:"subjectType,omitempty"`
	ResourceType string `json:"resourceType,omitempty"`
	Action       string `json:"action,omitempty"`
}

// ParseAskers reads ACCESS_ASKERS. An empty value is no rule (nil).
func ParseAskers(raw string) (*AskersConfig, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var c AskersConfig
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, fmt.Errorf("%w: %s must be one JSON object", ErrAskersConfig, EnvAskers)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Kind names the one kind that is set, or "" when none or several are.
func (c *AskersConfig) Kind() string {
	var kinds []string
	for kind, set := range map[string]bool{
		"kubernetesRBAC": c.KubernetesRBAC != nil, "connectedAccount": c.ConnectedAccount != "",
		"issuerGroups": len(c.IssuerGroups) > 0, "anySignedIn": c.AnySignedIn != nil, "external": c.External != nil,
	} {
		if set {
			kinds = append(kinds, kind)
		}
	}
	if len(kinds) != 1 {
		return ""
	}
	return kinds[0]
}

// Validate refuses a rule that sets no kind or several, or a kind that is malformed.
func (c *AskersConfig) Validate() error {
	if c.Kind() == "" {
		return fmt.Errorf("%w: set exactly one kind", ErrAskersConfig)
	}
	if len(c.IssuerGroups) > maxIssuerGroups || slices.Contains(c.IssuerGroups, "") {
		return fmt.Errorf("%w: issuerGroups holds 1 to %d non-empty names", ErrAskersConfig, maxIssuerGroups)
	}
	if c.External != nil {
		return c.External.validate()
	}
	return nil
}

func (e *ExternalConfig) validate() error {
	if !strings.HasPrefix(e.URL, "https://") || len(e.URL) <= len("https://") {
		return fmt.Errorf("%w: external.url must be https", ErrAskersConfig)
	}
	return nil
}

// AskerAuthorizer applies the crew's rule to a person a hop asserted. A person is admitted to
// use the crew and to nothing else: reading others' conversations stays a Kubernetes right.
// Only the kubernetesRBAC kind calls the Kubernetes API, and that kind is not routed here (its
// people are Kubernetes identities and the Kubernetes authorizer answers for them).
type AskerAuthorizer struct {
	Rule AskersConfig
	// PDP answers the external kind; nil refuses it.
	PDP AccessEvaluator
	Log logr.Logger
}

// AccessEvaluator asks a policy decision point whether a subject may take an action on a resource.
type AccessEvaluator interface {
	Evaluate(ctx context.Context, subject Identity, resourceID string) (bool, error)
}

// Allowed implements Authorizer.
func (a AskerAuthorizer) Allowed(
	ctx context.Context, id *Identity, attrs authorizationv1.ResourceAttributes,
) (bool, error) {
	if !isUseAccess(attrs) || a.Rule.Crew == "" || attrs.Namespace != a.Rule.Namespace || attrs.Name != a.Rule.Crew {
		return false, nil
	}
	switch a.Rule.Kind() {
	case "anySignedIn":
		return true, nil
	case "connectedAccount":
		// The account is declared by the crew (the operator checks it). Leasing it per person
		// comes with the tool gateway; today every signed-in person is admitted.
		return true, nil
	case "issuerGroups":
		return slices.ContainsFunc(id.Groups, func(g string) bool { return slices.Contains(a.Rule.IssuerGroups, g) }), nil
	case "external":
		return a.external(ctx, id, attrs)
	}
	return false, nil
}

func (a AskerAuthorizer) external(
	ctx context.Context, id *Identity, attrs authorizationv1.ResourceAttributes,
) (bool, error) {
	if a.PDP == nil {
		return false, nil
	}
	return a.PDP.Evaluate(ctx, *id, attrs.Namespace+"/"+attrs.Name)
}
