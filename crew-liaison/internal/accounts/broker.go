package accounts

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
)

// Declarations says what the crews declare. The liaison reads Crew resources to answer it.
type Declarations interface {
	// Account returns the account's declaration and the crews that declare it; false when no
	// crew does.
	Account(ctx context.Context, name string) (Declaration, bool, error)
	// All returns every account any crew declares, with the crews that declare each.
	All(ctx context.Context) ([]Declaration, error)
	// Declared returns every account a crew declares.
	Declared(ctx context.Context, crew string) ([]Declaration, error)
}

// Lease is what the broker hands a caller for one use: the token, when it ends, and the hosts
// it may be sent to. The caller uses it once and drops it.
type Lease struct {
	Token      string
	ExpiresAt  time.Time
	TokenHosts []string
}

// LeaseError is a refusal with the reason the gateways share.
type LeaseError struct {
	Reason string
	// Account and Crew say what the person would connect.
	Account string
}

func (e *LeaseError) Error() string { return "no lease: " + e.Reason }

// Broker leases a person's token to a crew's tools. It checks the crew declares the account,
// the install offers it, and the person consented to that crew; it refreshes an access token
// that is about to end, once however many callers ask, and writes a rotated refresh token
// before it hands out the new access token.
type Broker struct {
	vault  *Vault
	decls  Declarations
	regs   map[string]Registration
	client *Client
	now    func() time.Time

	mu      sync.Mutex
	flights map[string]*flight
}

// flight is one refresh in progress; waiters share its result.
type flight struct {
	done  chan struct{}
	lease Lease
	err   error
}

// NewBroker returns a broker.
func NewBroker(
	vault *Vault, decls Declarations, regs map[string]Registration, client *Client, now func() time.Time,
) *Broker {
	if now == nil {
		now = time.Now
	}
	return &Broker{
		vault: vault, decls: decls, regs: regs, client: client, now: now,
		flights: map[string]*flight{},
	}
}

// Lease returns a token for the person's account on behalf of crew ("namespace/name"). The
// reasons are the LeaseError's: NO_GRANT (nothing connected, or not offered to this crew),
// NEEDS_RECONNECT (the service no longer honors it, or the person never consented to this
// crew) and EXPIRED (the service's lifetime for it ended).
func (b *Broker) Lease(ctx context.Context, principal, account, crew string) (Lease, error) {
	d, reg, err := b.offered(ctx, account, crew)
	if err != nil {
		return Lease{}, &LeaseError{Reason: ReasonNoGrant, Account: account}
	}
	tok, rev, err := b.vault.Get(ctx, principal, account)
	if errors.Is(err, ErrNotConnected) {
		return Lease{}, &LeaseError{Reason: ReasonNoGrant, Account: account}
	}
	if err != nil {
		return Lease{}, err
	}
	if reason := b.refusal(tok, crew); reason != "" {
		return Lease{}, &LeaseError{Reason: reason, Account: account}
	}
	if !tok.KeepUntil.IsZero() && !b.now().Before(tok.KeepUntil) {
		_ = b.vault.Disconnect(ctx, principal, account)
		return Lease{}, &LeaseError{Reason: ReasonExpired, Account: account}
	}
	if tok.Access != "" && tok.AccessExpires.Sub(b.now()) > RenewBefore {
		b.vault.Touch(ctx, principal, account, tok, rev)
		return leaseOf(tok, d), nil
	}
	return b.renew(ctx, principal, account, d, reg)
}

// Check reports whether the person has a live connection a lease for crew could use, without
// asking the service or refreshing anything: "" when there is, else the lease reason.
func (b *Broker) Check(ctx context.Context, principal, account, crew string) (string, error) {
	if _, _, err := b.offered(ctx, account, crew); err != nil {
		return ReasonNoGrant, nil
	}
	tok, _, err := b.vault.Get(ctx, principal, account)
	if errors.Is(err, ErrNotConnected) {
		return ReasonNoGrant, nil
	}
	if err != nil {
		return "", err
	}
	if reason := b.refusal(tok, crew); reason != "" {
		return reason, nil
	}
	if !tok.KeepUntil.IsZero() && !b.now().Before(tok.KeepUntil) {
		return ReasonExpired, nil
	}
	return "", nil
}

// offered checks the crew declares the account and the install registered and allows it.
func (b *Broker) offered(ctx context.Context, account, crew string) (Declaration, Registration, error) {
	reg, ok := b.regs[account]
	if !ok || !reg.Allows(crew) {
		return Declaration{}, Registration{}, ErrNotAllowed
	}
	d, found, err := b.decls.Account(ctx, account)
	if err != nil || !found || !contains(d.Crews, crew) {
		return Declaration{}, Registration{}, ErrNotAllowed
	}
	return d, reg, nil
}

