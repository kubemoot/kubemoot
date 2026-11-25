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
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/conversationexport"
	"github.com/kubemoot/kubemoot/operator/pkg/conversationexport/natssource"
	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory/natskv"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
	"github.com/kubemoot/kubemoot/operator/pkg/mesh"
	"github.com/kubemoot/kubemoot/operator/pkg/natsconn"
)

var log = logf.Log.WithName("nats-publisher")

// errFmtObjectStore wraps a JetStream object-store lookup failure with the
// bucket name. Shared by the GetObject/ListObjects/DeleteObject handlers.
const errFmtObjectStore = "object store %s: %w"

// errNotConfigured is returned by operations that need a connection when no NATS URL is set.
var errNotConfigured = errors.New("NATS is not configured")

// connectFmt wraps a connection failure.
const connectFmt = "connect NATS: %w"

// Publisher provides lazy NATS connection and publish capabilities.
// When NATS is not configured, all operations are graceful no-ops.
type Publisher struct {
	// mu guards conn and lastSent.
	mu sync.Mutex
	// dialMu serializes dials (ensureConnected), so two callers never open two connections.
	// Lock order: dialMu, then mu. Nothing takes dialMu while it holds mu. mu is not held
	// during a dial; it is taken under dialMu only to read or install conn.
	dialMu sync.Mutex
	conn   *nats.Conn
	url    string
	// conf holds the TLS and credential settings every component reads the same way
	// (pkg/natsconn); the zero value is a plain, anonymous connection.
	conf natsconn.Config
	// dialFn replaces the real dial in tests; nil dials with conf.
	dialFn func() (*nats.Conn, error)
	// lastSent holds, per subject, the hash of the payload last published through
	// PublishOnChange. It lives in memory only, so the first publish after an operator
	// restart always goes out.
	lastSent map[string][sha256.Size]byte
}

// NewPublisher creates a publisher that connects lazily on first Publish call.
// The url parameter can be empty; if so, the NATS_URL env var is checked.
// If neither is set, the publisher becomes a no-op.
func NewPublisher(url string) *Publisher {
	if url == "" {
		url = os.Getenv("NATS_URL")
	}
	conf := natsconn.FromEnv(os.Getenv)
	conf.URL = url
	return &Publisher{url: url, conf: conf}
}

// dial opens a connection that reconnects without end. It touches no Publisher state, so
// it can run with or without mu held.
func (p *Publisher) dial() (*nats.Conn, error) {
	if p.dialFn != nil {
		return p.dialFn()
	}
	return p.conf.Connect(
		nats.Name("kubemoot-operator"),
		nats.ReconnectWait(5*time.Second),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.V(1).Info("NATS disconnected", "error", err)
			}
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			log.V(1).Info("NATS reconnected")
		}),
	)
}

// ensureConnected makes sure a connection exists, dialing WITHOUT holding mu: a dial to a
// NATS that is down blocks for the dial timeout, and under mu that would stall every other
// use of the publisher for as long. It is the only place a connection is made; every
// operation reaches it through lockConnected. A connection that is reconnecting is kept:
// it reconnects without end (MaxReconnects -1), buffers publishes and replays its
// subscriptions when the server is back, so replacing it would leak it and its
// subscriptions. Only a closed connection is replaced. A connection that appeared while
// this dialed wins and the extra one is closed.
func (p *Publisher) ensureConnected() error {
	if p.hasLiveConn() {
		return nil
	}
	p.dialMu.Lock()
	defer p.dialMu.Unlock()
	if p.hasLiveConn() {
		return nil
	}
	conn, err := p.dial()
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil && !p.conn.IsClosed() {
		conn.Close()
		return nil
	}
	p.conn = conn
	log.Info("Connected to NATS", "url", p.url)
	return nil
}

// errConnectionLost is returned when the connection closed between the dial and its use.
var errConnectionLost = errors.New("the NATS connection closed before it could be used")

// lockConnected dials outside mu when there is no connection, then takes mu and returns
// with it HELD and a live connection in p.conn. On an error mu is not held. It never
// dials under mu: a connection that closed in between is an error the caller reports or
// its retry loop absorbs.
func (p *Publisher) lockConnected() error {
	if err := p.ensureConnected(); err != nil {
		return err
	}
	p.mu.Lock()
	if p.conn == nil || p.conn.IsClosed() {
		p.mu.Unlock()
		return errConnectionLost
	}
	return nil
}

