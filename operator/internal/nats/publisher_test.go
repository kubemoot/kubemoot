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
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

// errFmtObjectStore is the shared format string used by the object-store
// handlers. Pin its shape so the GetObject/ListObjects/DeleteObject error
// messages stay byte-identical after the S1192 constant extraction.
func TestErrFmtObjectStore(t *testing.T) {
	got := fmt.Errorf(errFmtObjectStore, "my-bucket", fmt.Errorf("boom")).Error()
	want := "object store my-bucket: boom"
	if got != want {
		t.Fatalf("errFmtObjectStore = %q, want %q", got, want)
	}
}

// wantNoErr fails when an unconfigured no-op operation returned an error.
func wantNoErr(t *testing.T, op string, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("%s = %v, want nil", op, err)
	}
}

// wantNilNil fails when a (value, error) no-op did not return both nil. The
// value is taken as `any`; a typed-nil slice/pointer compares != nil under an
// interface, so callers pass a plain bool "value is nil" they computed.
func wantNilNil(t *testing.T, op string, valIsNil bool, err error) {
	t.Helper()
	if !valIsNil || err != nil {
		t.Errorf("%s did not return (nil, nil): valIsNil=%v err=%v", op, valIsNil, err)
	}
}

// An unconfigured publisher (empty URL, no NATS_URL) must be a graceful no-op:
// Configured/Connected report false and every operation returns the documented
// zero values without contacting a server.
func TestUnconfiguredPublisherIsNoOp(t *testing.T) {
	t.Setenv("NATS_URL", "")
	p := NewPublisher("")

	assertUnconfiguredState(t, p)
	assertNoOpMutations(t, p)
	assertNoOpKVReads(t, p)
	assertNoOpObjectStore(t, p)
	assertNoOpSubscriptions(t, p)

	// Close on an unconfigured publisher must not panic.
	p.Close()
}

func assertUnconfiguredState(t *testing.T, p *Publisher) {
	t.Helper()
	if p.Configured() {
		t.Error("Configured() = true, want false for empty URL")
	}
	if p.Connected() {
		t.Error("Connected() = true, want false for empty URL")
	}
	// SubjectHasMessages is optimistic true when NATS is unconfigured.
	if !p.SubjectHasMessages("STREAM", "subj") {
		t.Error("SubjectHasMessages() = false, want true for unconfigured publisher")
	}
}

func assertNoOpMutations(t *testing.T, p *Publisher) {
	t.Helper()
	wantNoErr(t, "Publish()", p.Publish("subj", map[string]string{"k": "v"}))
	wantNoErr(t, "EnsureConsumer()", p.EnsureConsumer("STREAM", "cons", []string{"a.b"}))
	wantNoErr(t, "DeleteConsumer()", p.DeleteConsumer("STREAM", "cons"))
	wantNoErr(t, "PutKVValue()", p.PutKVValue("b", "k", []byte("v")))
	wantNoErr(t, "DeleteKVKey()", p.DeleteKVKey("b", "k"))
	wantNoErr(t, "DeleteObject()", p.DeleteObject("b", "k"))
}

func assertNoOpKVReads(t *testing.T, p *Publisher) {
	t.Helper()
	v, err := p.GetKVValue("b", "k")
	wantNilNil(t, "GetKVValue()", v == nil, err)
	keys, err := p.ListKVKeys("b")
	wantNilNil(t, "ListKVKeys()", keys == nil, err)
	if n, err := p.PurgeKVPrefix("b", "pre"); n != 0 || err != nil {
		t.Errorf("PurgeKVPrefix() = (%d, %v), want (0, nil)", n, err)
	}
	wantNoErr(t, "PurgeSubject()", p.PurgeSubject("S", "kubemoot.discuss.ns.crew.>"))
	wantNoErr(t, "DeleteKVKeys()", p.DeleteKVKeys("b", []string{"k"}))
}

func assertNoOpObjectStore(t *testing.T, p *Publisher) {
	t.Helper()
	store, err := p.EnsureObjectStore("b", time.Hour)
	wantNilNil(t, "EnsureObjectStore()", store == nil, err)
	info, err := p.PutObject("b", "k", []byte("d"), time.Hour)
	wantNilNil(t, "PutObject()", info == nil, err)
	data, err := p.GetObject("b", "k")
	wantNilNil(t, "GetObject()", data == nil, err)
	okeys, err := p.ListObjects("b", "pre")
	wantNilNil(t, "ListObjects()", okeys == nil, err)
}

func assertNoOpSubscriptions(t *testing.T, p *Publisher) {
	t.Helper()
	sub, err := p.Subscribe("subj", nil)
	wantNilNil(t, "Subscribe()", sub == nil, err)
	sub, err = p.SubscribeDurable("STREAM", "subj", "dur", nil)
	wantNilNil(t, "SubscribeDurable()", sub == nil, err)
}

// NewPublisher falls back to the NATS_URL env var when the url argument is empty.
func TestNewPublisherEnvFallback(t *testing.T) {
	t.Setenv("NATS_URL", "nats://example:4222")
	if p := NewPublisher(""); !p.Configured() {
		t.Error("Configured() = false, want true when NATS_URL is set")
	}
	// Explicit url argument takes precedence and is also Configured.
	if p := NewPublisher("nats://override:4222"); !p.Configured() {
		t.Error("Configured() = false, want true for explicit url")
	}
}

// BucketsReady has nothing to wait for without NATS configured, and reports an
// unreachable server as not ready instead of as empty buckets.
func TestBucketsReady(t *testing.T) {
	wantNoErr(t, "BucketsReady() unconfigured", NewPublisher("").BucketsReady("b"))
	err := NewPublisher(unreachableNATS).BucketsReady("b")
	if !errors.Is(err, bus.ErrNotReady) {
		t.Errorf("BucketsReady() with NATS unreachable = %v, want bus.ErrNotReady", err)
	}
}

