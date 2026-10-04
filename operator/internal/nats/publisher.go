/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package nats

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

var log = logf.Log.WithName("nats-publisher")

// errFmtObjectStore wraps a JetStream object-store lookup failure with the
// bucket name. Shared by the GetObject/ListObjects/DeleteObject handlers.
const errFmtObjectStore = "object store %s: %w"

// Publisher provides lazy NATS connection and publish capabilities.
// When NATS is not configured, all operations are graceful no-ops.
type Publisher struct {
	mu   sync.Mutex
	conn *nats.Conn
	url  string
}

// NewPublisher creates a publisher that connects lazily on first Publish call.
// The url parameter can be empty; if so, the NATS_URL env var is checked.
// If neither is set, the publisher becomes a no-op.
func NewPublisher(url string) *Publisher {
	if url == "" {
		url = os.Getenv("NATS_URL")
	}
	return &Publisher{url: url}
}

// connect establishes the connection lazily. Must be called under lock.
func (p *Publisher) connect() error {
	if p.conn != nil && p.conn.IsConnected() {
		return nil
	}

	opts := []nats.Option{
		nats.Name("kubemoot-operator"),
		nats.ReconnectWait(5 * time.Second),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.V(1).Info("NATS disconnected", "error", err)
			}
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			log.V(1).Info("NATS reconnected")
		}),
	}

	conn, err := nats.Connect(p.url, opts...)
	if err != nil {
		return err
	}

	p.conn = conn
	log.Info("Connected to NATS", "url", p.url)
	return nil
}

