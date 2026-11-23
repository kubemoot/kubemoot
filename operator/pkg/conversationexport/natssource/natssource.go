/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package natssource reads what Kubemoot's NATS stores still hold about one
// conversation: the turns in the conversation history, the receipts and messages in the discussion
// stream, and the files in the workspace. It only reads.
package natssource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/conversationexport"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
	"github.com/kubemoot/kubemoot/operator/pkg/mesh"
)

// The stores the source reads. The workspace bucket is the one tool results are
// written to; the artifact bucket is the one they were written to before it.
const (
	StreamName      = bus.DiscussStream
	HistoryBucket   = "kubemoot_conversations"
	WorkspaceBucket = mesh.WorkspaceBucket
	ArtifactBucket  = mesh.ArtifactBucket

	messageThreadClose = "thread_close"
	metaReceipt        = "receipt"
	readTimeout        = 30 * time.Second
	fetchBatch         = 500
)

var fileBuckets = []string{WorkspaceBucket, ArtifactBucket}

// Source reads a conversation from NATS.
type Source struct {
	js jetstream.JetStream
}

// New returns a source over js.
func New(js jetstream.JetStream) *Source { return &Source{js: js} }

// Open returns a source over an existing connection.
func Open(nc *nats.Conn) (*Source, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, err
	}
	return New(js), nil
}

var _ conversationexport.Source = (*Source)(nil)

// Held reads the conversation's turns, then the receipts and files of the threads that
// answered them. A conversation whose history is gone is ErrNotHeld: the history is what
// names its threads, so without it the receipts and files cannot be tied to it.
func (s *Source) Held(ctx context.Context, scope crewscope.Scope, id string) (conversationexport.Held, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	turns, err := s.turns(ctx, scope, id)
	if err != nil {
		return conversationexport.Held{}, err
	}
	held := conversationexport.Held{
		Turns: turns, Receipts: map[string][]byte{}, Messages: map[string][]conversationexport.Message{},
	}
	threads := threadsOf(turns)
	for _, thread := range threads {
		record, err := s.record(ctx, scope, thread)
		if err != nil {
			return conversationexport.Held{}, err
		}
		if record.receipt != nil {
			held.Receipts[thread] = record.receipt
		}
		if len(record.messages) > 0 {
			held.Messages[thread] = record.messages
		}
	}
	held.Files, err = s.files(ctx, scope, threads)
	return held, err
}

func threadsOf(turns []conversationexport.Turn) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, t := range turns {
		if _, dup := seen[t.ThreadID]; t.ThreadID != "" && !dup {
			seen[t.ThreadID] = struct{}{}
			out = append(out, t.ThreadID)
		}
	}
	return out
}

func (s *Source) turns(ctx context.Context, scope crewscope.Scope, id string) ([]conversationexport.Turn, error) {
	kv, err := s.js.KeyValue(ctx, HistoryBucket)
	if errors.Is(err, jetstream.ErrBucketNotFound) {
		return nil, conversationexport.ErrNotHeld
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", HistoryBucket, err)
	}
	entry, err := kv.Get(ctx, scope.ConversationPrefix()+id)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return nil, conversationexport.ErrNotHeld
	}
	if err != nil {
		return nil, fmt.Errorf("read the conversation: %w", err)
	}
	var history bus.ConversationHistory
	if err := json.Unmarshal(entry.Value(), &history); err != nil || len(history.Turns) == 0 {
		return nil, conversationexport.ErrNotHeld
	}
	out := make([]conversationexport.Turn, 0, len(history.Turns))
	for _, t := range history.Turns {
		at, _ := time.Parse(time.RFC3339, t.Timestamp)
		out = append(out, conversationexport.Turn{
			ThreadID: t.ThreadID, Question: t.Query, Answer: t.Response, AnsweredAt: at.UTC(),
		})
	}
	return out, nil
}

// threadRecord is what the discussion stream still holds of one thread: the receipt of its
// closing message, and every message in stream order.
type threadRecord struct {
	receipt  []byte
	messages []conversationexport.Message
}

