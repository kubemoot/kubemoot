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

// Store is the read interface to the Object Store buckets of a discussion's workspace. It
// holds only config; each Get establishes and tears down its own connection. With more than
// one bucket it reads them in order and takes an object from the first that has it, so a
// reader stays correct while writers move from the old bucket to the new one.
type Store struct {
	url     string
	buckets []string
}

// Open returns a Store for the given NATS URL and buckets, in the order they are tried. It
// does not hold a connection; connections are per-Get. The buckets are created by the
// producer (the agent-runtime), so the reader never needs to create them, and a bucket that
// does not exist yet is skipped.
func Open(url string, buckets ...string) *Store {
	return &Store{url: url, buckets: buckets}
}

// Buckets are the buckets the Store reads, in the order it tries them.
func (s *Store) Buckets() []string { return s.buckets }

// notFound is true for the errors that mean "not in this bucket": the bucket does not exist
// (no writer has created it) or it holds no such object.
func notFound(err error) bool {
	return errors.Is(err, jetstream.ErrBucketNotFound) || errors.Is(err, jetstream.ErrObjectNotFound)
}

// inFirstBucket runs try on each bucket in order and returns the first result that is not a
// not-found. When every bucket reports not-found it returns an object-not-found if any bucket
// gave one (the object is missing from a bucket that exists), else the first bucket's error, so a
// key missing everywhere reads as the object-not-found it is even when the first bucket does not
// exist yet.
func inFirstBucket[T any](buckets []string, try func(bucket string) (T, error)) (T, error) {
	var zero T
	var first, objectMissing error
	for _, bucket := range buckets {
		v, err := try(bucket)
		if err == nil || !notFound(err) {
			return v, err
		}
		if first == nil {
			first = err
		}
		if objectMissing == nil && errors.Is(err, jetstream.ErrObjectNotFound) {
			objectMissing = err
		}
	}
	switch {
	case objectMissing != nil:
		return zero, objectMissing
	case first != nil:
		return zero, first
	}
	return zero, errors.New("no workspace bucket is configured")
}

// Get opens one connection, streams one object from the first bucket that has it, and returns a
// reader that closes the connection when the caller closes it. On any setup error the connection is
// closed before returning so nothing leaks.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	nc, js, err := s.connect("artifact-access-get")
	if err != nil {
		return nil, err
	}
	rc, err := inFirstBucket(s.buckets, func(bucket string) (io.ReadCloser, error) {
		obs, err := objectStore(ctx, js, bucket)
		if err != nil {
			return nil, err
		}
		res, err := obs.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		return &connBoundResult{res: res, nc: nc}, nil
	})
	if err != nil {
		nc.Close()
		return nil, err
	}
	return rc, nil
}

// connect opens the one connection a call uses for every bucket it tries. On error nothing is left
// open; on success the caller closes the connection.
func (s *Store) connect(name string) (*nats.Conn, jetstream.JetStream, error) {
	nc, err := nats.Connect(s.url, nats.Name(name))
	if err != nil {
		return nil, nil, fmt.Errorf("connect %s: %w", s.url, err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, nil, fmt.Errorf("jetstream: %w", err)
	}
	return nc, js, nil
}

// objectStore opens one bucket on a connection.
func objectStore(ctx context.Context, js jetstream.JetStream, bucket string) (jetstream.ObjectStore, error) {
	obs, err := js.ObjectStore(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("object store %q: %w", bucket, err)
	}
	return obs, nil
}

// Describe returns the size and headers of one object, from the first bucket that has it, on its
// own short-lived connection.
func (s *Store) Describe(ctx context.Context, key string) (store.Meta, error) {
	nc, js, err := s.connect("artifact-access-describe")
	if err != nil {
		return store.Meta{}, err
	}
	defer nc.Close()
	return inFirstBucket(s.buckets, func(bucket string) (store.Meta, error) {
		obs, err := objectStore(ctx, js, bucket)
		if err != nil {
			return store.Meta{}, err
		}
		info, err := obs.GetInfo(ctx, key)
		if err != nil {
			return store.Meta{}, err
		}
		return metaOf(info), nil
	})
}

// List returns the live objects whose key starts with prefix, sorted by key, over one short-lived
// connection. An empty or missing bucket is an empty list, not an error; a key in more than one
// bucket is listed once, from the first bucket.
func (s *Store) List(ctx context.Context, prefix string) ([]store.Meta, error) {
	nc, js, err := s.connect("artifact-access-list")
	if err != nil {
		return nil, err
	}
	defer nc.Close()
	var out []store.Meta
	seen := map[string]bool{}
	for _, bucket := range s.buckets {
		metas, err := listBucket(ctx, js, bucket, prefix)
		if err != nil {
			return nil, err
		}
		for _, m := range metas {
			if !seen[m.Key] {
				seen[m.Key] = true
				out = append(out, m)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// listBucket lists one bucket; a bucket no writer has created yet lists as empty.
func listBucket(ctx context.Context, js jetstream.JetStream, bucket, prefix string) ([]store.Meta, error) {
	obs, err := objectStore(ctx, js, bucket)
	if errors.Is(err, jetstream.ErrBucketNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
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
	return out, nil
}

// metaOf turns object info into Meta, keeping the first value of each header, decoded (see
// store.DecodeHeaderValue).
func metaOf(info *jetstream.ObjectInfo) store.Meta {
	m := store.Meta{Key: info.Name, Size: info.Size, Headers: make(map[string]string, len(info.Headers))}
	for k, v := range info.Headers {
		if len(v) > 0 {
			m.Headers[k] = store.DecodeHeaderValue(v[0])
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