// liveConn returns the live connection for a caller that works on it without holding mu.
func (p *Publisher) liveConn() (*nats.Conn, error) {
	if err := p.lockConnected(); err != nil {
		return nil, err
	}
	defer p.mu.Unlock()
	return p.conn, nil
}

// hasLiveConn reports whether the connection exists and is not closed (it may be
// reconnecting).
func (p *Publisher) hasLiveConn() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conn != nil && !p.conn.IsClosed()
}

// OpenCrewMemory returns a read-only reader of the crew memory bucket. Unlike
// ListKVKeys it reports every failure (NATS not configured, unreachable, bucket
// missing), so an export never mistakes an unreachable store for "no facts". It asks the
// server for the bucket without holding mu, so a slow answer does not stall publishes.
func (p *Publisher) OpenCrewMemory() (crewmemory.EntryReader, error) {
	if p.url == "" {
		return nil, errNotConfigured
	}
	conn, err := p.liveConn()
	if err != nil {
		return nil, fmt.Errorf(connectFmt, err)
	}
	return natskv.Open(conn)
}

// CrewMemoryKeys lists the keys of the crew memory bucket. Unlike ListKVKeys it reports
// every failure: a NATS that cannot be reached, a missing bucket and a list the server did
// not answer in time are bus.ErrNotReady, and any other failure is an error, so a caller
// never reads an unreachable bus as "no keys". It returns no keys and no error when NATS
// is not configured.
func (p *Publisher) CrewMemoryKeys() ([]string, error) {
	if p.url == "" {
		return nil, nil
	}
	reader, err := p.OpenCrewMemory()
	if err != nil {
		return nil, bus.AsNotReady(err)
	}
	keys, err := reader.ListKVKeys(crewmemory.Bucket)
	return keys, bus.AsNotReady(err)
}

// OpenConversationExport returns a builder of conversation exports over this publisher's
// NATS connection: it reads the conversation history, the discussion stream and the
// workspace, and the crew memory for the facts learned in the conversation. It reports
// every failure, like OpenCrewMemory.
func (p *Publisher) OpenConversationExport() (*conversationexport.Builder, error) {
	if p.url == "" {
		return nil, errNotConfigured
	}
	conn, err := p.liveConn()
	if err != nil {
		return nil, fmt.Errorf(connectFmt, err)
	}
	source, err := natssource.Open(conn)
	if err != nil {
		return nil, err
	}
	return &conversationexport.Builder{Source: source, Memory: p.OpenCrewMemory}, nil
}

// ConversationOwner reads the owner record of a conversation. found is false when the
// conversation has no record or its record cannot be read. Unlike GetKVValue it reports
// every failure to reach the store, so a caller can refuse instead of treating an
// unreachable store as "no owner". The read is not bounded by ctx: the NATS client's own
// timeouts apply.
func (p *Publisher) ConversationOwner(_ context.Context, scope crewscope.Scope, id string) (bus.Principal, bool, error) {
	if p.url == "" {
		return bus.Principal{}, false, errNotConfigured
	}
	if err := p.lockConnected(); err != nil {
		return bus.Principal{}, false, fmt.Errorf(connectFmt, err)
	}
	defer p.mu.Unlock()
	js, err := p.conn.JetStream()
	if err != nil {
		return bus.Principal{}, false, err
	}
	kv, err := js.KeyValue(bus.ConversationsBucket)
	if err != nil {
		return bus.Principal{}, false, err
	}
	entry, err := kv.Get(bus.ConversationOwnerKey(scope.Namespace, scope.Crew, id))
	if errors.Is(err, nats.ErrKeyNotFound) {
		return bus.Principal{}, false, nil
	}
	if err != nil {
		return bus.Principal{}, false, err
	}
	owner, ok := bus.DecodeConversationOwner(entry.Value())
	if !ok {
		log.Info("An owner record could not be read; the conversation is operator-only", "crew", scope.Crew)
	}
	return owner, ok, nil
}