// Publish JSON-encodes the payload and publishes to the given subject.
// Returns nil (no-op) when NATS is not configured.
func (p *Publisher) Publish(subject string, payload interface{}) error {
	if p.url == "" {
		return nil // NATS not configured — graceful no-op
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.connect(); err != nil {
		log.V(1).Info("Failed to connect to NATS, skipping publish", "subject", subject, "error", err)
		return nil // Non-fatal — operator continues without NATS
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	if err := p.conn.Publish(subject, data); err != nil {
		log.V(1).Info("Failed to publish to NATS", "subject", subject, "error", err)
		return nil // Non-fatal
	}

	return nil
}

// Configured reports whether a NATS URL is set (false → the publisher is a no-op).
func (p *Publisher) Configured() bool {
	return p.url != ""
}

// Connected reports whether the NATS connection is currently live. It lazily
// (re)connects so a never-published operator still gets a truthful answer. Used
// by the component-status endpoint to report NATS health without poking ports.
func (p *Publisher) Connected() bool {
	if p.url == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.connect(); err != nil {
		return false
	}
	return p.conn != nil && p.conn.IsConnected()
}

// EnsureConsumer creates or updates a durable JetStream consumer on the given stream.
// Filter subjects define which messages the consumer receives.
// Returns nil (no-op) when NATS is not configured.
func (p *Publisher) EnsureConsumer(stream, consumerName string, filterSubjects []string) error {
	if p.url == "" {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.connect(); err != nil {
		log.V(1).Info("Failed to connect to NATS for consumer management", "error", err)
		return nil // Non-fatal
	}

	js, err := p.conn.JetStream()
	if err != nil {
		log.V(1).Info("Failed to get JetStream context", "error", err)
		return nil
	}

	config := &nats.ConsumerConfig{
		Durable:           consumerName,
		DeliverPolicy:     nats.DeliverNewPolicy,
		DeliverSubject:    nats.NewInbox(), // Push consumer — agent-runtime uses js.subscribe()
		AckPolicy:         nats.AckExplicitPolicy,
		AckWait:           120 * time.Second,
		MaxDeliver:        3,
		InactiveThreshold: 24 * time.Hour,
		FilterSubjects:    filterSubjects,
	}

	// Check if existing consumer is a pull consumer (no DeliverSubject).
	// If so, delete and recreate as push — AddConsumer won't change delivery mode.
	existing, err := js.ConsumerInfo(stream, consumerName)
	if err == nil && existing.Config.DeliverSubject == "" {
		log.Info("Migrating pull consumer to push consumer", "stream", stream, "consumer", consumerName)
		_ = js.DeleteConsumer(stream, consumerName)
	}

	_, err = js.AddConsumer(stream, config)
	if err != nil {
		log.Info("Failed to ensure JetStream consumer", "stream", stream, "consumer", consumerName, "error", err)
		return err
	}

	log.Info("Ensured JetStream consumer", "stream", stream, "consumer", consumerName, "filterSubjects", filterSubjects)
	return nil
}

// DeleteConsumer removes a durable JetStream consumer.
// Returns nil (no-op) when NATS is not configured or consumer doesn't exist.
func (p *Publisher) DeleteConsumer(stream, consumerName string) error {
	if p.url == "" {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.connect(); err != nil {
		log.V(1).Info("Failed to connect to NATS for consumer deletion", "error", err)
		return nil
	}

	js, err := p.conn.JetStream()
	if err != nil {
		log.V(1).Info("Failed to get JetStream context", "error", err)
		return nil
	}

	if err := js.DeleteConsumer(stream, consumerName); err != nil {
		// Not found is fine — consumer may already be deleted
		if err == nats.ErrConsumerNotFound {
			return nil
		}
		log.V(1).Info("Failed to delete JetStream consumer", "stream", stream, "consumer", consumerName, "error", err)
		return nil // Non-fatal
	}

	log.Info("Deleted JetStream consumer", "stream", stream, "consumer", consumerName)
	return nil
}

// GetKVValue reads a single key from a NATS KV bucket.
// Returns nil, nil when NATS is not configured, bucket not found, or key not found.
func (p *Publisher) GetKVValue(bucket, key string) ([]byte, error) {
	if p.url == "" {
		return nil, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.connect(); err != nil {
		log.V(1).Info("Failed to connect to NATS for KV read", "error", err)
		return nil, nil
	}

	js, err := p.conn.JetStream()
	if err != nil {
		log.V(1).Info("Failed to get JetStream context for KV read", "error", err)
		return nil, nil
	}

	kv, err := js.KeyValue(bucket)
	if err != nil {
		log.V(1).Info("KV bucket not found", "bucket", bucket, "error", err)
		return nil, nil
	}

	entry, err := kv.Get(key)
	if err != nil {
		log.V(1).Info("KV key not found", "bucket", bucket, "key", key, "error", err)
		return nil, nil
	}

	return entry.Value(), nil
}

// PutKVValue writes a value to a NATS KV bucket, creating the bucket if it does not exist.
// Returns nil (no-op) when NATS is not configured.
func (p *Publisher) PutKVValue(bucket, key string, value []byte) error {
	if p.url == "" {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.connect(); err != nil {
		log.V(1).Info("Failed to connect to NATS for KV write", "error", err)
		return nil // Non-fatal
	}

	js, err := p.conn.JetStream()
	if err != nil {
		log.V(1).Info("Failed to get JetStream context for KV write", "error", err)
		return nil
	}

	kv, err := js.KeyValue(bucket)
	if err != nil {
		// Bucket does not exist — create it
		kv, err = js.CreateKeyValue(&nats.KeyValueConfig{
			Bucket:  bucket,
			History: 1,
		})
		if err != nil {
			log.Info("Failed to create KV bucket", "bucket", bucket, "error", err)
			return err
		}
		log.Info("Created KV bucket", "bucket", bucket)
	}

	if _, err := kv.Put(key, value); err != nil {
		log.Info("Failed to write KV value", "bucket", bucket, "key", key, "error", err)
		return err
	}

	log.V(1).Info("Wrote KV value", "bucket", bucket, "key", key, "size", len(value))
	return nil
}

// ListKVKeys returns all keys in a NATS KV bucket.
// Returns nil (no-op) when NATS is not configured or bucket does not exist.
func (p *Publisher) ListKVKeys(bucket string) ([]string, error) {
	if p.url == "" {
		return nil, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.connect(); err != nil {
		log.V(1).Info("Failed to connect to NATS for KV list", "error", err)
		return nil, nil
	}

	js, err := p.conn.JetStream()
	if err != nil {
		log.V(1).Info("Failed to get JetStream context for KV list", "error", err)
		return nil, nil
	}

	kv, err := js.KeyValue(bucket)
	if err != nil {
		// Bucket not found is not an error — just empty.
		return nil, nil
	}

	keyLister, err := kv.ListKeys()
	if err != nil {
		log.V(1).Info("Failed to list KV keys", "bucket", bucket, "error", err)
		return nil, nil
	}
	defer func() { _ = keyLister.Stop() }()

	var keys []string
	for key := range keyLister.Keys() {
		keys = append(keys, key)
	}
	return keys, nil
}

// PurgeKVPrefix deletes every key in the bucket that begins with prefix.
// Returns the number deleted. Used to garbage-collect a crew's working-memory
// facts on Crew deletion (keys are <crew>.<topic>.<key>). No-op when NATS is
// unconfigured; missing bucket is not an error.
func (p *Publisher) PurgeKVPrefix(bucket, prefix string) (int, error) {
	if p.url == "" {
		return 0, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.connect(); err != nil {
		log.V(1).Info("Failed to connect to NATS for KV purge", "error", err)
		return 0, err
	}
	js, err := p.conn.JetStream()
	if err != nil {
		return 0, err
	}
	kv, err := js.KeyValue(bucket)
	if err != nil {
		// Bucket not found — nothing to purge.
		return 0, nil
	}
	keyLister, err := kv.ListKeys()
	if err != nil {
		return 0, err
	}
	defer func() { _ = keyLister.Stop() }()

	deleted := 0
	for key := range keyLister.Keys() {
		if strings.HasPrefix(key, prefix) {
			if err := kv.Delete(key); err != nil {
				log.V(1).Info("Failed to delete KV key during purge", "bucket", bucket, "key", key, "error", err)
				continue
			}
			deleted++
		}
	}
	return deleted, nil
}

// Subscribe registers a handler for messages on a subject (or wildcard).
// Returns the subscription the caller should Drain/Unsubscribe on shutdown.
// Returns (nil, nil) when NATS is not configured — callers should treat the
// nil subscription as "subscribe was a no-op, no messages will arrive".
//
// Uses core NATS — messages published while the subscriber is offline are
// lost. Use SubscribeDurable when the subscriber must survive restarts.
func (p *Publisher) Subscribe(subject string, handler nats.MsgHandler) (*nats.Subscription, error) {
	if p.url == "" {
		return nil, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.connect(); err != nil {
		log.V(1).Info("Failed to connect to NATS for subscribe", "subject", subject, "error", err)
		return nil, err
	}
	return p.conn.Subscribe(subject, handler)
}

// SubscribeDurable registers a handler on a JetStream durable push
// consumer. Survives subscriber restarts: messages published while the
// subscriber is offline are redelivered when it reconnects. The caller
// MUST call msg.Ack()/Nak() to control redelivery; messages whose
// AckWait (60s) elapses without ack are redelivered, up to MaxDeliver
// (5) attempts.
//
// stream is the JetStream stream name (e.g. KUBEMOOT_DISCUSS). subject
// is the filter the consumer applies (e.g. kubemoot.discuss.> or a
// narrower pattern). durable is the consumer name — must be stable
// across pod restarts for delivery position to resume correctly.
//
// Returns (nil, nil) when NATS is not configured. Returns a non-nil
// subscription only after the consumer is provisioned successfully.
func (p *Publisher) SubscribeDurable(stream, subject, durable string, handler nats.MsgHandler) (*nats.Subscription, error) {
	if p.url == "" {
		return nil, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.connect(); err != nil {
		log.V(1).Info("Failed to connect to NATS for durable subscribe", "subject", subject, "error", err)
		return nil, err
	}
	js, err := p.conn.JetStream()
	if err != nil {
		log.V(1).Info("Failed to get JetStream context for durable subscribe", "error", err)
		return nil, err
	}

	sub, err := js.Subscribe(
		subject,
		handler,
		nats.BindStream(stream),
		nats.Durable(durable),
		nats.ManualAck(),
		nats.AckWait(60*time.Second),
		nats.MaxDeliver(5),
		nats.DeliverNew(),
	)
	if err != nil {
		log.V(1).Info("Failed to create JetStream subscription", "stream", stream, "durable", durable, "error", err)
		return nil, err
	}
	log.Info("JetStream durable subscription created", "stream", stream, "subject", subject, "durable", durable)
	return sub, nil
}

// SubjectHasMessages reports whether the given stream contains at least one
// message on the given subject. Used by the scheduler to detect deleted /
// aged-out discussion threads before publishing a synthetic message that
// would otherwise resurrect them.
//
// Returns true when NATS is not configured (optimistic — without a check,
// callers should proceed as if the subject exists; the publish itself will
// no-op anyway). Returns false only when the JetStream API confirms the
// subject has no messages.
func (p *Publisher) SubjectHasMessages(stream, subject string) bool {
	if p.url == "" {
		return true
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.connect(); err != nil {
		log.V(1).Info("Failed to connect to NATS for subject existence check", "error", err)
		return true
	}

	js, err := p.conn.JetStream()
	if err != nil {
		log.V(1).Info("Failed to get JetStream context for subject existence check", "error", err)
		return true
	}

	_, err = js.GetLastMsg(stream, subject)
	if err == nil {
		return true
	}
	// nats.ErrMsgNotFound (and a couple of legacy spellings) indicate the
	// subject is genuinely empty. Treat any other error as "couldn't tell"
	// → optimistic true.
	if err == nats.ErrMsgNotFound {
		return false
	}
	log.V(1).Info("subject existence check returned unexpected error", "stream", stream, "subject", subject, "error", err)
	return true
}

// DeleteKVKey removes a key from a NATS KV bucket.
// Returns nil (no-op) when NATS is not configured.
func (p *Publisher) DeleteKVKey(bucket, key string) error {
	if p.url == "" {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.connect(); err != nil {
		log.V(1).Info("Failed to connect to NATS for KV delete", "error", err)
		return nil
	}

	js, err := p.conn.JetStream()
	if err != nil {
		log.V(1).Info("Failed to get JetStream context for KV delete", "error", err)
		return nil
	}

	kv, err := js.KeyValue(bucket)
	if err != nil {
		return nil // bucket doesn't exist → nothing to delete
	}

	if err := kv.Delete(key); err != nil {
		log.V(1).Info("Failed to delete KV key", "bucket", bucket, "key", key, "error", err)
		return nil // Non-fatal
	}

	log.V(1).Info("Deleted KV key", "bucket", bucket, "key", key)
	return nil
}

// Close gracefully drains and closes the NATS connection.
func (p *Publisher) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conn != nil {
		if err := p.conn.Drain(); err != nil {
			log.V(1).Info("NATS drain failed; closing the connection", "error", err)
			p.conn.Close()
		}
		p.conn = nil
	}
}

// ============================================================================
// Object Store — JetStream-backed binary artifact storage.
//
// Used by [[Kubemoot Fitness Suite Runner with NATS Object Store Artifacts]]
// to persist fitness-suite XLSX outputs in a bucket the operator writes and
// the dashboard reads via a backend handler.
//
// Buckets are created idempotently on first access. Each bucket carries a
// max-age TTL (passed via EnsureObjectStore) so old artifacts auto-clean.
// ============================================================================

// EnsureObjectStore returns a handle to the named bucket, creating it if
// missing. ttl is the per-object max-age — objects older than this are
// auto-pruned by JetStream. Pass 0 for no TTL.
//
// Returns (nil, nil) when NATS is not configured (graceful no-op for
// dev-without-NATS, mirroring the rest of Publisher). Callers should
// nil-check the returned handle before use.
func (p *Publisher) EnsureObjectStore(bucket string, ttl time.Duration) (nats.ObjectStore, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.url == "" {
		return nil, nil
	}
	if err := p.connect(); err != nil {
		return nil, err
	}
	js, err := p.conn.JetStream()
	if err != nil {
		return nil, err
	}
	if store, err := js.ObjectStore(bucket); err == nil {
		return store, nil
	}
	// Bucket missing — create.
	return js.CreateObjectStore(&nats.ObjectStoreConfig{
		Bucket:      bucket,
		Description: "Kubemoot fitness-suite artifacts (XLSX bytes)",
		TTL:         ttl,
	})
}

// PutObject writes data to (bucket, key). Best-effort: returns the error
// from JetStream so the caller can decide whether the operation is
// load-bearing (fitness suite continues even if the artifact write fails;
// it just leaves status.artifactRef nil).
//
// ttl is the bucket's max-age. The reconciler can request a per-suite
// retention via spec.artifactRetention; the bucket gets re-created with
// that TTL on first write if the bucket doesn't already exist.
//
// Returns (nil, nil) on error or NATS-unset; the returned *ObjectInfo
// (when non-nil) carries size + the digest the dashboard can show.
func (p *Publisher) PutObject(bucket, key string, data []byte, ttl time.Duration) (*nats.ObjectInfo, error) {
	store, err := p.EnsureObjectStore(bucket, ttl)
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, nil
	}
	info, err := store.PutBytes(key, data)
	if err != nil {
		return nil, fmt.Errorf("object store PutBytes(%s, %s): %w", bucket, key, err)
	}
	return info, nil
}

// GetObject reads (bucket, key) and returns the bytes. The dashboard
// backend uses this to stream XLSX bytes on download. Returns
// (nil, nil) when NATS is not configured.
func (p *Publisher) GetObject(bucket, key string) ([]byte, error) {
	p.mu.Lock()
	if p.url == "" {
		p.mu.Unlock()
		return nil, nil
	}
	if err := p.connect(); err != nil {
		p.mu.Unlock()
		return nil, err
	}
	conn := p.conn
	p.mu.Unlock()

	js, err := conn.JetStream()
	if err != nil {
		return nil, err
	}
	store, err := js.ObjectStore(bucket)
	if err != nil {
		return nil, fmt.Errorf(errFmtObjectStore, bucket, err)
	}
	return store.GetBytes(key)
}

// ListObjects returns the keys in the bucket whose names start with prefix.
// Used to enumerate a suite's per-iteration transcripts for on-demand report
// generation. Returns (nil, nil) when NATS is not configured.
func (p *Publisher) ListObjects(bucket, prefix string) ([]string, error) {
	p.mu.Lock()
	if p.url == "" {
		p.mu.Unlock()
		return nil, nil
	}
	if err := p.connect(); err != nil {
		p.mu.Unlock()
		return nil, err
	}
	conn := p.conn
	p.mu.Unlock()

	js, err := conn.JetStream()
	if err != nil {
		return nil, err
	}
	store, err := js.ObjectStore(bucket)
	if err != nil {
		return nil, fmt.Errorf(errFmtObjectStore, bucket, err)
	}
	infos, err := store.List()
	if err != nil {
		if errors.Is(err, nats.ErrNoObjectsFound) {
			return nil, nil // empty bucket: no keys
		}
		return nil, err
	}
	keys := make([]string, 0, len(infos))
	for _, info := range infos {
		if strings.HasPrefix(info.Name, prefix) {
			keys = append(keys, info.Name)
		}
	}
	return keys, nil
}

// DeleteObject removes a single object from the bucket. Returns nil when NATS is
// not configured (no-op) or the object is already absent, so a purge is
// idempotent. Used by the fitness artifact purge (controller purgeArtifacts).
func (p *Publisher) DeleteObject(bucket, key string) error {
	p.mu.Lock()
	if p.url == "" {
		p.mu.Unlock()
		return nil
	}
	if err := p.connect(); err != nil {
		p.mu.Unlock()
		return err
	}
	conn := p.conn
	p.mu.Unlock()

	js, err := conn.JetStream()
	if err != nil {
		return err
	}
	store, err := js.ObjectStore(bucket)
	if err != nil {
		return fmt.Errorf(errFmtObjectStore, bucket, err)
	}
	if err := store.Delete(key); err != nil && !errors.Is(err, nats.ErrObjectNotFound) {
		return fmt.Errorf("deleting object %s: %w", key, err)
	}
	return nil
}
