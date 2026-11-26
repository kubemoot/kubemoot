package tokenstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	// DefaultBucket is the key-value bucket the liaison keeps its values in.
	DefaultBucket = "kubemoot_liaison_tokens"
	// natsWait bounds one call to NATS, so a server that is away fails a request and does
	// not hold it.
	natsWait = 5 * time.Second
	// markerLife is how long NATS keeps the empty marker a deleted or expired key leaves.
	// The marker holds the key's name and no value.
	markerLife = time.Minute
	// maxValueBytes is the largest value the bucket accepts; a sealed record is far smaller.
	maxValueBytes = 16 << 10
	// maxBucketBytes caps the bucket. A full bucket refuses new writes until values expire.
	maxBucketBytes = 64 << 20
)

// NATS is a Store in a NATS JetStream key-value bucket: every liaison replica sees the same
// values and a restart loses none. Each value carries its own time to live as a message
// TTL, which the server enforces, and the bucket's own expiry is the longest any value can
// live whatever a caller asks for. Values are ciphertext sealed by the caller.
//
// The bucket is created, or brought to these limits, on first use, and again after a
// failure, so a liaison that starts while NATS is away works once NATS is back.
type NATS struct {
	js     jetstream.JetStream
	bucket string
	maxTTL time.Duration

	mu sync.Mutex
	kv jetstream.KeyValue
}

// NewNATS returns a store in bucket over conn. maxTTL is the longest any value is kept; it
// becomes the bucket's expiry. Nothing is asked of the server until the first use.
func NewNATS(conn *nats.Conn, bucket string, maxTTL time.Duration) (*NATS, error) {
	if bucket == "" {
		bucket = DefaultBucket
	}
	if maxTTL <= 0 {
		return nil, ErrBadTTL
	}
	js, err := jetstream.New(conn)
	if err != nil {
		return nil, fmt.Errorf("token store: %w", err)
	}
	return &NATS{js: js, bucket: bucket, maxTTL: maxTTL}, nil
}

// ready returns the bucket, creating it or bringing it to the store's limits when this
// process has not done so yet.
func (n *NATS) ready(ctx context.Context) (jetstream.KeyValue, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.kv != nil {
		return n.kv, nil
	}
	kv, err := n.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         n.bucket,
		Description:    "What the crew liaison keeps for signed-in people, sealed",
		History:        1,
		TTL:            n.maxTTL,
		LimitMarkerTTL: markerLife,
		MaxValueSize:   maxValueBytes,
		MaxBytes:       maxBucketBytes,
		Storage:        jetstream.FileStorage,
	})
	if err != nil {
		return nil, fmt.Errorf("token store: bucket %s: %w", n.bucket, err)
	}
	n.kv = kv
	return kv, nil
}

// Put implements Store. The write goes to the bucket's subject for the key with the time to
// live as the message's TTL, which the key-value API offers only for Create.
func (n *NATS) Put(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := check(key, ttl); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, natsWait)
	defer cancel()
	if _, err := n.ready(ctx); err != nil {
		return err
	}
	_, err := n.js.Publish(ctx, "$KV."+n.bucket+"."+key, value, jetstream.WithMsgTTL(wholeSeconds(ttl)))
	return wrap(err)
}

// Create implements Store.
func (n *NATS) Create(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := check(key, ttl); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, natsWait)
	defer cancel()
	kv, err := n.ready(ctx)
	if err != nil {
		return err
	}
	_, err = kv.Create(ctx, key, value, jetstream.KeyTTL(wholeSeconds(ttl)))
	if errors.Is(err, jetstream.ErrKeyExists) {
		return ErrExists
	}
	return wrap(err)
}

// Get implements Store.
func (n *NATS) Get(ctx context.Context, key string) ([]byte, error) {
	if err := CheckKey(key); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, natsWait)
	defer cancel()
	kv, err := n.ready(ctx)
	if err != nil {
		return nil, err
	}
	entry, err := kv.Get(ctx, key)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, wrap(err)
	}
	return entry.Value(), nil
}

// Delete implements Store. The value and its history are removed at once; the empty marker
// left behind goes after markerLife.
func (n *NATS) Delete(ctx context.Context, key string) error {
	if err := CheckKey(key); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, natsWait)
	defer cancel()
	kv, err := n.ready(ctx)
	if err != nil {
		return err
	}
	return wrap(kv.Purge(ctx, key, jetstream.PurgeTTL(markerLife)))
}

// wholeSeconds rounds a time to live up to the whole seconds NATS counts in, so a value is
// never dropped sooner than asked.
func wholeSeconds(ttl time.Duration) time.Duration {
	return ttl.Truncate(time.Second) + min(ttl%time.Second, 1)*time.Second
}

// wrap marks a NATS failure as the store's. The key is never part of the error.
func wrap(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("token store: %w", err)
}
