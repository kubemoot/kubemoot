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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/catransport"
)

// The connectedAccount rule admits a person only while they hold a live connection to the
// crew's account. The liaison keeps the connections; the gateway asks its account broker, as
// the crew, whether this person has one. The environment the operator writes for a gateway
// whose rule is connectedAccount.
const (
	// EnvAccountBrokerURL is the base address of the liaison's account broker.
	EnvAccountBrokerURL = "ACCOUNT_BROKER_URL"
	// EnvAccountBrokerTokenFile is the projected ServiceAccount token (audience
	// AccountBrokerAudience) the gateway presents. It is read on every call: the kubelet rotates it.
	EnvAccountBrokerTokenFile = "ACCOUNT_BROKER_TOKEN_FILE"
	// EnvAccountBrokerCAFile is the install CA's public certificates, which verify the broker's
	// serving certificate over https. It is read again when it changes.
	EnvAccountBrokerCAFile = "ACCOUNT_BROKER_CA_FILE"

	// AccountBrokerAudience is the audience of that token.
	AccountBrokerAudience = "kubemoot-account-broker"

	connectedPath    = "/internal/v1/accounts/connected"
	connectedTimeout = 5 * time.Second
	maxConnectedBody = 4096
)

// ErrConnectionCheck marks a connection check that could not be answered. The person is
// refused, and the caller may retry: it is not a statement that they are not connected.
var ErrConnectionCheck = errors.New("could not check the connected account")

// ConnectionChecker says whether a person has a live connection to an account of the crew.
type ConnectionChecker interface {
	// Connected reports whether the person (by the id the crews' gateways give them) holds a
	// live connection to the account.
	Connected(ctx context.Context, principalID, account string) (bool, error)
}

// BrokerConnections asks the liaison's account broker.
type BrokerConnections struct {
	base      string
	tokenFile string
	client    *http.Client
}

// NewBrokerConnections returns a checker for the broker at base. tokenFile holds the
// projected ServiceAccount token. client nil is a client with a timeout.
func NewBrokerConnections(base, tokenFile string, client *http.Client) (*BrokerConnections, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("%s must be an http or https address", EnvAccountBrokerURL)
	}
	if tokenFile == "" {
		return nil, fmt.Errorf("%s needs %s", EnvAccountBrokerURL, EnvAccountBrokerTokenFile)
	}
	if client == nil {
		client = &http.Client{Timeout: connectedTimeout}
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &BrokerConnections{base: strings.TrimRight(base, "/"), tokenFile: tokenFile, client: &c}, nil
}

// Connected implements ConnectionChecker. Nothing in an error carries the token.
func (b *BrokerConnections) Connected(ctx context.Context, principalID, account string) (bool, error) {
	resp, err := b.post(ctx, connectedPath, map[string]string{"principalId": principalID, "account": account})
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	return readConnected(resp)
}

// post sends one authenticated request to the broker. An error is always ErrConnectionCheck
// and never carries the gateway's own token.
func (b *BrokerConnections) post(ctx context.Context, path string, payload map[string]string) (*http.Response, error) {
	raw, err := os.ReadFile(b.tokenFile)
	token := strings.TrimSpace(string(raw))
	if err != nil || token == "" {
		return nil, fmt.Errorf("%w: the gateway's own token is not available", ErrConnectionCheck)
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, ErrConnectionCheck
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: the broker is not reachable", ErrConnectionCheck)
	}
	return resp, nil
}

func readConnected(resp *http.Response) (bool, error) {
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("%w: the broker answered %d", ErrConnectionCheck, resp.StatusCode)
	}
	var out struct {
		Connected *bool `json:"connected"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, maxConnectedBody)).Decode(&out) != nil || out.Connected == nil {
		return false, fmt.Errorf("%w: the broker's answer was not understood", ErrConnectionCheck)
	}
	return *out.Connected, nil
}

// connectionsFromEnv builds the checker of a gateway whose rule is connectedAccount; nil when
// no broker is configured.
func connectionsFromEnv(getenv func(string) string) (ConnectionChecker, error) {
	broker, err := brokerFromEnv(getenv)
	if broker == nil {
		return nil, err
	}
	return broker, nil
}

// brokerFromEnv builds the client of the account broker the environment names, or nil when none
// is named. An https broker is verified against the CA file in ACCOUNT_BROKER_CA_FILE when set,
// and otherwise against the system roots, which the install CA is not in, so the call fails closed.
func brokerFromEnv(getenv func(string) string) (*BrokerConnections, error) {
	base := strings.TrimSpace(getenv(EnvAccountBrokerURL))
	if base == "" {
		return nil, nil
	}
	var client *http.Client
	caFile := strings.TrimSpace(getenv(EnvAccountBrokerCAFile))
	if caFile != "" && strings.HasPrefix(strings.ToLower(base), "https://") {
		transport, err := catransport.New(caFile)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", EnvAccountBrokerCAFile, err)
		}
		client = &http.Client{Timeout: connectedTimeout, Transport: transport}
	}
	return NewBrokerConnections(base, strings.TrimSpace(getenv(EnvAccountBrokerTokenFile)), client)
}
