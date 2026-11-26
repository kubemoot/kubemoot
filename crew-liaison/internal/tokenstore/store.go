// Package tokenstore keeps what the liaison must remember about a person between requests:
// the token an outside service issued for them, and the single-use marks of the door.
//
// Store is the one interface the rest of the liaison sees. Behind it, a Backend keeps small
// byte values by key with a time to live and compare-and-swap; a Sealed store wraps a
// backend and encrypts every value (AES-GCM, with the person and account as additional
// authenticated data), so a backend, and anyone who can read it, holds ciphertext only.
//
// Backends: NATS JetStream key-value (the default, nothing new to install), OpenBao (KV v2),
// and memory (tests and a single replica without NATS).
package tokenstore

import (
	"context"
	"errors"
	"math"
	"time"
)

// Errors every backend and store returns.
var (
	// ErrNotFound means nothing is kept under the key: never written, deleted or expired.
	ErrNotFound = errors.New("no value is kept under this key")
	// ErrConflict means a compare-and-swap found a different revision than the caller read.
	ErrConflict = errors.New("the value changed since it was read")
	// ErrBadTTL means the time to live is not positive.
	ErrBadTTL = errors.New("a stored value needs a time to live above zero")
	// ErrBadRecord means the record has no principal or no account.
	ErrBadRecord = errors.New("a record needs a principal and an account")
)

// Revision values a caller passes to Put.
const (
	// MustNotExist makes Put succeed only when nothing is kept under the key.
	MustNotExist uint64 = 0
	// AnyRevision makes Put overwrite whatever is kept.
	AnyRevision uint64 = math.MaxUint64
)

// Record is one person's value for one outside account. Value is the caller's own
// serialization (the token record); the store never looks inside it.
type Record struct {
	Principal string
	Account   string
	Value     []byte
	// Revision is set by Get and ListByPrincipal; pass it back to Put to swap only if
	// nothing else wrote in between.
	Revision uint64
}

// Store keeps records by (principal, account).
type Store interface {
	// Get returns the record, or ErrNotFound.
	Get(ctx context.Context, principal, account string) (Record, error)
	// Put writes the record for ttl and returns its new revision. ifRevision is the revision
	// the caller read, MustNotExist, or AnyRevision; a mismatch is ErrConflict. Writing
	// again (with the same value) is how a use refreshes the time to live.
	Put(ctx context.Context, rec Record, ttl time.Duration, ifRevision uint64) (uint64, error)
	// Delete removes one record at once. Deleting nothing is not an error.
	Delete(ctx context.Context, principal, account string) error
	// ListByPrincipal returns every record the person has.
	ListByPrincipal(ctx context.Context, principal string) ([]Record, error)
	// DeleteByPrincipal removes every record the person has and returns how many.
	DeleteByPrincipal(ctx context.Context, principal string) (int, error)
}

// Entry is a raw backend value with its revision.
type Entry struct {
	Value    []byte
	Revision uint64
}

// Backend keeps byte values under a group and a name, each for its own time to live. A
// group holds one person's records, so listing and deleting a person is one group. Group and
// name are lowercase hex, so every backend accepts them as a path or a subject token.
type Backend interface {
	Get(ctx context.Context, group, name string) (Entry, error)
	// Put follows Store.Put's ifRevision rules and returns the new revision.
	Put(ctx context.Context, group, name string, value []byte, ttl time.Duration, ifRevision uint64) (uint64, error)
	Delete(ctx context.Context, group, name string) error
	// Names lists the names kept in a group.
	Names(ctx context.Context, group string) ([]string, error)
	// Groups lists every group that holds a value, for the sweep that purges records no
	// declaration explains any more.
	Groups(ctx context.Context) ([]string, error)
}

// wholeSeconds rounds a time to live up to the whole seconds NATS and OpenBao count in, so a
// value is never dropped sooner than asked.
func wholeSeconds(ttl time.Duration) time.Duration {
	whole := ttl.Truncate(time.Second)
	if whole < ttl {
		whole += time.Second
	}
	return whole
}
