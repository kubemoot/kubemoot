// Package accounts holds a person's connected outside accounts: the token an outside service
// issued so a crew's tools act as that person there. The liaison keeps the tokens, sealed,
// only while the install has turned this on, a crew declares the account and the person
// pressed Connect, and deletes them when the person disconnects or signs out, when they go
// unused for 30 days, and when the service's own lifetime for them ends.
package accounts

import (
	"errors"
	"time"
)

// Lifetimes.
const (
	// IdleLife is how long a record is kept without being used; every use starts it again.
	IdleLife = 30 * 24 * time.Hour
	// RenewBefore is how long before an access token ends the broker refreshes it.
	RenewBefore = 60 * time.Second
	// touchEvery bounds how often a use rewrites a record only to restart its idle time.
	touchEvery = time.Hour
)

// State of a connection.
const (
	StateConnected      = "connected"
	StateNeedsReconnect = "needs_reconnect"
	StateMissing        = "missing"
)

// Reasons a lease is refused, the words the discussion gateway and the MCP gateway share.
const (
	ReasonNoGrant        = "NO_GRANT"
	ReasonNeedsReconnect = "NEEDS_RECONNECT"
	ReasonExpired        = "EXPIRED"
)

// Errors.
var (
	// ErrNotConnected means the person has no connection to the account.
	ErrNotConnected = errors.New("the account is not connected")
	// ErrNeedsReconnect means the service no longer honors the connection.
	ErrNeedsReconnect = errors.New("the account needs to be connected again")
	// ErrExpired means the service's own lifetime for the connection ended.
	ErrExpired = errors.New("the connection ended at the service")
	// ErrDisabled means this install keeps no connected accounts.
	ErrDisabled = errors.New("connected accounts are not turned on")
	// ErrNotAllowed means the install does not offer the account to this crew.
	ErrNotAllowed = errors.New("the account is not available to this crew")
)

// Declaration is what a Crew says about an account it needs, plus the crews that say it. It
// names no install.
type Declaration struct {
	Name          string
	DisplayName   string
	Purpose       string
	Required      bool
	Scopes        []string
	AuthorizeURL  string
	TokenURL      string
	RevocationURL string
	TokenHosts    []string
	// Crews are the "namespace/name" of every crew that declares the account.
	Crews []string
}

// Registration is what the install supplies for one account: its client at the service.
type Registration struct {
	ClientID     string
	ClientSecret string
	// AllowedCrews limits the account to crews ("namespace/name" or "name"); empty is every
	// crew that declares it.
	AllowedCrews []string
	// Issuer is the service's issuer identifier, which its redirect's "iss" must equal (RFC 9207).
	// Empty accepts the host of the authorization endpoint.
	Issuer string
	// MaxLifetime is the longest the service lets a connection stand, from the connect. Zero
	// when the service states it in its answers or does not limit it.
	MaxLifetime time.Duration
}

// Token is what is kept for one person and one account. Every secret field is sealed by the
// store before it reaches a backend.
type Token struct {
	Scopes        []string  `json:"s,omitempty"`
	Refresh       string    `json:"r,omitempty"`
	Access        string    `json:"a,omitempty"`
	AccessExpires time.Time `json:"x,omitempty"`
	// KeepUntil is the last moment the service allows the connection to be used; zero when
	// no limit is known. The record is never kept past it.
	KeepUntil time.Time `json:"k,omitempty"`
	GrantedAt time.Time `json:"g"`
	LastUsed  time.Time `json:"u"`
	// Crews are the crews the person consented to use the account.
	Crews []string `json:"c,omitempty"`
	State string   `json:"t,omitempty"`
}

// Connection is one kept record as the person sees it: nothing secret.
type Connection struct {
	Account   string
	State     string
	Scopes    []string
	GrantedAt time.Time
	Crews     []string
}
