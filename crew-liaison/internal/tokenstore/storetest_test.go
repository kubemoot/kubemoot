package tokenstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// contract is the behavior every Store backend must show. advance moves the backend's clock
// forward (or waits, for a backend with a real one).
func contract(t *testing.T, s Store, advance func(time.Duration)) {
	t.Helper()
	ctx := context.Background()
	const key = "account.example.0123abcd"

	if _, err := s.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a key never written: %v", err)
	}
	for _, value := range []string{"one", "two"} {
		if err := s.Put(ctx, key, []byte(value), time.Hour); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Get(ctx, key); err != nil || string(got) != value {
			t.Fatalf("get after put %s: %q %v", value, got, err)
		}
	}
	removal(t, s, key)
	expiry(t, s, advance)
	renewal(t, s, advance)
}

// removal is the part of the contract about a kept value going away: Create refuses to
// write over it, Delete removes it at once, and then the key is free again.
func removal(t *testing.T, s Store, key string) {
	t.Helper()
	ctx := context.Background()
	if err := s.Create(ctx, key, []byte("three"), time.Hour); !errors.Is(err, ErrExists) {
		t.Fatalf("create over a kept value: %v", err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("deleting nothing: %v", err)
	}
	if err := s.Create(ctx, key, []byte("four"), time.Hour); err != nil {
		t.Fatalf("create after delete: %v", err)
	}
}

// expiry is the part of the contract about time: a value lives for its own time to live and
// no longer, and one that expired is as gone as one that was deleted.
func expiry(t *testing.T, s Store, advance func(time.Duration)) {
	t.Helper()
	ctx := context.Background()
	const short, long = "ttl.short", "ttl.long"
	if err := s.Put(ctx, short, []byte("s"), 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, long, []byte("l"), time.Hour); err != nil {
		t.Fatal(err)
	}
	advance(3 * time.Second)
	if _, err := s.Get(ctx, short); !errors.Is(err, ErrNotFound) {
		t.Errorf("a value outlived its time to live: %v", err)
	}
	if _, err := s.Get(ctx, long); err != nil {
		t.Errorf("a value with time left was dropped: %v", err)
	}
	if err := s.Create(ctx, short, []byte("again"), time.Hour); err != nil {
		t.Errorf("create over an expired value: %v", err)
	}
}

// renewal is the part of the contract that makes "kept while it is used" work: writing a
// value again starts its time to live again.
func renewal(t *testing.T, s Store, advance func(time.Duration)) {
	t.Helper()
	ctx := context.Background()
	const key = "ttl.renewed"
	for range 3 {
		if err := s.Put(ctx, key, []byte("v"), 3*time.Second); err != nil {
			t.Fatal(err)
		}
		advance(2 * time.Second)
		if _, err := s.Get(ctx, key); err != nil {
			t.Fatalf("a value written again within its time was dropped: %v", err)
		}
	}
	advance(2 * time.Second)
	if _, err := s.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Errorf("a value outlived its last write by more than its time to live: %v", err)
	}
}

// refusals is the input every backend must refuse before touching anything.
func refusals(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	for _, key := range []string{"", ".lead", "trail.", "has space", "star*", "gt>", "sl/ash", strings.Repeat("k", 201)} {
		if err := s.Put(ctx, key, []byte("v"), time.Hour); !errors.Is(err, ErrBadKey) {
			t.Errorf("put %q: %v", key, err)
		}
		if err := s.Create(ctx, key, []byte("v"), time.Hour); !errors.Is(err, ErrBadKey) {
			t.Errorf("create %q: %v", key, err)
		}
		if _, err := s.Get(ctx, key); !errors.Is(err, ErrBadKey) {
			t.Errorf("get %q: %v", key, err)
		}
		if err := s.Delete(ctx, key); !errors.Is(err, ErrBadKey) {
			t.Errorf("delete %q: %v", key, err)
		}
	}
	for _, ttl := range []time.Duration{0, -time.Second} {
		if err := s.Put(ctx, "ok", []byte("v"), ttl); !errors.Is(err, ErrBadTTL) {
			t.Errorf("put with ttl %v: %v", ttl, err)
		}
		if err := s.Create(ctx, "ok", []byte("v"), ttl); !errors.Is(err, ErrBadTTL) {
			t.Errorf("create with ttl %v: %v", ttl, err)
		}
	}
}
