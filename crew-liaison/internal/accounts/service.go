package accounts

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// PrincipalID is the vault key of a person the door signed in: the id every place that
// compares a caller with an owner computes, from the user name the door's token carries.
func PrincipalID(user string) string {
	return identity.Hasher{}.ID(identity.IssuerKubernetes, user)
}

// Service is what the connect pages and the tools use: what an account is, who may connect
// it, finishing a connect, and disconnecting. It keeps nothing itself.
type Service struct {
	Vault  *Vault
	Decls  Declarations
	Regs   map[string]Registration
	Client *Client
	Now    func() time.Time
	// Broker leases tokens to the crews' gateways; nil when only the pages are in use.
	Broker *Broker
}

// Offer is an account as one person can connect it: the crew's declaration, the install's
// registration, and the crews that will use it.
type Offer struct {
	Declaration  Declaration
	Registration Registration
	// Crews are the crews that declare the account and that the install allows.
	Crews []string
}

// Offered returns the account as it can be connected. ErrNotAllowed when no crew declares
// it, the install did not register it, or none of the declaring crews is allowed.
func (s *Service) Offered(ctx context.Context, name string) (Offer, error) {
	reg, ok := s.Regs[name]
	if !ok {
		return Offer{}, ErrNotAllowed
	}
	d, found, err := s.Decls.Account(ctx, name)
	if err != nil {
		return Offer{}, err
	}
	if !found {
		return Offer{}, ErrNotAllowed
	}
	var crews []string
	for _, c := range d.Crews {
		if reg.Allows(c) {
			crews = append(crews, c)
		}
	}
	if len(crews) == 0 {
		return Offer{}, ErrNotAllowed
	}
	return Offer{Declaration: d, Registration: reg, Crews: crews}, nil
}

// OfferedFor is Offered for the account when the one crew named is among its crews.
func (s *Service) OfferedFor(ctx context.Context, name, crew string) (Offer, error) {
	o, err := s.Offered(ctx, name)
	if err != nil {
		return Offer{}, err
	}
	if !contains(o.Crews, crew) {
		return Offer{}, ErrNotAllowed
	}
	return o, nil
}

// Complete exchanges the service's code, keeps the tokens for the person, and returns the
// connection. consented are the crews the person saw on the consent page.
func (s *Service) Complete(
	ctx context.Context, principal string, o Offer, consented []string, code, verifier, redirect string,
) error {
	tokens, err := s.Client.Exchange(ctx, o.Declaration, o.Registration, code, verifier, redirect)
	if err != nil {
		return err
	}
	now := s.Now()
	tok := Token{
		Access: tokens.Access, Refresh: tokens.Refresh, GrantedAt: now, LastUsed: now,
		Scopes: scopesOf(tokens.Scope, o.Declaration.Scopes), Crews: consented, State: StateConnected,
	}
	if tokens.ExpiresIn > 0 {
		tok.AccessExpires = now.Add(time.Duration(tokens.ExpiresIn) * time.Second)
	}
	if life := tokens.RefreshLifetime(); life > 0 && tokens.Refresh != "" {
		tok.KeepUntil = now.Add(life)
	}
	_, err = s.Vault.Save(ctx, principal, o.Declaration.Name, tok, anyRevision)
	return err
}

// anyRevision overwrites a connection the person is connecting again.
const anyRevision = ^uint64(0)

func scopesOf(granted string, asked []string) []string {
	if f := strings.Fields(granted); len(f) > 0 {
		return f
	}
	return asked
}

// Outcome of a disconnect: whether the service was asked to revoke, and whether it did.
type Outcome struct {
	Asked   bool
	Revoked bool
}

// Disconnect asks the service to revoke the refresh token (RFC 7009) when the account
// declares an endpoint, then deletes the record whether or not the service answered.
func (s *Service) Disconnect(ctx context.Context, principal, account string) (Outcome, error) {
	var out Outcome
	tok, _, err := s.Vault.Get(ctx, principal, account)
	if err != nil && !errors.Is(err, ErrNotConnected) {
		return out, err
	}
	if err == nil {
		out = s.revoke(ctx, account, tok)
	}
	return out, s.Vault.Disconnect(ctx, principal, account)
}

// revoke tells the service, using the declaration as it stands now. A missing declaration or
// endpoint means the service was not asked.
func (s *Service) revoke(ctx context.Context, account string, tok Token) Outcome {
	d, found, err := s.Decls.Account(ctx, account)
	if err != nil || !found || d.RevocationURL == "" {
		return Outcome{}
	}
	secret, hint := tok.Refresh, "refresh_token"
	if secret == "" {
		secret, hint = tok.Access, "access_token"
	}
	if secret == "" {
		return Outcome{}
	}
	return Outcome{Asked: true, Revoked: s.Client.Revoke(ctx, d, s.Regs[account], secret, hint) == nil}
}

// Entry is one account in a person's overview.
type Entry struct {
	Account     string
	DisplayName string
	Purpose     string
	State       string
	Scopes      []string
	GrantedAt   time.Time
	Crews       []string
	Required    bool
}

// Overview lists every account the person's crews could use: the connected ones with their
// state and the ones not connected yet. Accounts the install does not offer are left out.
func (s *Service) Overview(ctx context.Context, principal string) ([]Entry, error) {
	declared, err := s.Decls.All(ctx)
	if err != nil {
		return nil, err
	}
	return s.overviewOf(ctx, principal, declared)
}

// SignOut deletes every connection the person has.
func (s *Service) SignOut(ctx context.Context, principal string) (int, error) {
	return s.Vault.SignOut(ctx, principal)
}

func (s *Service) overviewOf(ctx context.Context, principal string, declared []Declaration) ([]Entry, error) {
	conns, err := s.Vault.List(ctx, principal)
	if err != nil {
		return nil, err
	}
	byName := map[string]Connection{}
	for _, c := range conns {
		byName[c.Account] = c
	}
	var out []Entry
	for _, d := range declared {
		reg, ok := s.Regs[d.Name]
		if !ok || !anyAllowed(reg, d.Crews) {
			continue
		}
		e := Entry{
			Account: d.Name, DisplayName: d.DisplayName, Purpose: d.Purpose, State: StateMissing, Required: d.Required,
			Crews: allowedCrews(reg, d.Crews),
		}
		if c, ok := byName[d.Name]; ok {
			e.State, e.Scopes, e.GrantedAt, e.Crews = c.State, c.Scopes, c.GrantedAt, c.Crews
		}
		out = append(out, e)
	}
	return out, nil
}

func allowedCrews(reg Registration, crews []string) []string {
	var out []string
	for _, c := range crews {
		if reg.Allows(c) {
			out = append(out, c)
		}
	}
	return out
}

func anyAllowed(reg Registration, crews []string) bool {
	for _, c := range crews {
		if reg.Allows(c) {
			return true
		}
	}
	return false
}

// IssuerAccepted reports whether the "iss" a service sent on its redirect (RFC 9207) is the
// one expected: the registration's issuer when the install gave one, else any https address
// on the host of the account's authorization endpoint.
func IssuerAccepted(iss string, reg Registration, d Declaration) bool {
	if reg.Issuer != "" {
		return iss == reg.Issuer
	}
	got, err1 := url.Parse(iss)
	want, err2 := url.Parse(d.AuthorizeURL)
	return err1 == nil && err2 == nil && got.Scheme == "https" && got.Host == want.Host
}
