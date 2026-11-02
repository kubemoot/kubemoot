// Package store declares the one interface the artifact-access component reads an
// object through. Both consumers - the sandbox materializer and the read-ops MCP
// server - depend on this single declaration rather than each defining their own,
// so the contract cannot silently drift between them. natsstore.Store satisfies it
// structurally; tests supply their own fakes.
package store

import (
	"context"
	"io"
)

// Meta is what the store knows about an object besides its bytes: the key, the size and the
// NATS object headers (the first value of each), which the agent runtime sets on an object
// that holds one tool call's result.
type Meta struct {
	Key     string
	Size    uint64
	Headers map[string]string
}

// Describer returns the metadata of one object. A store that does not implement it serves
// objects without headers.
type Describer interface {
	Describe(ctx context.Context, key string) (Meta, error)
}

// Lister lists the live objects whose key starts with prefix, in key order.
type Lister interface {
	List(ctx context.Context, prefix string) ([]Meta, error)
}

// Getter streams an object's bytes by key. The returned reader must be closed.
type Getter interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}
