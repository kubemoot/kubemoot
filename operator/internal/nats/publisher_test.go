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
	"fmt"
	"testing"
	"time"
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
