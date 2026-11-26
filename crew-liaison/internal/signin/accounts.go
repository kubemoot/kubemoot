package signin

import (
	"context"
	"errors"
	"time"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const (
	// idleLifetime is how long a person's upstream token and an app's sign-in are kept
	// without being used. Every use starts it again.
	idleLifetime = 30 * 24 * time.Hour
	// renewBefore is how long before the upstream token's expiry the front renews it.
	renewBefore = 60 * time.Second
	// touchEvery bounds how often a use rewrites the record only to restart its idle time.
	touchEvery = time.Hour
)

// account is what the front keeps for one person and the upstream: the upstream's tokens
// and how long they may be kept. It is sealed before it reaches the store.
type account struct {
	Access  string `json:"a"`
	Refresh string `json:"r,omitempty"`
	// AccessExpires is when the upstream said the access token ends (unix), 0 when it did not.
	AccessExpires int64 `json:"x,omitempty"`
	// KeepUntil is the last moment the upstream allows the sign-in to be used (unix), 0
	// when no limit is known. The record is never kept past it.
	KeepUntil int64 `json:"k,omitempty"`
	// Used is when the record's idle time was last started (unix).
	Used int64 `json:"u"`
}

func (f *front) accountKey(subject string) string {
	return "account." + f.cfg.UpstreamName + "." + subject
}

// newAccount builds the record for tokens the upstream issued at a sign-in.
func (f *front) newAccount(tokens upstreamTokens) account {
	now := f.now()
	acct := account{Access: tokens.Access, Refresh: tokens.Refresh, Used: now.Unix()}
	if tokens.ExpiresIn > 0 {
		acct.AccessExpires = now.Unix() + tokens.ExpiresIn
	}
	acct.KeepUntil = f.keepUntil(tokens, acct)
	return acct
}

// keepUntil is the upstream's limit on a sign-in: what its answer says about the refresh
// token, else the configured lifetime, else, with no refresh token, the access token's end.
func (f *front) keepUntil(tokens upstreamTokens, acct account) int64 {
	now := f.now()
	switch {
	case tokens.Refresh != "" && tokens.refreshLifetime() > 0:
		return now.Unix() + tokens.refreshLifetime()
	case f.cfg.UpstreamTokenLifetime > 0:
		return now.Add(f.cfg.UpstreamTokenLifetime).Unix()
	case tokens.Refresh == "":
		return acct.AccessExpires
	}
	return 0
}

// renewed is the record after the upstream answered a refresh: new tokens, and the same
// limit unless the answer states a new one. An upstream that does not rotate its refresh
// token returns none, and the kept one stays.
func (f *front) renewed(old account, tokens upstreamTokens) account {
	acct := f.newAccount(tokens)
	if acct.Refresh == "" {
		acct.Refresh = old.Refresh
	}
	if tokens.refreshLifetime() == 0 {
		acct.KeepUntil = old.KeepUntil
	}
	return acct
}

// timeToLive is how long acct may still be kept: the idle time, cut at the upstream's limit.
func (f *front) timeToLive(acct account) time.Duration {
	ttl := idleLifetime
	if acct.KeepUntil > 0 {
		ttl = min(ttl, time.Unix(acct.KeepUntil, 0).Sub(f.now()))
	}
	return ttl
}

// saveAccount seals acct and keeps it under the person's key. A record whose time is over
// is deleted instead and reported as identity.ErrInvalid.
func (f *front) saveAccount(ctx context.Context, subject string, acct account) error {
	ttl := f.timeToLive(acct)
	if ttl <= 0 {
		f.forgetAccount(ctx, subject, "the upstream's limit was reached")
		return identity.ErrInvalid
	}
	return f.putSealed(ctx, f.accountKey(subject), acct, ttl)
}

// loadAccount returns the person's record. None kept, or one that does not open (its key
// was removed, or it was copied from another person), is identity.ErrInvalid: the person
// signs in again.
func (f *front) loadAccount(ctx context.Context, subject string) (account, error) {
	var acct account
	err := f.getSealed(ctx, f.accountKey(subject), &acct)
	if errors.Is(err, tokenstore.ErrNotFound) || errors.Is(err, errBadBlob) {
		return account{}, identity.ErrInvalid
	}
	return acct, err
}

// forgetAccount deletes the person's upstream token at once.
func (f *front) forgetAccount(ctx context.Context, subject, reason string) {
	if err := f.store.Delete(ctx, f.accountKey(subject)); err != nil {
		f.event("could not delete a kept token", "error", err.Error())
		return
	}
	f.event("kept token deleted", "reason", reason)
}

// touch restarts the record's idle time when it was last started long enough ago, so a
// person who keeps asking keeps their sign-in and each request does not cost a write.
func (f *front) touch(ctx context.Context, subject string, acct account) {
	if f.now().Sub(time.Unix(acct.Used, 0)) < touchEvery {
		return
	}
	acct.Used = f.now().Unix()
	if err := f.saveAccount(ctx, subject, acct); err != nil {
		f.event("could not restart a kept token's idle time")
	}
}

// due reports whether the upstream token should be renewed before it is used.
func (f *front) due(acct account) bool {
	return acct.AccessExpires > 0 && !f.now().Add(renewBefore).Before(time.Unix(acct.AccessExpires, 0))
}

// renewAccount asks the upstream for new tokens with the kept refresh token and keeps the
// answer. Concurrent requests of one person share one call, and a request that finds the
// record already renewed (it no longer holds stale's access token) uses it as it is. An
// upstream that refuses ends the sign-in: the record is deleted and the result is
// identity.ErrInvalid.
func (f *front) renewAccount(ctx context.Context, subject string, stale account) (account, error) {
	v, err, _ := f.renewals.Do(subject, func() (any, error) {
		current, err := f.loadAccount(ctx, subject)
		if err != nil || current.Access != stale.Access {
			return current, err
		}
		if current.Refresh == "" {
			f.forgetAccount(ctx, subject, "expired with no refresh token")
			return account{}, identity.ErrInvalid
		}
		tokens, err := f.redeemRefresh(ctx, current.Refresh)
		if errors.Is(err, errUpstreamRefused) {
			f.forgetAccount(ctx, subject, "the upstream refused to renew it")
			return account{}, identity.ErrInvalid
		}
		if err != nil {
			return account{}, err
		}
		next := f.renewed(current, tokens)
		return next, f.saveAccount(ctx, subject, next)
	})
	if err != nil {
		return account{}, err
	}
	return v.(account), nil
}

// putSealed seals v for key and writes it.
func (f *front) putSealed(ctx context.Context, key string, v any, ttl time.Duration) error {
	blob, err := f.sealer.Seal(purposeStored+key, v)
	if err != nil {
		return err
	}
	return f.store.Put(ctx, key, []byte(blob), ttl)
}

// getSealed reads key and opens it into v. The value opens only under the key it was sealed
// for.
func (f *front) getSealed(ctx context.Context, key string, v any) error {
	blob, err := f.store.Get(ctx, key)
	if err != nil {
		return err
	}
	return f.sealer.Open(purposeStored+key, string(blob), v)
}
