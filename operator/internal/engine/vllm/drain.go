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

package vllm

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	// DefaultDrainPoll is how often a server being drained is asked for its request
	// count. It paces the reads; it never ends the wait.
	DefaultDrainPoll = time.Second
	// DefaultDrainLimit is the safety net on a drain: a server that still has requests
	// after this long stays awake and the switch fails. The normal path ends when the
	// count reaches zero. It stays below the lifecycle server's own safety limit, so the
	// switch reports its reason before the asker gives up.
	DefaultDrainLimit = 10 * time.Minute
)

// errNoRequestCount is a /metrics page that cannot say how many requests a server holds.
var errNoRequestCount = errors.New("its /metrics does not report vllm:num_requests_running")

// WithDrain sets how often a server being drained is read and the safety net that
// ends the wait; zero keeps the default.
func (a *Adapter) WithDrain(poll, limit time.Duration) *Adapter {
	a.drainPoll, a.drainLimit = poll, limit
	return a
}

func (a *Adapter) pollEvery() time.Duration {
	if a.drainPoll > 0 {
		return a.drainPoll
	}
	return DefaultDrainPoll
}

func (a *Adapter) limit() time.Duration {
	if a.drainLimit > 0 {
		return a.drainLimit
	}
	return DefaultDrainLimit
}

// inFlight is the requests the server at endpoint holds: running plus waiting in its
// own queue, both of which a sleep would cut off. A page without the running gauge
// cannot show the server idle and is an error; a missing waiting gauge counts as none.
func (a *Adapter) inFlight(ctx context.Context, endpoint string) (int, error) {
	body, err := a.get(ctx, endpoint, pathMetrics)
	if err != nil {
		return 0, err
	}
	families, err := parseMetrics(body)
	if err != nil {
		return 0, err
	}
	running := wholeCount(sumOf(families, metricRunning))
	if running == nil {
		return 0, errNoRequestCount
	}
	n := *running
	if waiting := wholeCount(sumOf(families, metricWaiting)); waiting != nil {
		n += *waiting
	}
	return n, nil
}

// drain returns when the server at endpoint holds no request. It fails, leaving the
// server awake, when the count cannot be read or the safety net ends the wait.
// New calls are not stopped here: the lifecycle server queues switches behind one
// another, and a call that arrives meanwhile is the caller's to place.
func (a *Adapter) drain(ctx context.Context, endpoint, model string) error {
	waitCtx, cancel := context.WithTimeout(ctx, a.limit())
	defer cancel()
	last := 0
	for {
		n, err := a.inFlight(waitCtx, endpoint)
		if err == nil && n == 0 {
			return nil
		}
		if err != nil && waitCtx.Err() == nil {
			return fmt.Errorf("not putting %q to sleep: cannot tell whether it is idle: %w; it stays awake", model, err)
		}
		if err == nil {
			last = n
		}
		select {
		case <-waitCtx.Done():
			return drainEnded(ctx, model, last, a.limit())
		case <-time.After(a.pollEvery()):
		}
	}
}

// drainEnded is the reason a drain stopped without reaching zero: the asker gave up, or
// the safety net ended it.
func drainEnded(ctx context.Context, model string, last int, limit time.Duration) error {
	if ctx.Err() != nil {
		return fmt.Errorf("not putting %q to sleep: the request ended with %d still in flight; it stays awake: %w", model, last, ctx.Err())
	}
	return fmt.Errorf("not putting %q to sleep: %d requests were still in flight after the %s safety limit; it stays awake", model, last, limit)
}

// sleepWhenIdle waits for the server to hold no request, then puts it to sleep.
func (a *Adapter) sleepWhenIdle(ctx context.Context, endpoint, model string, level int) error {
	if err := a.drain(ctx, endpoint, model); err != nil {
		return err
	}
	if err := a.sleep(ctx, endpoint, level); err != nil {
		return fmt.Errorf("putting %q to sleep: %w", model, err)
	}
	return nil
}