func TestCrewMemoryKeysReportsAnUnreachableBusInsteadOfNoKeys(t *testing.T) {
	keys, err := NewPublisher("").CrewMemoryKeys()
	if err != nil || keys != nil {
		t.Errorf("CrewMemoryKeys() unconfigured = %v, %v; want no keys and no error", keys, err)
	}
	_, err = NewPublisher(unreachableNATS).CrewMemoryKeys()
	if !errors.Is(err, bus.ErrNotReady) {
		t.Errorf("CrewMemoryKeys() with NATS unreachable = %v, want bus.ErrNotReady", err)
	}
}

func TestCrewMemoryKeysListsTheBucketAndReportsAMissingOne(t *testing.T) {
	url := startLocalJetStream(t)
	p := NewPublisher(url)
	t.Cleanup(p.Close)
	if keys, err := p.CrewMemoryKeys(); !errors.Is(err, bus.ErrNotReady) {
		t.Fatalf("CrewMemoryKeys() with no bucket = %v, %v; want bus.ErrNotReady", keys, err)
	}

	kv := createCrewMemoryBucket(t, url)
	keys, err := p.CrewMemoryKeys()
	if err != nil || len(keys) != 0 {
		t.Fatalf("CrewMemoryKeys() of an empty bucket = %v, %v; want no keys and no error", keys, err)
	}
	if _, err := kv.Put("ns.crew.topic.key", []byte("v")); err != nil {
		t.Fatal(err)
	}
	keys, err = p.CrewMemoryKeys()
	if err != nil || !reflect.DeepEqual(keys, []string{"ns.crew.topic.key"}) {
		t.Fatalf("CrewMemoryKeys() = %v, %v; want the one key", keys, err)
	}
}

func TestCrewMemoryKeyOperationsReportAnUnreachableBus(t *testing.T) {
	if v, found, err := NewPublisher("").CrewMemoryGet("k"); v != nil || found || err != nil {
		t.Errorf("CrewMemoryGet() unconfigured = %v, %v, %v; want not found and no error", v, found, err)
	}
	down := NewPublisher(unreachableNATS)
	if _, _, err := down.CrewMemoryGet("k"); !errors.Is(err, bus.ErrNotReady) {
		t.Errorf("CrewMemoryGet() with NATS unreachable = %v, want bus.ErrNotReady", err)
	}
	if err := down.CrewMemoryPut("k", []byte("v")); !errors.Is(err, bus.ErrNotReady) {
		t.Errorf("CrewMemoryPut() with NATS unreachable = %v, want bus.ErrNotReady", err)
	}
	if err := down.CrewMemoryDelete("k"); !errors.Is(err, bus.ErrNotReady) {
		t.Errorf("CrewMemoryDelete() with NATS unreachable = %v, want bus.ErrNotReady", err)
	}

}

func TestCrewMemoryKeyOperationsReadAndWriteTheBucket(t *testing.T) {
	url := startLocalJetStream(t)
	p := NewPublisher(url)
	t.Cleanup(p.Close)
	if _, _, err := p.CrewMemoryGet("k"); !errors.Is(err, bus.ErrNotReady) {
		t.Fatalf("CrewMemoryGet() with no bucket = %v, want bus.ErrNotReady", err)
	}
	createCrewMemoryBucket(t, url)
	if _, found, err := p.CrewMemoryGet("missing"); found || err != nil {
		t.Fatalf("CrewMemoryGet() of a missing key = %v, %v; want not found and no error", found, err)
	}
	if err := p.CrewMemoryPut("k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if v, found, err := p.CrewMemoryGet("k"); err != nil || !found || string(v) != "v" {
		t.Fatalf("CrewMemoryGet() = %q, %v, %v; want the value", v, found, err)
	}
	deleteTwiceAndExpectGone(t, p, "k")
}

// deleteTwiceAndExpectGone deletes key twice (a key that is gone is not a failure) and
// checks it no longer reads.
func deleteTwiceAndExpectGone(t *testing.T, p *Publisher, key string) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if err := p.CrewMemoryDelete(key); err != nil {
			t.Fatalf("CrewMemoryDelete() #%d = %v, want no error", i+1, err)
		}
	}
	if _, found, _ := p.CrewMemoryGet(key); found {
		t.Error("the key must be gone after the delete")
	}
}

// createCrewMemoryBucket creates the crew memory bucket on the server at url.
func createCrewMemoryBucket(t *testing.T, url string) nats.KeyValue {
	t.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	kv, err := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: crewmemory.Bucket})
	if err != nil {
		t.Fatal(err)
	}
	return kv
}

func TestOpenCrewMemoryReportsAnUnconfiguredServerInsteadOfAnEmptyStore(t *testing.T) {
	if _, err := (&Publisher{}).OpenCrewMemory(); err == nil {
		t.Fatal("OpenCrewMemory() = nil error with no NATS URL, want an error so an export never reads it as no facts")
	}
}

func TestOpenConversationExportReportsAnUnconfiguredServerInsteadOfAnEmptyStore(t *testing.T) {
	if _, err := (&Publisher{}).OpenConversationExport(); err == nil {
		t.Fatal("OpenConversationExport() = nil error with no NATS URL, want an error so an export never reads it as no conversation")
	}
}

func TestConversationOwnerReportsAnUnconfiguredStoreAsAnErrorNotAsNoOwner(t *testing.T) {
	_, found, err := (&Publisher{}).ConversationOwner(context.Background(), crewscope.Scope{Namespace: "ns", Crew: "c"}, "id")
	if err == nil || found {
		t.Fatalf("found=%v err=%v: an unreachable store must not read as 'no owner'", found, err)
	}
}
