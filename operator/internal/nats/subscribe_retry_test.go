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
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

var quick = Backoff{First: time.Millisecond, Max: 4 * time.Millisecond}

func TestSubscribeUntilReadyRetriesUntilNATSIsBack(t *testing.T) {
	want := &nats.Subscription{}
	calls := 0
	var waits []time.Duration
	sub, err := SubscribeUntilReady(context.Background(), func() (*nats.Subscription, error) {
		calls++
		if calls < 5 {
			return nil, errors.New("nats: no servers available for connection")
		}
		return want, nil
	}, quick, func(_ error, retryIn time.Duration) { waits = append(waits, retryIn) })
	if err != nil || sub != want {
		t.Fatalf("got %v, %v", sub, err)
	}
	if calls != 5 {
		t.Errorf("%d attempts, want 5", calls)
	}
	wantWaits := []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond}
	if len(waits) != len(wantWaits) {
		t.Fatalf("waits %v, want %v", waits, wantWaits)
	}
	for i := range waits {
		if waits[i] != wantWaits[i] {
			t.Errorf("wait %d is %v, want %v (doubling, capped at Max)", i, waits[i], wantWaits[i])
		}
	}
}

func TestSubscribeUntilReadyStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	done := make(chan struct{})
	var sub *nats.Subscription
	var err error
	go func() {
		sub, err = SubscribeUntilReady(ctx, func() (*nats.Subscription, error) {
			attempts++
			if attempts == 2 {
				cancel()
			}
			return nil, errors.New("down")
		}, quick, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SubscribeUntilReady did not return after the context ended")
	}
	if sub != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, %v; want nil and context.Canceled", sub, err)
	}
}

func TestSubscribeUntilReadyReturnsAtOnceWhenNATSIsNotConfigured(t *testing.T) {
	calls, failedCalls := 0, 0
	sub, err := SubscribeUntilReady(context.Background(), func() (*nats.Subscription, error) {
		calls++
		return nil, nil
	}, quick, func(error, time.Duration) { failedCalls++ })
	if sub != nil || err != nil || calls != 1 || failedCalls != 0 {
		t.Fatalf("got %v, %v after %d calls and %d failures; want nil, nil after one call", sub, err, calls, failedCalls)
	}
}

func TestSubscribeUntilReadySucceedsFirstTimeWithoutWaiting(t *testing.T) {
	want := &nats.Subscription{}
	failedCalls := 0
	sub, err := SubscribeUntilReady(context.Background(), func() (*nats.Subscription, error) { return want, nil },
		Backoff{First: time.Hour, Max: time.Hour}, func(error, time.Duration) { failedCalls++ })
	if err != nil || sub != want || failedCalls != 0 {
		t.Fatalf("got %v, %v, %d failures", sub, err, failedCalls)
	}
}

func TestAZeroBackoffUsesTheDefaultInsteadOfSpinning(t *testing.T) {
	if got := (Backoff{}).orDefault(); got != DefaultBackoff {
		t.Fatalf("zero backoff became %v, want %v", got, DefaultBackoff)
	}
	if got := (Backoff{First: time.Second, Max: time.Millisecond}).orDefault(); got.Max != time.Second {
		t.Fatalf("a Max below First became %v, want it raised to First", got.Max)
	}
}

func TestAPublisherKeepsAConnectionThatIsReconnecting(t *testing.T) {
	// A connection object that is not closed is kept (it reconnects on its own and replays
	// its subscriptions); connect must not open a second one and leak the first.
	p := &Publisher{url: unreachableNATS}
	conn := &nats.Conn{} // not connected and not closed: the state of a reconnecting connection
	p.conn = conn
	if err := p.connect(); err != nil {
		t.Fatalf("connect replaced a live connection: %v", err)
	}
	if p.conn != conn {
		t.Fatal("connect replaced a connection that was not closed")
	}
}

// unreachableNATS is an address nothing listens on, for tests of a NATS that is down.
const unreachableNATS = "nats://127.0.0.1:1"
