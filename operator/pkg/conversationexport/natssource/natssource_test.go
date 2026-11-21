/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package natssource

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/kubemoot/kubemoot/operator/pkg/conversationexport"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

const (
	thread2     = "th-2"
	keyThreadID = "threadId"
)

var scope = crewscope.Scope{Namespace: "ns", Crew: "c"}

// The fakes implement only what the source calls; any other method panics on the
// embedded nil interface, which would show the source reaching past a read.

type fakeEntry struct {
	jetstream.KeyValueEntry
	value []byte
}

func (e fakeEntry) Value() []byte { return e.value }

type fakeKV struct {
	jetstream.KeyValue
	entries map[string][]byte
	err     error
}

func (f fakeKV) Get(_ context.Context, key string) (jetstream.KeyValueEntry, error) {
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.entries[key]
	if !ok {
		return nil, jetstream.ErrKeyNotFound
	}
	return fakeEntry{value: v}, nil
}

type fakeMsg struct {
	jetstream.Msg
	data []byte
}

func (m fakeMsg) Data() []byte { return m.data }

type fakeBatch struct {
	jetstream.MessageBatch
	msgs []jetstream.Msg
}

func (b fakeBatch) Messages() <-chan jetstream.Msg {
	ch := make(chan jetstream.Msg, len(b.msgs))
	for _, m := range b.msgs {
		ch <- m
	}
	close(ch)
	return ch
}

func (fakeBatch) Error() error { return nil }

type fakeConsumer struct {
	jetstream.Consumer
	msgs []jetstream.Msg
}

func (c fakeConsumer) CachedInfo() *jetstream.ConsumerInfo {
	return &jetstream.ConsumerInfo{Name: "tmp", NumPending: uint64(len(c.msgs))}
}

func (c fakeConsumer) Fetch(int, ...jetstream.FetchOpt) (jetstream.MessageBatch, error) {
	return fakeBatch{msgs: c.msgs}, nil
}

type fakeResult struct {
	jetstream.ObjectResult
	r io.Reader
}

func (f fakeResult) Read(p []byte) (int, error) { return f.r.Read(p) }
func (fakeResult) Close() error                 { return nil }

type fakeObjects struct {
	jetstream.ObjectStore
	bodies map[string]string
}

func (f fakeObjects) List(context.Context, ...jetstream.ListObjectsOpt) ([]*jetstream.ObjectInfo, error) {
	out := make([]*jetstream.ObjectInfo, 0, len(f.bodies))
	for name := range f.bodies {
		out = append(out, &jetstream.ObjectInfo{ObjectMeta: jetstream.ObjectMeta{Name: name}})
	}
	return out, nil
}

func (f fakeObjects) Get(_ context.Context, name string, _ ...jetstream.GetObjectOpt) (jetstream.ObjectResult, error) {
	return fakeResult{r: strings.NewReader(f.bodies[name])}, nil
}

// fakeJS is a JetStream with a history bucket, a stream of messages per thread and two object stores.
type fakeJS struct {
	jetstream.JetStream
	kv        fakeKV
	kvErr     error
	messages  map[string][]jetstream.Msg // by thread, the last token of the filter
	streamErr error
	objects   map[string]fakeObjects // by bucket
	filters   []string
}

func (f *fakeJS) KeyValue(_ context.Context, bucket string) (jetstream.KeyValue, error) {
	if f.kvErr != nil || bucket != HistoryBucket {
		return nil, errOr(f.kvErr, jetstream.ErrBucketNotFound)
	}
	return f.kv, nil
}

func errOr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

func (f *fakeJS) CreateConsumer(_ context.Context, stream string, cfg jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	f.filters = append(f.filters, cfg.FilterSubject)
	thread := cfg.FilterSubject[strings.LastIndex(cfg.FilterSubject, ".")+1:]
	return fakeConsumer{msgs: f.messages[thread]}, nil
}

