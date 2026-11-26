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
	"io"
	"net/http"
	"strings"
	"time"
)

// A crew's gateway leases a person's connected account token from the liaison's account broker
// for one tool call: it names the person by the id the crews' gateways give them and the
// account, and the broker names the crew from the gateway's own ServiceAccount.
const (
	leasePath     = "/internal/v1/accounts/lease"
	maxLeaseReply = 16 * 1024

	// EnvAccountCrew is the name of the crew the gateway serves, which the gateway needs to find
	// a conversation's owner when it leases an account token.
	EnvAccountCrew = "KUBEMOOT_CREW"
)

// The reasons a lease is refused, the words the liaison, the discussion gateway and the tool
// gateway share.
const (
	LeaseNoGrant        = "NO_GRANT"
	LeaseNeedsReconnect = "NEEDS_RECONNECT"
	LeaseExpired        = "EXPIRED"
)

// AccountLease is one person's access token for an outside service, for one call.
type AccountLease struct {
	// Token is the bearer token. Never log it, trace it or store it.
	Token string
	// ExpiresAt is when the token stops working.
	ExpiresAt time.Time
	// TokenHosts are the only hosts the token may be sent to.
	TokenHosts []string
}

// String never shows the token.
func (l AccountLease) String() string {
	return fmt.Sprintf("AccountLease{expiresAt: %s, tokenHosts: %v}", l.ExpiresAt.Format(time.RFC3339), l.TokenHosts)
}

// LeaseRefusal is the broker's answer that there is no token to lease, with why.
type LeaseRefusal struct {
	// Reason is LeaseNoGrant, LeaseNeedsReconnect or LeaseExpired.
	Reason string
}

func (e *LeaseRefusal) Error() string { return "no lease: " + e.Reason }

// AccountLeaser leases a person's token for an account of the crew the caller serves. An error
// that is not a *LeaseRefusal means the broker could not answer, which is retryable.
type AccountLeaser interface {
	Lease(ctx context.Context, principalID, account string) (AccountLease, error)
}

// Lease implements AccountLeaser. Nothing in an error carries a token.
func (b *BrokerConnections) Lease(ctx context.Context, principalID, account string) (AccountLease, error) {
	resp, err := b.post(ctx, leasePath, map[string]string{"principalId": principalID, "account": account})
	if err != nil {
		return AccountLease{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		return readLease(resp.Body)
	case http.StatusNotFound:
		return AccountLease{}, &LeaseRefusal{Reason: LeaseNoGrant}
	case http.StatusConflict:
		return AccountLease{}, &LeaseRefusal{Reason: LeaseNeedsReconnect}
	case http.StatusGone:
		return AccountLease{}, &LeaseRefusal{Reason: LeaseExpired}
	}
	return AccountLease{}, fmt.Errorf("%w: the broker answered %d", ErrConnectionCheck, resp.StatusCode)
}

// readLease decodes an answer that carries a token. An answer without a token, an end time or
// token hosts is not a lease.
func readLease(body io.Reader) (AccountLease, error) {
	var out struct {
		Token      string   `json:"token"`
		ExpiresAt  string   `json:"expiresAt"`
		TokenHosts []string `json:"tokenHosts"`
	}
	if json.NewDecoder(io.LimitReader(body, maxLeaseReply)).Decode(&out) != nil {
		return AccountLease{}, fmt.Errorf("%w: the broker's answer was not understood", ErrConnectionCheck)
	}
	end, err := time.Parse(time.RFC3339, out.ExpiresAt)
	if err != nil || strings.TrimSpace(out.Token) == "" || len(out.TokenHosts) == 0 {
		return AccountLease{}, fmt.Errorf("%w: the broker's answer was not understood", ErrConnectionCheck)
	}
	return AccountLease{Token: out.Token, ExpiresAt: end, TokenHosts: out.TokenHosts}, nil
}

// IsLeaseRefusal reports whether err is a refusal, and returns it.
func IsLeaseRefusal(err error) (*LeaseRefusal, bool) {
	var refusal *LeaseRefusal
	return refusal, errors.As(err, &refusal)
}

// AccountLeaserFromEnv builds the leaser of a gateway that has a broker address; nil when none
// is configured.
func AccountLeaserFromEnv(getenv func(string) string) (AccountLeaser, error) {
	base := strings.TrimSpace(getenv(EnvAccountBrokerURL))
	if base == "" {
		return nil, nil
	}
	return NewBrokerConnections(base, strings.TrimSpace(getenv(EnvAccountBrokerTokenFile)), nil)
}