// Publish JSON-encodes the payload and publishes to the given subject.
// Returns nil (no-op) when NATS is not configured.
func (p *Publisher) Publish(subject string, payload interface{}) error {
	if p.url == "" {
		return nil // NATS not configured - graceful no-op
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if !p.lockForSend(subject) {
		return nil // a failed send is non-fatal: the operator continues without NATS
	}
	defer p.mu.Unlock()
	p.sendLocked(subject, data)
	return nil
}

// PublishOnChange publishes the payload only when it differs from the last payload
// published on the same subject; an unchanged payload sends nothing. A reconcile that
// runs every minute therefore writes to the stream when the object's state changes, not
// on every pass. The payload must not carry timestamps or counters that change on their
// own. A payload that could not be sent (NATS down) is not remembered, so the next call
// tries again. The publish is buffered by the client, so a connection lost right after
// it can drop one change until the payload changes again; the stream records changes of
// state, and the object's own status is the source of truth. Returns nil (no-op) when
// NATS is not configured.
func (p *Publisher) PublishOnChange(subject string, payload interface{}) error {
	if p.url == "" {
		return nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)

	if !p.lockForSend(subject) {
		return nil
	}
	defer p.mu.Unlock()
	if last, ok := p.lastSent[subject]; ok && last == sum {
		return nil
	}
	if !p.sendLocked(subject, data) {
		return nil
	}
	if p.lastSent == nil {
		p.lastSent = map[string][sha256.Size]byte{}
	}
	p.lastSent[subject] = sum
	return nil
}

// ForgetPublished drops the remembered payload of a subject, for an object that was
// deleted: a later object with the same name publishes again.
func (p *Publisher) ForgetPublished(subject string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.lastSent, subject)
}

// lockForSend takes mu with a live connection for a publish on subject and reports whether
// it did; false means there is no connection and mu is not held.
func (p *Publisher) lockForSend(subject string) bool {
	if err := p.lockConnected(); err != nil {
		log.V(1).Info("Failed to connect to NATS, skipping publish", "subject", subject, "error", err)
		return false
	}
	return true
}

// sendLocked publishes data and reports whether it went out. Must be called under mu with
// a live connection (lockForSend).
func (p *Publisher) sendLocked(subject string, data []byte) bool {
	if err := p.conn.Publish(subject, data); err != nil {
		log.V(1).Info("Failed to publish to NATS", "subject", subject, "error", err)
		return false
	}
	return true
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
	if err := p.lockConnected(); err != nil {
		return false
	}
	defer p.mu.Unlock()
	return p.conn.IsConnected()
}