func (f *fakeJS) DeleteConsumer(context.Context, string, string) error { return nil }

func (f *fakeJS) ObjectStore(_ context.Context, bucket string) (jetstream.ObjectStore, error) {
	o, ok := f.objects[bucket]
	if !ok {
		return nil, jetstream.ErrBucketNotFound
	}
	return o, nil
}

func history(turns ...map[string]string) []byte {
	b, _ := json.Marshal(map[string]any{"lastAccessed": "2026-11-20T19:00:00Z", "turns": turns})
	return b
}

func turn(thread, q, a string) map[string]string {
	return map[string]string{"query": q, "response": a, keyThreadID: thread, "timestamp": "2026-11-20T19:03:00Z"}
}

func closeMsg(thread string, receipt map[string]any) jetstream.Msg {
	b, _ := json.Marshal(map[string]any{
		keyThreadID: thread, "messageType": "thread_close", "metadata": map[string]any{"receipt": receipt},
	})
	return fakeMsg{data: b}
}

func synthesisMsg(thread string) jetstream.Msg {
	b, _ := json.Marshal(map[string]any{keyThreadID: thread, "messageType": "synthesis", "content": "answer"})
	return fakeMsg{data: b}
}

func newJS() *fakeJS {
	return &fakeJS{
		kv: fakeKV{entries: map[string][]byte{
			"conv.ns.c.conv-1": history(turn("th-1", "q1", "a1"), turn(thread2, "q2", "a2")),
		}},
		messages: map[string][]jetstream.Msg{
			"th-1":  {synthesisMsg("th-1"), closeMsg("th-1", map[string]any{"tokens": 5})},
			thread2: {synthesisMsg(thread2)}, // no thread_close: its receipt is gone
		},
		objects: map[string]fakeObjects{
			WorkspaceBucket: {bodies: map[string]string{
				"ns/c/th-1/obs/result-1": "table",
				"ns/c/th-9/obs/result-2": "another conversation's",
				"ns/other/th-1/obs/leak": "another crew's",
				"ns/c/th-1/too/deep/x":   "not a workspace key",
			}},
			ArtifactBucket: {bodies: map[string]string{"ns/c/th-2/obs/old": "older bucket"}},
		},
	}
}

func TestHeldReadsTurnsReceiptsAndFilesOfTheConversation(t *testing.T) {
	held, err := New(newJS()).Held(context.Background(), scope, "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(held.Turns) != 2 || held.Turns[0].Question != "q1" || held.Turns[1].ThreadID != thread2 || held.Turns[0].AnsweredAt.IsZero() {
		t.Errorf("turns = %+v", held.Turns)
	}
	if !strings.Contains(string(held.Receipts["th-1"]), `"tokens": 5`) {
		t.Errorf("receipts = %v", held.Receipts)
	}
	if _, ok := held.Receipts[thread2]; ok {
		t.Error("a thread with no thread_close has a receipt")
	}
	assertFiles(t, held.Files)
}

func assertFiles(t *testing.T, files []conversationexport.File) {
	t.Helper()
	if len(files) != 2 || files[0].Thread != "th-1" || files[0].Name != "obs/result-1" || files[1].Thread != thread2 {
		t.Fatalf("files = %+v, want the two workspace files of this conversation's threads", files)
	}
	rc, err := files[0].Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(rc); string(body) != "table" {
		t.Errorf("body = %q", body)
	}
}

func TestReceiptsAreReadOnlyFromTheCrewsOwnThreadSubjects(t *testing.T) {
	js := newJS()
	if _, err := New(js).Held(context.Background(), scope, "conv-1"); err != nil {
		t.Fatal(err)
	}
	for _, f := range js.filters {
		if !strings.HasPrefix(f, "kubemoot.discuss.ns.c.*.th-") || strings.Contains(f, ">") {
			t.Errorf("filter %q reaches past the crew's thread", f)
		}
	}
}

