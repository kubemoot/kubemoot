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

package controller

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

// flakySubscriber refuses the first fails subscribes, as NATS does while it waits for its
// volume after a power loss, then accepts.
type flakySubscriber struct {
	mu       sync.Mutex
	fails    int
	calls    int
	subjects []string
	accepted chan struct{}
}

func (f *flakySubscriber) Subscribe(subject string, _ nats.MsgHandler) (*nats.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.subjects = append(f.subjects, subject)
	if f.calls <= f.fails {
		return nil, errors.New("nats: no servers available for connection")
	}
	close(f.accepted)
	return &nats.Subscription{}, nil
}

func TestTheLifecycleServerSubscribesOnceNATSComesBack(t *testing.T) {
	sub := &flakySubscriber{fails: 3, accepted: make(chan struct{})}
	server := &ModelLifecycleServer{Subscriber: sub, Backoff: kubemootnats.Backoff{First: time.Millisecond, Max: 2 * time.Millisecond}}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { _ = server.Start(ctx); close(stopped) }()

	select {
	case <-sub.accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("the server never subscribed after NATS came back")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after its context ended")
	}
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.calls != 4 {
		t.Errorf("%d subscribe attempts, want 4 (three refused, one accepted)", sub.calls)
	}
	for _, s := range sub.subjects {
		if s != bus.ModelLifecycleSubjectPrefix+"*" {
			t.Errorf("subscribed to %q, want %q", s, bus.ModelLifecycleSubjectPrefix+"*")
		}
	}
}

func TestTheLifecycleServerStopsRetryingWhenTheOperatorStops(t *testing.T) {
	sub := &flakySubscriber{fails: 1 << 30, accepted: make(chan struct{})}
	server := &ModelLifecycleServer{Subscriber: sub, Backoff: kubemootnats.Backoff{First: time.Millisecond, Max: time.Millisecond}}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { _ = server.Start(ctx); close(stopped) }()
	waitForCalls(t, sub, 3) // it is retrying
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Start kept retrying after its context ended")
	}
}

func TestTheLifecycleServerUsesTheDefaultBackoffWhenUnset(t *testing.T) {
	if got := (&ModelLifecycleServer{}).backoff(); got != kubemootnats.DefaultBackoff {
		t.Fatalf("backoff %v, want the default %v", got, kubemootnats.DefaultBackoff)
	}
}

// waitForCalls waits until the subscriber has been asked at least n times.
func waitForCalls(t *testing.T, sub *flakySubscriber, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sub.mu.Lock()
		calls := sub.calls
		sub.mu.Unlock()
		if calls >= n {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("the server did not retry %d times", n)
}