// EnsureConsumer creates or updates a durable JetStream consumer on the given stream.
// Filter subjects define which messages the consumer receives.
// Returns nil (no-op) when NATS is not configured.
func (p *Publisher) EnsureConsumer(stream, consumerName string, filterSubjects []string) error {
	if p.url == "" {
		return nil
	}

	if err := p.lockConnected(); err != nil {
		log.V(1).Info("Failed to connect to NATS for consumer management", "error", err)
		return nil // Non-fatal
	}
	defer p.mu.Unlock()

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
		AckWait:           mesh.ConsumerAckWait,
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

	if err := p.lockConnected(); err != nil {
		log.V(1).Info("Failed to connect to NATS for consumer deletion", "error", err)
		return nil
	}
	defer p.mu.Unlock()

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

	if err := p.lockConnected(); err != nil {
		log.V(1).Info("Failed to connect to NATS for KV read", "error", err)
		return nil, nil
	}
	defer p.mu.Unlock()

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

	if err := p.lockConnected(); err != nil {
		log.V(1).Info("Failed to connect to NATS for KV write", "error", err)
		return nil // Non-fatal
	}
	defer p.mu.Unlock()

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

// BucketsReady reports nil when NATS is reachable and every bucket exists. When it
// is not, the error wraps bus.ErrNotReady, so a caller can wait for the bus instead
// of treating the gap as empty data. Unlike ListKVKeys it never reads a missing
// bucket or an unreachable server as "no keys".
func (p *Publisher) BucketsReady(buckets ...string) error {
	if p.url == "" {
		return nil
	}
	if err := p.lockConnected(); err != nil {
		return fmt.Errorf("%w: connecting to NATS: %w", bus.ErrNotReady, err)
	}
	defer p.mu.Unlock()
	js, err := p.conn.JetStream()
	if err != nil {
		return bus.AsNotReady(fmt.Errorf("opening JetStream: %w", err))
	}
	for _, bucket := range buckets {
		if _, err := js.KeyValue(bucket); err != nil {
			return bus.AsNotReady(fmt.Errorf("KV bucket %s: %w", bucket, err))
		}
	}
	return nil
}

// ListKVKeys returns all keys in a NATS KV bucket.
// Returns nil (no-op) when NATS is not configured or bucket does not exist.
func (p *Publisher) ListKVKeys(bucket string) ([]string, error) {
	if p.url == "" {
		return nil, nil
	}

	if err := p.lockConnected(); err != nil {
		log.V(1).Info("Failed to connect to NATS for KV list", "error", err)
		return nil, nil
	}
	defer p.mu.Unlock()

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

	if err := p.lockConnected(); err != nil {
		log.V(1).Info("Failed to connect to NATS for KV purge", "error", err)
		return 0, err
	}
	defer p.mu.Unlock()
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

// PurgeSubject removes every message on the stream whose subject matches the
// filter (wildcards allowed). Unlike the best-effort helpers it returns every
// failure so a caller can retry. No-op when NATS is not configured; a missing
// stream is not an error.
func (p *Publisher) PurgeSubject(stream, filter string) error {
	if p.url == "" {
		return nil
	}
	if err := p.lockConnected(); err != nil {
		return err
	}
	defer p.mu.Unlock()
	js, err := p.conn.JetStream()
	if err != nil {
		return err
	}
	if err := js.PurgeStream(stream, &nats.StreamPurgeRequest{Subject: filter}); err != nil && !errors.Is(err, nats.ErrStreamNotFound) {
		return fmt.Errorf("purge %s %s: %w", stream, filter, err)
	}
	log.Info("Purged stream subject", "stream", stream, "filter", filter)
	return nil
}

// DeleteKVKeys deletes the exact keys from a bucket and returns every failure so
// a caller can retry. No-op when NATS is not configured; a missing bucket or key
// is not an error.
func (p *Publisher) DeleteKVKeys(bucket string, keys []string) error {
	if p.url == "" || len(keys) == 0 {
		return nil
	}
	if err := p.lockConnected(); err != nil {
		return err
	}
	defer p.mu.Unlock()
	js, err := p.conn.JetStream()
	if err != nil {
		return err
	}
	kv, err := js.KeyValue(bucket)
	if err != nil {
		return nil // bucket does not exist: nothing to delete
	}
	var errs []error
	for _, key := range keys {
		if err := kv.Delete(key); err != nil && !errors.Is(err, nats.ErrKeyNotFound) {
			errs = append(errs, fmt.Errorf("delete %s/%s: %w", bucket, key, err))
		}
	}
	return errors.Join(errs...)
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

	// Dial outside mu: a failed dial must not block publishes for the dial timeout.
	if err := p.lockConnected(); err != nil {
		log.V(1).Info("Failed to connect to NATS for subscribe", "subject", subject, "error", err)
		return nil, err
	}
	defer p.mu.Unlock()
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

	// Dial outside mu: a failed dial must not block publishes for the dial timeout.
	if err := p.lockConnected(); err != nil {
		log.V(1).Info("Failed to connect to NATS for durable subscribe", "subject", subject, "error", err)
		return nil, err
	}
	defer p.mu.Unlock()
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

	if err := p.lockConnected(); err != nil {
		log.V(1).Info("Failed to connect to NATS for subject existence check", "error", err)
		return true
	}
	defer p.mu.Unlock()

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

	if err := p.lockConnected(); err != nil {
		log.V(1).Info("Failed to connect to NATS for KV delete", "error", err)
		return nil
	}
	defer p.mu.Unlock()

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

// Close gracefully drains and closes the NATS connection. It waits for a dial in flight
// (at most the dial timeout) so the connection that dial makes is closed too.
func (p *Publisher) Close() {
	// dialMu first (the order ensureConnected uses), so a dial in flight finishes before the
	// connection is closed and cannot install a connection nothing closes.
	p.dialMu.Lock()
	defer p.dialMu.Unlock()
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
	if p.url == "" {
		return nil, nil
	}
	if err := p.lockConnected(); err != nil {
		return nil, err
	}
	defer p.mu.Unlock()
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
	if p.url == "" {
		return nil, nil
	}
	conn, err := p.liveConn()
	if err != nil {
		return nil, err
	}

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
	if p.url == "" {
		return nil, nil
	}
	conn, err := p.liveConn()
	if err != nil {
		return nil, err
	}

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
	if p.url == "" {
		return nil
	}
	conn, err := p.liveConn()
	if err != nil {
		return err
	}

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
