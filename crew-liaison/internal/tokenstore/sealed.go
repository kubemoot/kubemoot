package tokenstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sealer encrypts and opens a value under a purpose that binds it to one place. The
// liaison's signin.Sealer is one (AES-GCM, key ids for rotation).
type Sealer interface {
	Seal(purpose string, v any) (string, error)
	Open(purpose, blob string, v any) error
}

// sealedRecord is what is encrypted: the record, so a listing needs no key in the name.
type sealedRecord struct {
	Principal string `json:"p"`
	Account   string `json:"a"`
	Value     []byte `json:"v"`
}

// Sealed is a Store over a Backend that encrypts every value. The principal and account are
// additional authenticated data, so a value copied to another person's or account's key
// fails to open. The key names hash the person and the account, and the person never appears
// in the value. The value starts with the account's name in hex so a listing can name the
// records, so a backend learns which accounts a key holds (a name a crew declares, such as
// "library") but never whose they are.
type Sealed struct {
	backend Backend
	sealer  Sealer
	maxTTL  time.Duration
}

// NewSealed returns a store. maxTTL is the longest any record is kept, whatever a caller
// asks for.
func NewSealed(backend Backend, sealer Sealer, maxTTL time.Duration) (*Sealed, error) {
	if backend == nil || sealer == nil {
		return nil, errors.New("token store: a backend and a sealer are required")
	}
	if maxTTL <= 0 {
		return nil, ErrBadTTL
	}
	return &Sealed{backend: backend, sealer: sealer, maxTTL: maxTTL}, nil
}

// groupOf and nameOf are the backend key of a record.
func groupOf(principal string) string { return digest("principal", principal) }
func nameOf(account string) string    { return digest("account", account) }

func digest(kind, s string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + s))
	return hex.EncodeToString(sum[:16])
}

// purposeOf binds a sealed value to its person and account. Lengths keep two different
// pairs from ever producing the same text.
func purposeOf(principal, account string) string {
	return fmt.Sprintf("tokenstore:%d:%s:%d:%s", len(principal), principal, len(account), account)
}

func checkRecord(principal, account string) error {
	if principal == "" || account == "" {
		return ErrBadRecord
	}
	return nil
}

// Get implements Store.
func (s *Sealed) Get(ctx context.Context, principal, account string) (Record, error) {
	if err := checkRecord(principal, account); err != nil {
		return Record{}, err
	}
	e, err := s.backend.Get(ctx, groupOf(principal), nameOf(account))
	if err != nil {
		return Record{}, err
	}
	return s.open(principal, account, e)
}

func (s *Sealed) open(principal, account string, e Entry) (Record, error) {
	var sr sealedRecord
	_, blob, ok := splitValue(e.Value)
	if !ok || s.sealer.Open(purposeOf(principal, account), blob, &sr) != nil {
		return Record{}, errors.New("token store: a kept value does not open")
	}
	if sr.Principal != principal || sr.Account != account {
		return Record{}, errors.New("token store: a kept value does not open")
	}
	return Record{Principal: principal, Account: account, Value: sr.Value, Revision: e.Revision}, nil
}

// Put implements Store.
func (s *Sealed) Put(ctx context.Context, rec Record, ttl time.Duration, ifRevision uint64) (uint64, error) {
	if err := checkRecord(rec.Principal, rec.Account); err != nil {
		return 0, err
	}
	if ttl <= 0 {
		return 0, ErrBadTTL
	}
	ttl = min(ttl, s.maxTTL)
	blob, err := s.sealer.Seal(purposeOf(rec.Principal, rec.Account),
		sealedRecord{Principal: rec.Principal, Account: rec.Account, Value: rec.Value})
	if err != nil {
		return 0, errors.New("token store: the value could not be sealed")
	}
	value := []byte(hex.EncodeToString([]byte(rec.Account)) + "." + blob)
	return s.backend.Put(ctx, groupOf(rec.Principal), nameOf(rec.Account), value, ttl, ifRevision)
}

// Delete implements Store.
func (s *Sealed) Delete(ctx context.Context, principal, account string) error {
	if err := checkRecord(principal, account); err != nil {
		return err
	}
	return s.backend.Delete(ctx, groupOf(principal), nameOf(account))
}

// ListByPrincipal implements Store. A value that no longer opens (a key that was retired,
// a record that was written for another person) is left out, not returned as an error.
func (s *Sealed) ListByPrincipal(ctx context.Context, principal string) ([]Record, error) {
	if principal == "" {
		return nil, ErrBadRecord
	}
	names, err := s.backend.Names(ctx, groupOf(principal))
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, name := range names {
		e, err := s.backend.Get(ctx, groupOf(principal), name)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if rec, ok := s.openAny(principal, e); ok && nameOf(rec.Account) == name {
			out = append(out, rec)
		}
	}
	return out, nil
}

// openAny opens a value whose account is not known beforehand: the account is read from the
// value's plain prefix, then the open is bound to it, so a changed prefix fails to open.
func (s *Sealed) openAny(principal string, e Entry) (Record, bool) {
	account, _, ok := splitValue(e.Value)
	if !ok {
		return Record{}, false
	}
	rec, err := s.open(principal, account, e)
	return rec, err == nil
}

// splitValue separates the hex account prefix from the sealed blob.
func splitValue(v []byte) (account, blob string, ok bool) {
	prefix, blob, found := strings.Cut(string(v), ".")
	raw, err := hex.DecodeString(prefix)
	if !found || err != nil || len(raw) == 0 {
		return "", "", false
	}
	return string(raw), blob, true
}

// DeleteByPrincipal implements Store.
func (s *Sealed) DeleteByPrincipal(ctx context.Context, principal string) (int, error) {
	if principal == "" {
		return 0, ErrBadRecord
	}
	names, err := s.backend.Names(ctx, groupOf(principal))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, name := range names {
		if err := s.backend.Delete(ctx, groupOf(principal), name); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
