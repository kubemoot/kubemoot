/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package nats

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/operator/internal/retention"
)

// kvEntriesTimeout bounds the wait for a bucket's current keys to arrive.
const kvEntriesTimeout = 60 * time.Second

// KVEntries lists the keys of a bucket that begin with prefix (which ends in a dot) and
// the time each was last written, without reading the values. A missing bucket or an
// unconfigured NATS lists nothing; every other failure is returned, so a caller never
// mistakes an unreachable store for an empty one.
func (p *Publisher) KVEntries(bucket, prefix string) ([]retention.KVEntry, error) {
	if p.url == "" {
		return nil, nil
	}
	kv, err := p.keyValue(bucket)
	if err != nil || kv == nil {
		return nil, err
	}
	watcher, err := kv.Watch(prefix+">", nats.MetaOnly(), nats.IgnoreDeletes())
	if err != nil {
		return nil, fmt.Errorf("watch %s %s: %w", bucket, prefix, err)
	}
	defer func() { _ = watcher.Stop() }()

	var out []retention.KVEntry
	timeout := time.After(kvEntriesTimeout)
	for {
		select {
		case e := <-watcher.Updates():
			if e == nil { // the current keys have all arrived
				return out, nil
			}
			if strings.HasPrefix(e.Key(), prefix) {
				out = append(out, retention.KVEntry{Key: e.Key(), Written: e.Created()})
			}
		case <-timeout:
			return nil, fmt.Errorf("listing %s %s timed out", bucket, prefix)
		}
	}
}

// jetStream returns a JetStream context, holding the publisher's lock only while it
// connects, so a slow stream operation does not block every other use of the publisher.
func (p *Publisher) jetStream() (nats.JetStreamContext, error) {
	if err := p.lockConnected(); err != nil {
		return nil, err
	}
	defer p.mu.Unlock()
	return p.conn.JetStream()
}

// keyValue opens a bucket. It returns nil, nil for a bucket that does not exist.
func (p *Publisher) keyValue(bucket string) (nats.KeyValue, error) {
	js, err := p.jetStream()
	if err != nil {
		return nil, err
	}
	kv, err := js.KeyValue(bucket)
	if errors.Is(err, nats.ErrBucketNotFound) {
		return nil, nil
	}
	return kv, err
}

// firstMessageTimeout bounds the wait for the first message a consumer is owed.
const firstMessageTimeout = 10 * time.Second

// PurgeStreamBefore removes the messages on a subject filter that were written before
// the time and returns how many it removed. A missing stream removes nothing. The server
// finds the first message at or after the time (a consumer that starts there); a
// message that cannot be read stops the purge before it removes anything.
func (p *Publisher) PurgeStreamBefore(stream, subject string, before time.Time) (int, error) {
	if p.url == "" {
		return 0, nil
	}
	js, err := p.jetStream()
	if err != nil {
		return 0, err
	}
	held, last, err := subjectCount(js, stream, subject)
	if err != nil || held == 0 {
		return 0, err
	}
	// Purge below the oldest message to keep; when every message is older, below the
	// next sequence the stream would assign, so a message written meanwhile is kept.
	req := &nats.StreamPurgeRequest{Subject: subject, Sequence: last + 1}
	kept, err := firstKeptSequence(js, stream, subject, before)
	if err != nil {
		return 0, err
	}
	if kept.found {
		req.Sequence = kept.seq
	}
	if err := js.PurgeStream(stream, req); err != nil {
		return 0, fmt.Errorf("purge %s %s: %w", stream, subject, err)
	}
	left, _, err := subjectCount(js, stream, subject)
	if err != nil {
		return 0, err
	}
	return max(0, held-left), nil
}

// keptSequence is the stream sequence of the oldest message a purge keeps.
type keptSequence struct {
	seq   uint64
	found bool
}

// firstKeptSequence asks the server for the first message on the subject written at or
// after the time. found is false when every message is older.
func firstKeptSequence(js nats.JetStreamContext, stream, subject string, at time.Time) (keptSequence, error) {
	// A pull consumer delivers nothing until asked, so its pending count is the number of
	// messages written at or after the time.
	ci, err := js.AddConsumer(stream, &nats.ConsumerConfig{
		DeliverPolicy:     nats.DeliverByStartTimePolicy,
		OptStartTime:      &at,
		FilterSubject:     subject,
		AckPolicy:         nats.AckNonePolicy,
		InactiveThreshold: time.Minute,
	})
	if err != nil {
		return keptSequence{}, fmt.Errorf("find the cutoff in %s: %w", stream, err)
	}
	defer func() { _ = js.DeleteConsumer(stream, ci.Name) }()
	if ci.NumPending == 0 {
		return keptSequence{}, nil
	}
	sub, err := js.PullSubscribe(subject, "", nats.Bind(stream, ci.Name))
	if err != nil {
		return keptSequence{}, fmt.Errorf("find the cutoff in %s: %w", stream, err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	msgs, err := sub.Fetch(1, nats.MaxWait(firstMessageTimeout))
	if err != nil {
		return keptSequence{}, fmt.Errorf("read the cutoff message in %s: %w", stream, err)
	}
	if len(msgs) == 0 {
		return keptSequence{}, fmt.Errorf("read the cutoff message in %s: the server sent none", stream)
	}
	meta, err := msgs[0].Metadata()
	if err != nil {
		return keptSequence{}, fmt.Errorf("read the cutoff message in %s: %w", stream, err)
	}
	return keptSequence{seq: meta.Sequence.Stream, found: true}, nil
}

// subjectCount is how many messages the stream holds on the subject filter and the
// stream's last sequence. A missing stream holds none.
func subjectCount(js nats.JetStreamContext, stream, subject string) (count int, last uint64, err error) {
	info, err := js.StreamInfo(stream, &nats.StreamInfoRequest{SubjectsFilter: subject})
	if errors.Is(err, nats.ErrStreamNotFound) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("stream %s: %w", stream, err)
	}
	n := 0
	for _, c := range info.State.Subjects {
		n += int(c) //nolint:gosec // a message count that fits an int
	}
	return n, info.State.LastSeq, nil
}