// record reads the thread's messages in one pass of the thread's own subjects, as the
// asker's stream carried them. The receipt is nil when the stream no longer holds one. A
// thread id that is not one subject token is not looked up: it would widen the filter past
// the thread. A message published to two subjects is kept once.
func (s *Source) record(ctx context.Context, scope crewscope.Scope, thread string) (threadRecord, error) {
	var rec threadRecord
	if strings.ContainsAny(thread, ".*> \t\r\n") {
		return rec, nil
	}
	consumer, err := s.js.CreateConsumer(ctx, StreamName, jetstream.ConsumerConfig{
		AckPolicy:         jetstream.AckNonePolicy,
		DeliverPolicy:     jetstream.DeliverAllPolicy,
		FilterSubject:     scope.DiscussSubject("*", thread),
		InactiveThreshold: readTimeout,
	})
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		return rec, nil
	}
	if err != nil {
		return rec, fmt.Errorf("read the discussion stream: %w", err)
	}
	defer func() { _ = s.js.DeleteConsumer(context.WithoutCancel(ctx), StreamName, consumer.CachedInfo().Name) }()
	seen := map[string]struct{}{}
	err = readPending(ctx, consumer, func(m bus.DiscussionMessage) {
		if m.ThreadID != thread {
			return
		}
		if _, dup := seen[m.MessageID]; m.MessageID != "" && dup {
			return
		}
		seen[m.MessageID] = struct{}{}
		rec.messages = append(rec.messages, messageOf(m))
		if m.MessageType == messageThreadClose {
			rec.receipt = receiptOf(m, rec.receipt)
		}
	})
	return rec, err
}

func receiptOf(m bus.DiscussionMessage, previous []byte) []byte {
	if r, ok := m.Metadata[metaReceipt].(map[string]interface{}); ok && len(r) > 0 {
		if raw, err := json.MarshalIndent(r, "", "  "); err == nil {
			return raw
		}
	}
	return previous
}

func messageOf(m bus.DiscussionMessage) conversationexport.Message {
	at, _ := time.Parse(time.RFC3339Nano, m.Timestamp)
	return conversationexport.Message{
		ID: m.MessageID, Agent: m.AgentName, Signal: string(m.MessageType),
		Channel: m.Channel, Content: m.Content, At: at.UTC(),
	}
}

func readPending(ctx context.Context, c jetstream.Consumer, observe func(bus.DiscussionMessage)) error {
	pending := int(c.CachedInfo().NumPending)
	for pending > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch, err := c.Fetch(min(pending, fetchBatch), jetstream.FetchMaxWait(2*time.Second))
		if err != nil {
			return err
		}
		read := 0
		for msg := range batch.Messages() {
			read++
			var m bus.DiscussionMessage
			if json.Unmarshal(msg.Data(), &m) == nil {
				observe(m)
			}
		}
		if err := batch.Error(); err != nil && !errors.Is(err, jetstream.ErrNoMessages) {
			return err
		}
		if read == 0 {
			return nil
		}
		pending -= read
	}
	return nil
}

// files lists the workspace objects of the threads: keys <namespace>/<crew>/<thread>/<agent>/<object>.
func (s *Source) files(
	ctx context.Context, scope crewscope.Scope, threads []string,
) ([]conversationexport.File, error) {
	want := map[string]struct{}{}
	for _, t := range threads {
		want[t] = struct{}{}
	}
	seen := map[string]struct{}{}
	var out []conversationexport.File
	for _, bucket := range fileBuckets {
		found, err := s.bucketFiles(ctx, bucket, scope.Namespace+"/"+scope.Crew+"/", want, seen)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Thread+out[i].Name < out[j].Thread+out[j].Name })
	return out, nil
}

// bucketFiles lists the files of the wanted threads in one bucket, skipping keys already seen. A
// bucket that is not there holds none.
func (s *Source) bucketFiles(
	ctx context.Context, bucket, crewPrefix string, want, seen map[string]struct{},
) ([]conversationexport.File, error) {
	store, err := s.js.ObjectStore(ctx, bucket)
	if errors.Is(err, jetstream.ErrBucketNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", bucket, err)
	}
	infos, err := store.List(ctx)
	if err != nil && !errors.Is(err, jetstream.ErrNoObjectsFound) {
		return nil, fmt.Errorf("list %s: %w", bucket, err)
	}
	var out []conversationexport.File
	for _, info := range infos {
		thread, name, ok := fileOf(info.Name, crewPrefix)
		if _, mine := want[thread]; !ok || !mine {
			continue
		}
		if _, dup := seen[info.Name]; dup {
			continue
		}
		seen[info.Name] = struct{}{}
		out = append(out, conversationexport.File{Thread: thread, Name: name, Open: opener(store, info.Name)})
	}
	return out, nil
}

// fileOf splits an object key of the crew into its thread and <agent>/<object>.
func fileOf(key, crewPrefix string) (thread, name string, ok bool) {
	rest, ok := strings.CutPrefix(key, crewPrefix)
	if !ok || strings.Contains(key, "..") {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[0], parts[1] + "/" + parts[2], true
}

func opener(store jetstream.ObjectStore, key string) func(context.Context) (io.ReadCloser, error) {
	return func(ctx context.Context) (io.ReadCloser, error) {
		obj, err := store.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		return obj, nil
	}
}
