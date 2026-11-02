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
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/kubemoot/kubemoot/artifact-access/internal/store"

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
	nc, obs, err := s.session(ctx, "artifact-access-get")
	if err != nil {
		return nil, err
	}
	res, err := obs.Get(ctx, key)
	if err != nil {
		nc.Close()
		return nil, err
	}
	return &connBoundResult{res: res, nc: nc}, nil
}

// session opens a connection and the bucket on it. On error nothing is left open; on success the
// caller closes the connection.
func (s *Store) session(ctx context.Context, name string) (*nats.Conn, jetstream.ObjectStore, error) {
	nc, err := nats.Connect(s.url, nats.Name(name))
	if err != nil {
		return nil, nil, fmt.Errorf("connect %s: %w", s.url, err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, nil, fmt.Errorf("jetstream: %w", err)
	}
	obs, err := js.ObjectStore(ctx, s.bucket)
	if err != nil {
		nc.Close()
		return nil, nil, fmt.Errorf("object store %q: %w", s.bucket, err)
	}
	return nc, obs, nil
}

// Describe returns the size and headers of one object, on its own short-lived connection.
func (s *Store) Describe(ctx context.Context, key string) (store.Meta, error) {
	nc, obs, err := s.session(ctx, "artifact-access-describe")
	if err != nil {
		return store.Meta{}, err
	}
	defer nc.Close()
	info, err := obs.GetInfo(ctx, key)
	if err != nil {
		return store.Meta{}, err
	}
	return metaOf(info), nil
}

// List returns the live objects whose key starts with prefix, sorted by key, on its own
// short-lived connection. An empty bucket is an empty list, not an error.
func (s *Store) List(ctx context.Context, prefix string) ([]store.Meta, error) {
	nc, obs, err := s.session(ctx, "artifact-access-list")
	if err != nil {
		return nil, err
	}
	defer nc.Close()
	infos, err := obs.List(ctx)
	if err != nil && !errors.Is(err, jetstream.ErrNoObjectsFound) {
		return nil, fmt.Errorf("list %q: %w", prefix, err)
	}
	var out []store.Meta
	for _, info := range infos {
		if info != nil && !info.Deleted && strings.HasPrefix(info.Name, prefix) {
			out = append(out, metaOf(info))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// metaOf turns object info into Meta, keeping the first value of each header.
func metaOf(info *jetstream.ObjectInfo) store.Meta {
	m := store.Meta{Key: info.Name, Size: info.Size, Headers: make(map[string]string, len(info.Headers))}
	for k, v := range info.Headers {
		if len(v) > 0 {
			m.Headers[k] = v[0]
		}
	}
	return m
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
