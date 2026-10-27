// Package store declares the one interface the artifact-access component reads an
// object through. Both consumers - the sandbox materializer and the read-ops MCP
// server - depend on this single declaration rather than each defining their own,
// so the contract cannot silently drift between them. natsstore.Store satisfies it
// structurally; tests supply their own fakes.
package store

import (
	"context"
	"errors"
	"io"
)

// ErrNotFound is wrapped by a Getter when no object exists under the key.
var ErrNotFound = errors.New("artifact not found")

// Getter streams an object's bytes by key. The returned reader must be closed.
type Getter interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}
