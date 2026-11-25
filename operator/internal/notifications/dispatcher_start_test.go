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

package notifications

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
)

// flakyDurable refuses the first fails subscribes (NATS down, or the stream not yet
// created by the bootstrap), then accepts.
type flakyDurable struct {
	mu       sync.Mutex
	fails    int
	calls    int
	accepted chan struct{}
	once     sync.Once
}

func (f *flakyDurable) SubscribeDurable(stream, subject, durable string, _ nats.MsgHandler) (*nats.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.fails {
		return nil, errors.New("nats: stream not found")
	}
	if stream != DiscussStreamName || subject != DiscussionSubjectWildcard || durable != DispatcherDurableName {
		return nil, errors.New("unexpected stream, subject or durable")
	}
	f.once.Do(func() { close(f.accepted) })
	return &nats.Subscription{}, nil
}

func TestTheDispatcherSubscribesOnceNATSComesBack(t *testing.T) {
	sub := &flakyDurable{fails: 2, accepted: make(chan struct{})}
	d := &Dispatcher{Publisher: sub, Backoff: kubemootnats.Backoff{First: time.Millisecond, Max: 2 * time.Millisecond}}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { _ = d.Start(ctx); close(stopped) }()

	select {
	case <-sub.accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("the dispatcher never subscribed after NATS came back")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after its context ended")
	}
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.calls != 3 {
		t.Errorf("%d subscribe attempts, want 3 (two refused, one accepted)", sub.calls)
	}
}
