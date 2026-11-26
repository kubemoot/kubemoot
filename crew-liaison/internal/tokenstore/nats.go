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
	markerLife = time.Minute
	// maxValueBytes is the largest value the bucket accepts; a sealed record is far smaller.
	maxValueBytes = 16 << 10
	// maxBucketBytes caps the bucket. A full bucket refuses new writes until values expire.
	maxBucketBytes = 64 << 20
)

// NATS is a Backend in a NATS JetStream key-value bucket: every liaison replica sees the same
// values and a restart loses none. Each value carries its own time to live as a message TTL
// the server enforces, and the bucket's expiry is the longest any value can live whatever a
// caller asks for. One bucket, one key per (group, name): "<group>.<name>".
//
// The bucket is created, or brought to these limits, on first use and again after a failure,
// so a liaison that starts while NATS is away works once NATS is back.
type NATS struct {
	js     jetstream.JetStream
	bucket string
	maxTTL time.Duration

	mu sync.Mutex
	kv jetstream.KeyValue
}

// NewNATS returns a backend in bucket over conn. maxTTL becomes the bucket's expiry.
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

func natsKey(group, name string) string { return group + "." + name }

// Get implements Backend.
func (n *NATS) Get(ctx context.Context, group, name string) (Entry, error) {
	ctx, cancel := context.WithTimeout(ctx, natsWait)
	defer cancel()
	kv, err := n.ready(ctx)
	if err != nil {
		return Entry{}, err
	}
	e, err := kv.Get(ctx, natsKey(group, name))
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return Entry{}, ErrNotFound
	}
	if err != nil {
		return Entry{}, wrapNATS(err)
	}
	return Entry{Value: e.Value(), Revision: e.Revision()}, nil
}

// Put implements Backend. The compare-and-swap is the server's check that the key's last
// sequence is the revision the caller read.
func (n *NATS) Put(
	ctx context.Context, group, name string, value []byte, ttl time.Duration, ifRevision uint64,
) (uint64, error) {
	if ttl <= 0 {
		return 0, ErrBadTTL
	}
	ctx, cancel := context.WithTimeout(ctx, natsWait)
	defer cancel()
	kv, err := n.ready(ctx)
	if err != nil {
		return 0, err
	}
	key := natsKey(group, name)
	if ifRevision == MustNotExist {
		rev, err := kv.Create(ctx, key, value, jetstream.KeyTTL(wholeSeconds(ttl)))
		if errors.Is(err, jetstream.ErrKeyExists) {
			return 0, ErrConflict
		}
		return rev, wrapNATS(err)
	}
	opts := []jetstream.PublishOpt{jetstream.WithMsgTTL(wholeSeconds(ttl))}
	if ifRevision != AnyRevision {
		opts = append(opts, jetstream.WithExpectLastSequenceForSubject(ifRevision, "$KV."+n.bucket+"."+key))
	}
	ack, err := n.js.Publish(ctx, "$KV."+n.bucket+"."+key, value, opts...)
	if err != nil {
		var apiErr *jetstream.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode == jetstream.JSErrCodeStreamWrongLastSequence {
			return 0, ErrConflict
		}
		return 0, wrapNATS(err)
	}
	return ack.Sequence, nil
}

// Delete implements Backend. The value and its history are removed at once; the empty marker
// left behind goes after markerLife.
func (n *NATS) Delete(ctx context.Context, group, name string) error {
	ctx, cancel := context.WithTimeout(ctx, natsWait)
	defer cancel()
	kv, err := n.ready(ctx)
	if err != nil {
		return err
	}
	return wrapNATS(kv.Purge(ctx, natsKey(group, name), jetstream.PurgeTTL(markerLife)))
}

// Names implements Backend.
func (n *NATS) Names(ctx context.Context, group string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, natsWait)
	defer cancel()
	kv, err := n.ready(ctx)
	if err != nil {
		return nil, err
	}
	lister, err := kv.ListKeysFiltered(ctx, group+".*")
	if err != nil {
		return nil, wrapNATS(err)
	}
	var out []string
	prefix := group + "."
	for key := range lister.Keys() {
		if len(key) > len(prefix) {
			out = append(out, key[len(prefix):])
		}
	}
	_ = lister.Stop()
	return out, nil
}

// wrapNATS marks a NATS failure as the store's. The key is never part of the error.
func wrapNATS(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("token store: %w", err)
}