func TestAThreadIdThatWouldWidenTheFilterIsNotLookedUp(t *testing.T) {
	js := newJS()
	js.kv.entries["conv.ns.c.conv-1"] = history(turn(">", "q", "a"), turn("a.b", "q", "a"), turn("x y", "q", "a"))
	held, err := New(js).Held(context.Background(), scope, "conv-1")
	if err != nil || len(held.Receipts) != 0 || len(js.filters) != 0 {
		t.Errorf("err = %v, receipts = %v, filters = %v", err, held.Receipts, js.filters)
	}
}

func TestAConversationWhoseHistoryIsGoneIsNotHeld(t *testing.T) {
	js := newJS()
	for name, id := range map[string]string{"no key": "gone", "other crew's key": "conv-2"} {
		js.kv.entries["conv.ns.other.conv-2"] = history(turn("th-1", "q", "a"))
		if _, err := New(js).Held(context.Background(), scope, id); !errors.Is(err, conversationexport.ErrNotHeld) {
			t.Errorf("%s: err = %v, want ErrNotHeld", name, err)
		}
	}
	js.kv.entries["conv.ns.c.empty"] = history()
	js.kv.entries["conv.ns.c.broken"] = []byte("not json")
	for _, id := range []string{"empty", "broken"} {
		if _, err := New(js).Held(context.Background(), scope, id); !errors.Is(err, conversationexport.ErrNotHeld) {
			t.Errorf("%s: err = %v, want ErrNotHeld", id, err)
		}
	}
}

func TestAMissingHistoryBucketIsNotHeld(t *testing.T) {
	js := newJS()
	js.kvErr = jetstream.ErrBucketNotFound
	if _, err := New(js).Held(context.Background(), scope, "conv-1"); !errors.Is(err, conversationexport.ErrNotHeld) {
		t.Errorf("err = %v", err)
	}
}

func TestAStoreFailureIsAnErrorNotAnExpiredConversation(t *testing.T) {
	down := errors.New("server went away")
	js := newJS()
	js.kv.err = down
	if _, err := New(js).Held(context.Background(), scope, "conv-1"); err == nil || errors.Is(err, conversationexport.ErrNotHeld) {
		t.Errorf("history failure: err = %v", err)
	}
	js = newJS()
	js.streamErr = down
	if _, err := New(js).Held(context.Background(), scope, "conv-1"); err == nil || errors.Is(err, conversationexport.ErrNotHeld) {
		t.Errorf("stream failure: err = %v", err)
	}
}

func TestAMissingStreamMeansNoReceiptsNotAFailure(t *testing.T) {
	js := newJS()
	js.streamErr = jetstream.ErrStreamNotFound
	held, err := New(js).Held(context.Background(), scope, "conv-1")
	if err != nil || len(held.Receipts) != 0 || len(held.Turns) != 2 {
		t.Errorf("err = %v, receipts = %v, turns = %d", err, held.Receipts, len(held.Turns))
	}
}

func TestMissingObjectStoresMeanNoFiles(t *testing.T) {
	js := newJS()
	js.objects = nil
	held, err := New(js).Held(context.Background(), scope, "conv-1")
	if err != nil || len(held.Files) != 0 {
		t.Errorf("err = %v, files = %+v", err, held.Files)
	}
}

func TestFileOfSplitsOnlyAWorkspaceKeyOfTheCrew(t *testing.T) {
	cases := map[string]bool{
		"ns/c/th/agent/obj": true, "ns/c/th/agent": false, "ns/c/th/a/b/c": false, "ns/c//a/b": false,
		"ns/c/../a/b": false, "other/c/th/a/b": false,
	}
	for key, want := range cases {
		if _, _, ok := fileOf(key, "ns/c/"); ok != want {
			t.Errorf("fileOf(%q) ok = %v, want %v", key, ok, want)
		}
	}
}
