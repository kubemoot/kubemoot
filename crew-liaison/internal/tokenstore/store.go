// Package tokenstore is where the liaison keeps what it must remember about a signed-in
// person: the token an outside service issued for them, and their sign-in at the door. It is
// one seam (Store) with a backend behind it. Values arrive already sealed (encrypted) by the
// caller, so a backend holds ciphertext only and never needs the keys.
//
// Every value has its own time to live. A value that is not written again within it is gone,
// which is how "kept only while it is used" is enforced by the store and not by a sweep.
package tokenstore

import (
	"context"
	"errors"
	"regexp"
	"time"
)

// Errors every backend returns.
var (
	// ErrNotFound means no value is kept under the key: never written, deleted or expired.
	ErrNotFound = errors.New("no value is kept under this key")
	// ErrExists means Create found a value already kept under the key.
	ErrExists = errors.New("a value is already kept under this key")
	// ErrBadKey means the key is not one a store accepts.
	ErrBadKey = errors.New(`a store key is 1 to 200 letters, digits, "-", "_" or "." and does not start or end with "."`)
	// ErrBadTTL means the time to live is not positive.
	ErrBadTTL = errors.New("a stored value needs a time to live above zero")
)

// Store keeps small values by key, each for its own time to live.
type Store interface {
	// Put writes value under key and (re)starts its time to live.
	Put(ctx context.Context, key string, value []byte, ttl time.Duration) error
	// Create writes value under key only when nothing is kept there, else ErrExists. It is
	// the one atomic check a caller has: the first of two concurrent Creates wins.
	Create(ctx context.Context, key string, value []byte, ttl time.Duration) error
	// Get returns the value under key, or ErrNotFound.
	Get(ctx context.Context, key string) ([]byte, error)
	// Delete removes the value under key at once. Deleting nothing is not an error.
	Delete(ctx context.Context, key string) error
}

// keyForm is the keys every backend accepts, so a key valid in one is valid in all.
var keyForm = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]{0,198}[A-Za-z0-9_-]$|^[A-Za-z0-9_-]$`)

// CheckKey returns ErrBadKey unless key is one every backend accepts.
func CheckKey(key string) error {
	if !keyForm.MatchString(key) {
		return ErrBadKey
	}
	return nil
}

// check is the validation every write shares.
func check(key string, ttl time.Duration) error {
	if err := CheckKey(key); err != nil {
		return err
	}
	if ttl <= 0 {
		return ErrBadTTL
	}
	return nil
}
