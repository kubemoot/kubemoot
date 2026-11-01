/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package coordchaos

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// NATSBus reads a crew's coordinator log and discussion stream into a Store with two
// ordered JetStream consumers, and reads thread leases from the lease bucket. It never
// publishes: the harness only reads the bus.
type NATSBus struct {
	nc      *nats.Conn
	js      jetstream.JetStream
	store   *Store
	ns      string
	crew    string
	events  *watcher
	discuss *watcher
}

// watcher is one ordered consumer and how far its handler got.
type watcher struct {
	cons      jetstream.Consumer
	ctx       jetstream.ConsumeContext
	processed atomic.Uint64
}

// DialBus connects to NATS at url and starts reading everything the crew's streams hold
// from since on.
func DialBus(ctx context.Context, url, namespace, crew string, since time.Time, opts ...nats.Option) (*NATSBus, error) {
	nc, err := nats.Connect(url, append([]nats.Option{nats.Name("coordinator-chaos")}, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("connecting to NATS at %s: %w", url, err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, err
	}
	b := &NATSBus{nc: nc, js: js, store: NewStore(), ns: namespace, crew: crew}
	if b.events, err = b.watch(ctx, streamCoordinator, coordinatorSubjectRoot+namespace+"."+crew+".>", since, b.onEvent); err != nil {
		b.Close()
		return nil, err
	}
	if b.discuss, err = b.watch(ctx, streamDiscuss, discussionSubjectRoot+namespace+"."+crew+".>", since, b.onMsg); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

func (b *NATSBus) watch(ctx context.Context, stream, subject string, since time.Time, h func(jetstream.Msg)) (*watcher, error) {
	cons, err := b.js.OrderedConsumer(ctx, stream, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{subject},
		DeliverPolicy:  jetstream.DeliverByStartTimePolicy,
		OptStartTime:   &since,
	})
	if err != nil {
		return nil, fmt.Errorf("reading stream %s: %w", stream, err)
	}
	w := &watcher{cons: cons}
	w.ctx, err = cons.Consume(func(m jetstream.Msg) {
		h(m)
		if md, err := m.Metadata(); err == nil {
			w.processed.Store(md.Sequence.Stream)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("consuming stream %s: %w", stream, err)
	}
	return w, nil
}

// Close stops the consumers and the connection.
func (b *NATSBus) Close() {
	for _, w := range []*watcher{b.events, b.discuss} {
		if w != nil && w.ctx != nil {
			w.ctx.Stop()
		}
	}
	b.nc.Close()
}

// Store returns the store the consumers fill.
func (b *NATSBus) Store() *Store { return b.store }

func (b *NATSBus) onEvent(m jetstream.Msg) {
	md, err := m.Metadata()
	if err != nil {
		return
	}
	var e Event
	if json.Unmarshal(m.Data(), &e) != nil {
		return
	}
	e.Seq, e.Seen = md.Sequence.Stream, time.Now()
	b.store.AddEvent(e)
}

// wireMsg is the part of a discussion message the harness reads.
type wireMsg struct {
	MessageID   string `json:"messageId"`
	ThreadID    string `json:"threadId"`
	AgentName   string `json:"agentName"`
	MessageType string `json:"messageType"`
	Channel     string `json:"channel"`
	Metadata    struct {
		ConversationID string `json:"conversationId"`
	} `json:"metadata"`
}

func (b *NATSBus) onMsg(m jetstream.Msg) {
	md, err := m.Metadata()
	if err != nil {
		return
	}
	var w wireMsg
	if json.Unmarshal(m.Data(), &w) != nil || w.ThreadID == "" {
		return
	}
	b.store.AddMsg(Msg{
		Seq: md.Sequence.Stream, Subject: m.Subject(), MessageID: w.MessageID, ThreadID: w.ThreadID,
		AgentName: w.AgentName, MessageType: w.MessageType, Channel: w.Channel,
		ConversationID: w.Metadata.ConversationID, Seen: time.Now(),
	})
}

// CatchUp returns once both consumers have handled everything their streams hold for the
// crew: the server has nothing pending for them and the handler reached the last
// sequence the server delivered.
func (b *NATSBus) CatchUp(ctx context.Context) error {
	for _, w := range []*watcher{b.events, b.discuss} {
		if err := w.caughtUp(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (w *watcher) caughtUp(ctx context.Context) error {
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		info, err := w.cons.Info(ctx)
		if err == nil && info.NumPending == 0 && w.processed.Load() >= info.Delivered.Stream {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for the streams to be read: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

// LeaseHolder returns the pod name stored under the thread's lease key.
func (b *NATSBus) LeaseHolder(ctx context.Context, threadID string) (string, error) {
	kv, err := b.js.KeyValue(ctx, defaultLeaseBucket)
	if err != nil {
		return "", fmt.Errorf("opening %s (is coordinator.lease on?): %w", defaultLeaseBucket, err)
	}
	e, err := kv.Get(ctx, b.ns+"."+b.crew+"."+threadID)
	if err != nil {
		return "", err
	}
	return string(e.Value()), nil
}
