/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package nats

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
	"github.com/kubemoot/kubemoot/operator/pkg/natsconn/natstest"
)

const (
	testStream  = "KUBEMOOT_DISCUSS"
	testSubject = "kubemoot.discuss.ns.crew.t1"
)

// jetStreamPublisher starts an in-process server with JetStream and returns a publisher
// connected to it, plus a raw JetStream context for arranging and checking state.
func jetStreamPublisher(t *testing.T) (*Publisher, nats.JetStreamContext) {
	t.Helper()
	env := natstest.Start(t, natstest.Options{JetStream: true})
	p := NewPublisher(env.URL)
	t.Cleanup(p.Close)
	if !p.Connected() {
		t.Fatal("publisher did not connect to the test server")
	}
	js, err := p.conn.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	return p, js
}

func addDiscussStream(t *testing.T, js nats.JetStreamContext) {
	t.Helper()
	if _, err := js.AddStream(&nats.StreamConfig{Name: testStream, Subjects: []string{"kubemoot.discuss.>"}}); err != nil {
		t.Fatal(err)
	}
}

func TestPublishReachesASubscriberAndOnChangeSkipsAnUnchangedPayload(t *testing.T) {
	p, _ := jetStreamPublisher(t)
	got := make(chan string, 8)
	sub, err := p.Subscribe("test.subject", func(m *nats.Msg) { got <- string(m.Data) })
	if err != nil || sub == nil {
		t.Fatalf("Subscribe = %v, %v", sub, err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	if err := p.Publish("test.subject", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{"one", "one", "two"} {
		if err := p.PublishOnChange("test.subject", payload); err != nil {
			t.Fatal(err)
		}
	}
	for _, w := range []string{`{"k":"v"}`, `"one"`, `"two"`} {
		requireNext(t, got, w)
	}
	select {
	case extra := <-got:
		t.Fatalf("an unchanged payload was sent again: %s", extra)
	case <-time.After(200 * time.Millisecond):
	}

	p.ForgetPublished("test.subject")
	if err := p.PublishOnChange("test.subject", "two"); err != nil {
		t.Fatal(err)
	}
	requireNext(t, got, `"two"`)
}

// requireNext fails unless the next message on got is want.
func requireNext(t *testing.T, got <-chan string, want string) {
	t.Helper()
	select {
	case m := <-got:
		if m != want {
			t.Fatalf("received %s, want %s", m, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no message %s", want)
	}
}

func TestPublishRejectsAPayloadThatCannotBeEncoded(t *testing.T) {
	p, _ := jetStreamPublisher(t)
	if err := p.Publish("s", make(chan int)); err == nil {
		t.Error("Publish accepted an unencodable payload")
	}
	if err := p.PublishOnChange("s", make(chan int)); err == nil {
		t.Error("PublishOnChange accepted an unencodable payload")
	}
}

func TestEnsureConsumerMigratesAPullConsumerToPushAndDeleteIsIdempotent(t *testing.T) {
	p, js := jetStreamPublisher(t)
	addDiscussStream(t, js)
	if _, err := js.AddConsumer(testStream, &nats.ConsumerConfig{
		Durable: "agent-1", AckPolicy: nats.AckExplicitPolicy, FilterSubject: testSubject,
	}); err != nil {
		t.Fatal(err)
	}

	if err := p.EnsureConsumer(testStream, "agent-1", []string{testSubject}); err != nil {
		t.Fatal(err)
	}
	info, err := js.ConsumerInfo(testStream, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if info.Config.DeliverSubject == "" {
		t.Error("the pull consumer was not replaced by a push consumer")
	}
	if err := p.EnsureConsumer(testStream, "agent-1", []string{testSubject}); err != nil {
		t.Errorf("ensuring an existing push consumer again = %v", err)
	}
	if err := p.EnsureConsumer("NO_SUCH_STREAM", "agent-1", []string{testSubject}); err == nil {
		t.Error("a consumer on a missing stream was reported as ensured")
	}

	if err := p.DeleteConsumer(testStream, "agent-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := js.ConsumerInfo(testStream, "agent-1"); !errors.Is(err, nats.ErrConsumerNotFound) {
		t.Errorf("consumer still there after DeleteConsumer: %v", err)
	}
	if err := p.DeleteConsumer(testStream, "agent-1"); err != nil {
		t.Errorf("deleting an absent consumer = %v, want nil", err)
	}
}

const kvBucket = "kv"

func putKeys(t *testing.T, p *Publisher, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if err := p.PutKVValue(kvBucket, k, []byte("v-"+k)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestKVReadsOfAMissingBucketAreEmptyNotErrors(t *testing.T) {
	p, _ := jetStreamPublisher(t)
	if v, err := p.GetKVValue(kvBucket, "a"); v != nil || err != nil {
		t.Errorf("GetKVValue = (%q, %v), want (nil, nil)", v, err)
	}
	if keys, err := p.ListKVKeys(kvBucket); keys != nil || err != nil {
		t.Errorf("ListKVKeys = (%v, %v), want (nil, nil)", keys, err)
	}
	if n, err := p.PurgeKVPrefix(kvBucket, "x"); n != 0 || err != nil {
		t.Errorf("PurgeKVPrefix = %d, %v", n, err)
	}
	wantNoErr(t, "DeleteKVKeys", p.DeleteKVKeys(kvBucket, []string{"k"}))
	wantNoErr(t, "DeleteKVKey", p.DeleteKVKey(kvBucket, "k"))
}

func TestKVRoundTripAndList(t *testing.T) {
	p, _ := jetStreamPublisher(t)
	putKeys(t, p, "crew1.a", "crew1.b", "crew2.a")
	if v, _ := p.GetKVValue(kvBucket, "crew1.a"); string(v) != "v-crew1.a" {
		t.Errorf("GetKVValue = %q", v)
	}
	if v, err := p.GetKVValue(kvBucket, "nope"); v != nil || err != nil {
		t.Errorf("a missing key reads as (%q, %v), want (nil, nil)", v, err)
	}
	keys, err := p.ListKVKeys(kvBucket)
	slices.Sort(keys)
	if err != nil || !slices.Equal(keys, []string{"crew1.a", "crew1.b", "crew2.a"}) {
		t.Errorf("ListKVKeys = %v, %v", keys, err)
	}
}

func TestKVPurgeByPrefixKeepsOtherKeys(t *testing.T) {
	p, _ := jetStreamPublisher(t)
	putKeys(t, p, "crew1.a", "crew1.b", "crew2.a")
	if n, err := p.PurgeKVPrefix(kvBucket, "crew1."); n != 2 || err != nil {
		t.Errorf("PurgeKVPrefix = %d, %v, want 2 deleted", n, err)
	}
	if v, _ := p.GetKVValue(kvBucket, "crew2.a"); v == nil {
		t.Error("a key outside the prefix was purged")
	}
}

func TestKVDeleteByKey(t *testing.T) {
	p, _ := jetStreamPublisher(t)
	putKeys(t, p, "a", "b", "c")
	if err := p.DeleteKVKeys(kvBucket, []string{"a", "never-there"}); err != nil {
		t.Errorf("DeleteKVKeys = %v, want nil for an absent key", err)
	}
	wantNoErr(t, "DeleteKVKeys with no keys", p.DeleteKVKeys(kvBucket, nil))
	if err := p.DeleteKVKey(kvBucket, "b"); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a", "b"} {
		if v, _ := p.GetKVValue(kvBucket, k); v != nil {
			t.Errorf("key %s still readable after delete: %q", k, v)
		}
	}
	if v, _ := p.GetKVValue(kvBucket, "c"); v == nil {
		t.Error("a key that was not deleted is gone")
	}
}

func TestBucketsReadyNamesTheMissingBucket(t *testing.T) {
	p, _ := jetStreamPublisher(t)
	if err := p.PutKVValue("here", "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := p.BucketsReady("here"); err != nil {
		t.Errorf("BucketsReady with every bucket present = %v", err)
	}
	err := p.BucketsReady("here", "missing")
	if !errors.Is(err, bus.ErrNotReady) {
		t.Fatalf("BucketsReady with a bucket missing = %v, want bus.ErrNotReady", err)
	}
}

func TestPurgeSubjectRemovesOnlyTheMatchingMessages(t *testing.T) {
	p, js := jetStreamPublisher(t)
	if err := p.PurgeSubject("NO_SUCH_STREAM", "x.>"); err != nil {
		t.Errorf("purging a missing stream = %v, want nil", err)
	}
	addDiscussStream(t, js)
	for _, s := range []string{testSubject, testSubject, "kubemoot.discuss.ns.crew.t2"} {
		if _, err := js.Publish(s, []byte("m")); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.PurgeSubject(testStream, testSubject); err != nil {
		t.Fatal(err)
	}
	if p.SubjectHasMessages(testStream, testSubject) {
		t.Error("the purged subject still has messages")
	}
	if !p.SubjectHasMessages(testStream, "kubemoot.discuss.ns.crew.t2") {
		t.Error("a subject outside the filter lost its messages")
	}
	if !p.SubjectHasMessages("NO_SUCH_STREAM", "x") {
		t.Error("an unanswerable check must read as true so nothing is dropped")
	}
}

func TestSubscribeDurableDeliversAndResumesAfterAck(t *testing.T) {
	p, js := jetStreamPublisher(t)
	addDiscussStream(t, js)
	got := make(chan string, 4)
	sub, err := p.SubscribeDurable(testStream, "kubemoot.discuss.>", "dur-1", func(m *nats.Msg) {
		got <- string(m.Data)
		_ = m.Ack()
	})
	if err != nil || sub == nil {
		t.Fatalf("SubscribeDurable = %v, %v", sub, err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	if _, err := js.Publish(testSubject, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m != "hello" {
			t.Errorf("delivered %q", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery on the durable consumer")
	}
	if _, err := p.SubscribeDurable("NO_SUCH_STREAM", "x.>", "d", func(*nats.Msg) {}); err == nil {
		t.Error("a durable subscription on a missing stream succeeded")
	}
}

func TestObjectStoreOperationsOnAMissingBucketReportIt(t *testing.T) {
	p, _ := jetStreamPublisher(t)
	if _, err := p.GetObject("no_bucket", "k"); err == nil {
		t.Error("GetObject on a missing bucket returned no error")
	}
	if _, err := p.ListObjects("no_bucket", ""); err == nil {
		t.Error("ListObjects on a missing bucket returned no error")
	}
	if err := p.DeleteObject("no_bucket", "k"); err == nil {
		t.Error("DeleteObject on a missing bucket returned no error")
	}
}

func TestObjectStoreRoundTrip(t *testing.T) {
	p, _ := jetStreamPublisher(t)
	if store, err := p.EnsureObjectStore("arts", time.Hour); err != nil || store == nil {
		t.Fatalf("EnsureObjectStore = %v, %v", store, err)
	}
	if keys, err := p.ListObjects("arts", "x"); keys != nil || err != nil {
		t.Fatalf("an empty bucket lists (%v, %v), want (nil, nil)", keys, err)
	}
	putObjects(t, p, map[string]string{"suite1/a": "AAA", "suite1/b": "BBB", "suite2/a": "CCC"})
	if b, err := p.GetObject("arts", "suite1/a"); err != nil || string(b) != "AAA" {
		t.Errorf("GetObject = %q, %v", b, err)
	}
	keys, err := p.ListObjects("arts", "suite1/")
	slices.Sort(keys)
	if err != nil || !slices.Equal(keys, []string{"suite1/a", "suite1/b"}) {
		t.Errorf("ListObjects = %v, %v", keys, err)
	}
}

func putObjects(t *testing.T, p *Publisher, objects map[string]string) {
	t.Helper()
	for k, v := range objects {
		info, err := p.PutObject("arts", k, []byte(v), time.Hour)
		if err != nil || info == nil || info.Size != uint64(len(v)) {
			t.Fatalf("PutObject(%s) = %+v, %v", k, info, err)
		}
	}
}

func TestDeleteObjectIsIdempotent(t *testing.T) {
	p, _ := jetStreamPublisher(t)
	if _, err := p.PutObject("arts", "k", []byte("x"), time.Hour); err != nil {
		t.Fatal(err)
	}
	wantNoErr(t, "DeleteObject", p.DeleteObject("arts", "k"))
	wantNoErr(t, "DeleteObject of an absent object, so a purge can repeat", p.DeleteObject("arts", "k"))
	if _, err := p.GetObject("arts", "k"); err == nil {
		t.Error("a deleted object is still readable")
	}
}

func TestConversationOwnerWithoutABucketIsAnErrorNotNoOwner(t *testing.T) {
	p, _ := jetStreamPublisher(t)
	scope := crewscope.Scope{Namespace: "ns", Crew: "crew"}
	if _, found, err := p.ConversationOwner(context.Background(), scope, "c1"); err == nil || found {
		t.Fatalf("found=%v err=%v, want an error so it never reads as 'no owner'", found, err)
	}
}

func TestConversationOwnerReadsTheRecord(t *testing.T) {
	p, _ := jetStreamPublisher(t)
	scope := crewscope.Scope{Namespace: "ns", Crew: "crew"}
	rec, _ := json.Marshal(bus.ConversationOwner{Owner: bus.Principal{Kind: bus.PrincipalKindKubernetes}})
	if err := p.PutKVValue(bus.ConversationsBucket, bus.ConversationOwnerKey("ns", "crew", "c1"), rec); err != nil {
		t.Fatal(err)
	}
	if err := p.PutKVValue(bus.ConversationsBucket, bus.ConversationOwnerKey("ns", "crew", "bad"), []byte("not json")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, found, err := p.ConversationOwner(ctx, scope, "c1")
	if err != nil || !found || owner.Kind != bus.PrincipalKindKubernetes {
		t.Errorf("owner = %+v, found=%v, err=%v", owner, found, err)
	}
	for _, id := range []string{"absent", "bad"} {
		if _, found, err := p.ConversationOwner(ctx, scope, id); err != nil || found {
			t.Errorf("%s: found=%v err=%v, want not found and no error", id, found, err)
		}
	}
}

func TestOpenConversationExportAndCrewMemoryConnect(t *testing.T) {
	p, _ := jetStreamPublisher(t)
	if b, err := p.OpenConversationExport(); err != nil || b == nil || b.Source == nil || b.Memory == nil {
		t.Errorf("OpenConversationExport = %+v, %v", b, err)
	}
}
