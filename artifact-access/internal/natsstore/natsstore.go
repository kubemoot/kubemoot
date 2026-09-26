// Package natsstore reads objects from a NATS JetStream Object Store bucket.
//
// Every Get runs on its OWN short-lived connection, opened and closed for that one
// read. This is deliberate: a single long-lived connection shared across many
// streaming Gets was observed to bleed chunks from a previously-read object into a
// later read (a read returned its object's bytes PLUS a stale earlier object's
// bytes, failing the digest), while a fresh connection always read the object
// correctly. Isolating each read on its own connection removes the shared state
// that allowed the bleed. Reads are infrequent (one referenced object per
// discussion), so a connection per read is a fine trade for correctness.
package natsstore

import (
	"context"
	"fmt"
	"io"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Store is the read interface to an Object Store bucket. It holds only config; each
// Get establishes and tears down its own connection.
type Store struct {
	url    string
	bucket string
}

// Open returns a Store for the given NATS URL and bucket. It does not hold a
// connection; connections are per-Get. The bucket is created by the producer
// (the agent-runtime), so the reader never needs to create it.
func Open(url, bucket string) *Store {
	return &Store{url: url, bucket: bucket}
}

// Get opens a fresh connection, streams one object, and returns a reader that closes
// the connection when the caller closes it. On any setup error the connection is
// closed before returning so nothing leaks.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	nc, err := nats.Connect(s.url, nats.Name("artifact-access-get"))
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", s.url, err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream: %w", err)
	}
	obs, err := js.ObjectStore(ctx, s.bucket)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("object store %q: %w", s.bucket, err)
	}
	res, err := obs.Get(ctx, key)
	if err != nil {
		nc.Close()
		return nil, err
	}
	return &connBoundResult{res: res, nc: nc}, nil
}

// connBoundResult ties an object read to the connection it was read on, so the
// connection lives exactly as long as the read and is torn down with it.
type connBoundResult struct {
	res jetstream.ObjectResult
	nc  *nats.Conn
}

func (c *connBoundResult) Read(p []byte) (int, error) { return c.res.Read(p) }

func (c *connBoundResult) Close() error {
	err := c.res.Close()
	c.nc.Close()
	return err
}
