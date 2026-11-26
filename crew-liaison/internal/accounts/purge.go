package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/go-logr/logr"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
)

// Grace is how long records stay after the last crew stopped declaring their account: long
// enough for a crew that is edited or redeployed, short enough that nobody's token outlives the
// reason it was kept.
const Grace = 7 * 24 * time.Hour

// orphanPrincipal keys the marks of when an account was first seen without any declaration,
// apart from every person's records.
const orphanPrincipal = "orphan-mark"

// refStore is a store that can list its records by account without opening them.
type refStore interface {
	Refs(ctx context.Context, skip ...string) ([]tokenstore.Ref, error)
	DeleteRef(ctx context.Context, ref tokenstore.Ref) error
}

// orphanMark is what is kept for an account no crew declares.
type orphanMark struct {
	Since time.Time `json:"s"`
}

// Purger deletes the records of an account that no crew declares any more, once the last
// declaration has been gone for Grace. The service is not asked to revoke: the declaration, and
// with it the revocation endpoint, is gone. The sweep reads accounts by the name each record
// carries in plain, never a token, and logs only the account name and a count.
type Purger struct {
	Vault *Vault
	Decls Declarations
	Now   func() time.Time
	Log   logr.Logger
}

// Run makes one pass and returns how many records it deleted. A declaration that cannot be
// read stops the pass: nothing is purged on a guess.
func (p *Purger) Run(ctx context.Context) (int, error) {
	store, ok := p.Vault.store.(refStore)
	if !ok {
		return 0, nil
	}
	declared, err := p.Decls.All(ctx)
	if err != nil {
		return 0, err
	}
	live := map[string]bool{}
	for _, name := range declaredNames(declared) {
		live[name] = true
		if err := p.clearMark(ctx, name); err != nil {
			return 0, err
		}
	}
	refs, err := store.Refs(ctx, lockPrincipal, orphanPrincipal)
	if err != nil {
		return 0, err
	}
	byAccount := map[string][]tokenstore.Ref{}
	for _, ref := range refs {
		if !live[ref.Account] {
			byAccount[ref.Account] = append(byAccount[ref.Account], ref)
		}
	}
	total := 0
	for account, list := range byAccount {
		n, err := p.purgeIfDue(ctx, store, account, list)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// purgeIfDue starts the account's clock when it has none, and deletes its records when the
// clock has run Grace.
func (p *Purger) purgeIfDue(
	ctx context.Context, store refStore, account string, refs []tokenstore.Ref,
) (int, error) {
	since, err := p.markedSince(ctx, account)
	if err != nil {
		return 0, err
	}
	if p.Now().Sub(since) < Grace {
		return 0, nil
	}
	for i, ref := range refs {
		if err := store.DeleteRef(ctx, ref); err != nil {
			return i, err
		}
	}
	_ = p.Vault.store.Delete(ctx, orphanPrincipal, account)
	p.Log.Info("Records of an account no crew declares were deleted", "account", account, "records", len(refs))
	return len(refs), nil
}

// clearMark forgets the clock of an account a crew declares, when it has one: a later
// disappearance starts a new one.
func (p *Purger) clearMark(ctx context.Context, account string) error {
	if _, err := p.Vault.store.Get(ctx, orphanPrincipal, account); err != nil {
		if errors.Is(err, tokenstore.ErrNotFound) {
			return nil
		}
		return err
	}
	return p.Vault.store.Delete(ctx, orphanPrincipal, account)
}

// markedSince returns when the account was first seen undeclared, writing the mark now when
// there is none or the one kept cannot be read (it is repaired, so the clock does not wait for
// a broken value to expire). A lost race reads the winner's mark.
func (p *Purger) markedSince(ctx context.Context, account string) (time.Time, error) {
	rec, err := p.Vault.store.Get(ctx, orphanPrincipal, account)
	if err != nil && !errors.Is(err, tokenstore.ErrNotFound) {
		return time.Time{}, err
	}
	var mark orphanMark
	if err == nil {
		if json.Unmarshal(rec.Value, &mark) == nil && !mark.Since.IsZero() {
			return mark.Since, nil
		}
		return p.writeMark(ctx, account, tokenstore.AnyRevision)
	}
	return p.writeMark(ctx, account, tokenstore.MustNotExist)
}

// writeMark starts the account's clock now. When another replica created the mark first, that
// replica's mark is as good, so the conflict is not an error.
func (p *Purger) writeMark(ctx context.Context, account string, ifRevision uint64) (time.Time, error) {
	now := p.Now()
	raw, _ := json.Marshal(orphanMark{Since: now})
	_, err := p.Vault.store.Put(ctx, tokenstore.Record{Principal: orphanPrincipal, Account: account, Value: raw},
		2*Grace, ifRevision)
	if errors.Is(err, tokenstore.ErrConflict) {
		return now, nil
	}
	return now, err
}

// Purger returns the sweep that deletes the records of accounts no crew declares any more.
func (s *Service) Purger() *Purger {
	return &Purger{Vault: s.Vault, Decls: s.Decls, Now: s.Now, Log: s.Log}
}

// PurgeEvery is how often the liaison runs the sweep.
const PurgeEvery = time.Hour