// refusal is the reason a connected token may not be used by this crew, or "".
func (b *Broker) refusal(tok Token, crew string) string {
	if tok.State == StateNeedsReconnect {
		return ReasonNeedsReconnect
	}
	if !contains(tok.Crews, crew) {
		return ReasonNeedsReconnect
	}
	return ""
}

func leaseOf(tok Token, d Declaration) Lease {
	return Lease{Token: tok.Access, ExpiresAt: tok.AccessExpires, TokenHosts: d.TokenHosts}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// renew refreshes under a per-record flight so concurrent callers share one call.
func (b *Broker) renew(
	ctx context.Context, principal, account string, d Declaration, reg Registration,
) (Lease, error) {
	key := principal + "\x00" + account
	b.mu.Lock()
	if f, ok := b.flights[key]; ok {
		b.mu.Unlock()
		select {
		case <-f.done:
			return f.lease, f.err
		case <-ctx.Done():
			return Lease{}, ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	b.flights[key] = f
	b.mu.Unlock()

	f.lease, f.err = b.refresh(ctx, principal, account, d, reg)

	b.mu.Lock()
	delete(b.flights, key)
	b.mu.Unlock()
	close(f.done)
	return f.lease, f.err
}

// refresh re-reads the record (another replica may have renewed it already), calls the
// service, and writes the answer with a compare-and-swap before returning it. A replica that
// lost the race re-reads and uses the winner's token instead of a refresh token already spent.
func (b *Broker) refresh(
	ctx context.Context, principal, account string, d Declaration, reg Registration,
) (Lease, error) {
	tok, rev, err := b.vault.Get(ctx, principal, account)
	if err != nil {
		return Lease{}, &LeaseError{Reason: ReasonNoGrant, Account: account}
	}
	if tok.Access != "" && tok.AccessExpires.Sub(b.now()) > RenewBefore {
		return leaseOf(tok, d), nil
	}
	if tok.Refresh == "" {
		return Lease{}, b.markReconnect(ctx, principal, account, tok, rev)
	}
	fresh, err := b.client.Refresh(ctx, d, reg, tok.Refresh)
	if errors.Is(err, ErrRefused) {
		return Lease{}, b.markReconnect(ctx, principal, account, tok, rev)
	}
	if err != nil {
		return Lease{}, err
	}
	next := b.renewed(tok, fresh)
	if _, err := b.vault.Save(ctx, principal, account, next, rev); err != nil {
		return b.afterLostSwap(ctx, principal, account, d, err)
	}
	return leaseOf(next, d), nil
}

// afterLostSwap handles a failed save: another replica won the swap, so its record is read
// and used; an expired record is the service's limit being reached.
func (b *Broker) afterLostSwap(
	ctx context.Context, principal, account string, d Declaration, err error,
) (Lease, error) {
	if errors.Is(err, ErrExpired) {
		return Lease{}, &LeaseError{Reason: ReasonExpired, Account: account}
	}
	if !errors.Is(err, tokenstore.ErrConflict) {
		return Lease{}, err
	}
	won, _, gerr := b.vault.Get(ctx, principal, account)
	if gerr != nil || won.Access == "" {
		return Lease{}, &LeaseError{Reason: ReasonNeedsReconnect, Account: account}
	}
	return leaseOf(won, d), nil
}

// renewed is the record after a refresh: new tokens, and the same limit unless the answer
// states a new one. A service that does not rotate its refresh token returns none, and the
// kept one stays.
func (b *Broker) renewed(old Token, fresh Tokens) Token {
	next := old
	next.Access = fresh.Access
	next.AccessExpires = time.Time{}
	if fresh.ExpiresIn > 0 {
		next.AccessExpires = b.now().Add(time.Duration(fresh.ExpiresIn) * time.Second)
	}
	if fresh.Refresh != "" {
		next.Refresh = fresh.Refresh
	}
	if life := fresh.RefreshLifetime(); life > 0 {
		next.KeepUntil = b.now().Add(life)
	}
	next.LastUsed = b.now()
	next.State = StateConnected
	return next
}

// markReconnect records that the service no longer honors the connection; the next lease
// is refused from the record without asking the service again.
func (b *Broker) markReconnect(ctx context.Context, principal, account string, tok Token, rev uint64) error {
	tok.State = StateNeedsReconnect
	tok.Access = ""
	_, _ = b.vault.Save(ctx, principal, account, tok, rev)
	return &LeaseError{Reason: ReasonNeedsReconnect, Account: account}
}
