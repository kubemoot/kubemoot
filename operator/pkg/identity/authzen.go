/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/netguard"
)

const (
	authzenTimeout = 5 * time.Second
	authzenMaxBody = 64 << 10

	defaultSubjectType  = "user"
	defaultResourceType = "crew"
	defaultAction       = "ask"
)

// AuthZENClient asks a policy decision point the access evaluation of the OpenID AuthZEN
// Authorization API 1.0: POST {subject, action, resource} and read {decision}. Only an HTTP 200
// whose decision is the JSON value true admits; every other outcome, an error included, is a
// refusal or a failed check, never an admission.
type AuthZENClient struct {
	cfg    ExternalConfig
	token  string
	client *http.Client
}

// NewAuthZENClient returns the client of cfg. token, when not empty, is sent as a bearer token;
// client, nil for the default, is what makes the call (redirects are never followed). The
// default dials public addresses only; NewGuardedAuthZENClient adds the install's host list.
func NewAuthZENClient(cfg ExternalConfig, token string, client *http.Client) (*AuthZENClient, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	cfg.SubjectType = orDefault(cfg.SubjectType, defaultSubjectType)
	cfg.ResourceType = orDefault(cfg.ResourceType, defaultResourceType)
	cfg.Action = orDefault(cfg.Action, defaultAction)
	if client == nil {
		client = netguard.PublicDialer().Client(authzenTimeout, nil)
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &AuthZENClient{cfg: cfg, token: token, client: &c}, nil
}

// PolicyGuard is what makes a call to a policy decision point safe to send a person's identity
// on: the hosts the install trusts, and the dialer every connection goes through.
type PolicyGuard struct {
	// Hosts are the install's trusted policy decision point hosts.
	Hosts PolicyHosts
	// Dialer, nil for the public-addresses-only system one, resolves and checks each address.
	Dialer *netguard.Dialer
	// TLS, nil for the default, is the TLS setting of the connection.
	TLS *tls.Config
}

// NewGuardedAuthZENClient is NewAuthZENClient for a rule a crew author wrote: the URL's host must
// be on the install's list (ErrPolicyHostNotTrusted otherwise), and the call dials only through
// the guard's dialer, so a private, loopback, link-local or cluster address is never reached,
// whatever name resolves to it. It uses no proxy and follows no redirect.
func NewGuardedAuthZENClient(cfg ExternalConfig, token string, guard PolicyGuard) (*AuthZENClient, error) {
	if !guard.Hosts.Trusts(cfg.URL) {
		return nil, ErrPolicyHostNotTrusted
	}
	dialer := guard.Dialer
	if dialer == nil {
		dialer = netguard.PublicDialer()
	}
	return NewAuthZENClient(cfg, token, dialer.Client(authzenTimeout, guard.TLS))
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

type authzenEntity struct {
	Type       string         `json:"type"`
	ID         string         `json:"id,omitempty"`
	Name       string         `json:"name,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

type authzenRequest struct {
	Subject  authzenEntity `json:"subject"`
	Action   authzenEntity `json:"action"`
	Resource authzenEntity `json:"resource"`
}

type authzenResponse struct {
	Decision *bool `json:"decision"`
}

// Evaluate implements AccessEvaluator.
func (c *AuthZENClient) Evaluate(ctx context.Context, subject Identity, resourceID string) (bool, error) {
	props := map[string]any{}
	if len(subject.Groups) > 0 {
		props["groups"] = subject.Groups
	}
	body, err := json.Marshal(authzenRequest{
		Subject:  authzenEntity{Type: c.cfg.SubjectType, ID: subject.Subject, Properties: props},
		Action:   authzenEntity{Name: c.cfg.Action},
		Resource: authzenEntity{Type: c.cfg.ResourceType, ID: resourceID},
	})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.decide(req)
}

func (c *AuthZENClient) decide(req *http.Request) (bool, error) {
	resp, err := c.client.Do(req)
	if err != nil {
		return false, errors.New("policy decision point unreachable")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("policy decision point answered %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, authzenMaxBody+1))
	if err != nil || len(raw) > authzenMaxBody {
		return false, errors.New("policy decision point answer unreadable")
	}
	var out authzenResponse
	if err := json.Unmarshal(raw, &out); err != nil || out.Decision == nil {
		return false, errors.New("policy decision point answer has no decision")
	}
	return *out.Decision, nil
}
