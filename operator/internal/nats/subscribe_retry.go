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
	"time"

	"github.com/nats-io/nats.go"
)

// Backoff bounds for SubscribeUntilReady: the first retry waits First, each later one
// twice as long, never more than Max.
type Backoff struct {
	First time.Duration
	Max   time.Duration
}

// DefaultBackoff retries after 1 s, then 2 s, 4 s and so on up to 30 s.
var DefaultBackoff = Backoff{First: time.Second, Max: 30 * time.Second}

// orDefault is b, or DefaultBackoff when b has no positive First (a zero Backoff would
// retry in a tight loop); a Max below First is raised to First.
func (b Backoff) orDefault() Backoff {
	if b.First <= 0 {
		return DefaultBackoff
	}
	if b.Max < b.First {
		b.Max = b.First
	}
	return b
}

// SubscribeUntilReady calls subscribe until it returns a subscription or ctx ends, so a
// runnable that starts while NATS is unreachable (after a power loss, NATS waits for its
// volume) subscribes once NATS is back instead of idling for the life of the process.
// failed is told about every failed attempt and the wait before the next one. A subscribe
// that returns neither a subscription nor an error means NATS is not configured (Publisher
// with no URL): that is returned at once as (nil, nil), since retrying cannot change it.
// It returns nil and ctx's error when ctx ends first.
func SubscribeUntilReady(
	ctx context.Context,
	subscribe func() (*nats.Subscription, error),
	backoff Backoff,
	failed func(err error, retryIn time.Duration),
) (*nats.Subscription, error) {
	backoff = backoff.orDefault()
	wait := backoff.First
	for {
		sub, err := subscribe()
		if err == nil {
			return sub, nil
		}
		if failed != nil {
			failed(err, wait)
		}
		if !sleep(ctx, wait) {
			return nil, ctx.Err()
		}
		wait = min(wait*2, backoff.Max)
	}
}

// sleep waits d, or until ctx ends; it reports whether the full wait passed.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
