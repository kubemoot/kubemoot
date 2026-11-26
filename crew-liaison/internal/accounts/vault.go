package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
)

// Vault keeps the tokens in a token store and applies the retention rules: each write sets
// the record's time to live to the idle life, cut at the service's limit.
type Vault struct {
	store tokenstore.Store
	regs  map[string]Registration
	now   func() time.Time
}

// NewVault returns a vault over store. regs are the install's registrations; the vault reads
// each account's MaxLifetime from them.
func NewVault(store tokenstore.Store, regs map[string]Registration, now func() time.Time) (*Vault, error) {
	if store == nil {
		return nil, errors.New("accounts: a vault needs a store")
	}
	if now == nil {
		now = time.Now
	}
	return &Vault{store: store, regs: regs, now: now}, nil
}

// TimeToLive is how long a record may still be kept: the idle life, cut at the service's
// limit. With no refresh token the access token's end is the limit. Zero or less means the
// record is over.
func (v *Vault) TimeToLive(account string, t Token) time.Duration {
	ttl := IdleLife
	now := v.now()
	if !t.KeepUntil.IsZero() {
		ttl = min(ttl, t.KeepUntil.Sub(now))
	}
	if t.Refresh == "" && !t.AccessExpires.IsZero() {
		ttl = min(ttl, t.AccessExpires.Sub(now))
	}
	if max := v.regs[account].MaxLifetime; max > 0 && !t.GrantedAt.IsZero() {
		ttl = min(ttl, t.GrantedAt.Add(max).Sub(now))
	}
	return ttl
}

// Get returns the token and the revision to swap against.
func (v *Vault) Get(ctx context.Context, principal, account string) (Token, uint64, error) {
	rec, err := v.store.Get(ctx, principal, account)
	if errors.Is(err, tokenstore.ErrNotFound) {
		return Token{}, 0, ErrNotConnected
	}
	if err != nil {
		return Token{}, 0, err
	}
	var t Token
	if json.Unmarshal(rec.Value, &t) != nil {
		return Token{}, 0, errors.New("accounts: a kept record is not readable")
	}
	return t, rec.Revision, nil
}

// Save writes the token. ifRevision is the revision read, tokenstore.MustNotExist or
// tokenstore.AnyRevision. A token whose time is already over is deleted instead and the
// result is ErrExpired.
func (v *Vault) Save(
	ctx context.Context, principal, account string, t Token, ifRevision uint64,
) (uint64, error) {
	ttl := v.TimeToLive(account, t)
	if ttl <= 0 {
		_ = v.store.Delete(ctx, principal, account)
		return 0, ErrExpired
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return 0, err
	}
	rev, err := v.store.Put(ctx, tokenstore.Record{Principal: principal, Account: account, Value: raw}, ttl, ifRevision)
	if errors.Is(err, tokenstore.ErrConflict) {
		return 0, tokenstore.ErrConflict
	}
	return rev, err
}

// Touch restarts the record's idle life after a use, at most once an hour so a person who
// keeps asking keeps the connection and each request does not cost a write. A lost race is
// not an error: another replica wrote a newer record.
func (v *Vault) Touch(ctx context.Context, principal, account string, t Token, rev uint64) {
	if v.now().Sub(t.LastUsed) < touchEvery {
		return
	}
	t.LastUsed = v.now()
	_, _ = v.Save(ctx, principal, account, t, rev)
}

// Disconnect deletes one connection at once.
func (v *Vault) Disconnect(ctx context.Context, principal, account string) error {
	return v.store.Delete(ctx, principal, account)
}

// SignOut deletes every connection the person has and returns how many.
func (v *Vault) SignOut(ctx context.Context, principal string) (int, error) {
	return v.store.DeleteByPrincipal(ctx, principal)
}

// List returns the person's connections, without any secret, by account name.
func (v *Vault) List(ctx context.Context, principal string) ([]Connection, error) {
	recs, err := v.store.ListByPrincipal(ctx, principal)
	if err != nil {
		return nil, err
	}
	out := make([]Connection, 0, len(recs))
	for _, rec := range recs {
		var t Token
		if json.Unmarshal(rec.Value, &t) != nil {
			continue
		}
		out = append(out, Connection{
			Account: rec.Account, State: stateOf(t), Scopes: t.Scopes, GrantedAt: t.GrantedAt, Crews: t.Crews,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Account < out[j].Account })
	return out, nil
}

func stateOf(t Token) string {
	if t.State == "" {
		return StateConnected
	}
	return t.State
}

// lockPrincipal keys the refresh leases, apart from every person's records.
const lockPrincipal = "refresh-lease"

// TryLock takes the right to refresh one record for ttl: the one atomic create the store has, so
// the first of any number of replicas wins. It reports false when another holds it.
func (v *Vault) TryLock(ctx context.Context, principal, account string, ttl time.Duration) (bool, error) {
	_, err := v.store.Put(ctx, tokenstore.Record{Principal: lockPrincipal, Account: principal + "|" + account}, ttl,
		tokenstore.MustNotExist)
	if errors.Is(err, tokenstore.ErrConflict) {
		return false, nil
	}
	return err == nil, err
}

// Unlock gives the right back at once. A failure is left to the time to live.
func (v *Vault) Unlock(ctx context.Context, principal, account string) {
	_ = v.store.Delete(ctx, lockPrincipal, principal+"|"+account)
}
