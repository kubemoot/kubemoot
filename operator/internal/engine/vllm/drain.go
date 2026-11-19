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

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
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

// holds is what keeps the server at endpoint from sleeping: the requests it reports (running
// plus queued) and the calls placed on its models that have not ended. A placed call may
// not have reached the server yet, so its request count cannot show it.
func (a *Adapter) holds(ctx context.Context, endpoint string, models []string) (int, error) {
	n, err := a.inFlight(ctx, endpoint)
	if err != nil {
		return 0, err
	}
	placed, err := engine.PlacedCalls(ctx, models)
	if err != nil {
		return 0, fmt.Errorf("reading the calls placed on it: %w", err)
	}
	return max(n, placed), nil
}

// drain returns when nothing holds the server at endpoint. It fails, leaving the server
// awake, when what holds it cannot be read or the safety net ends the wait. It does not
// stop new calls: the caller publishes the draining state first.
func (a *Adapter) drain(ctx context.Context, endpoint string, models []string) error {
	waitCtx, cancel := context.WithTimeout(ctx, a.limit())
	defer cancel()
	last := 0
	for {
		n, err := a.holds(waitCtx, endpoint, models)
		if err == nil && n == 0 {
			return nil
		}
		if err != nil && waitCtx.Err() == nil {
			return fmt.Errorf("not putting %q to sleep: cannot tell whether it is idle: %w; it stays awake", models[0], err)
		}
		if err == nil {
			last = n
		}
		select {
		case <-waitCtx.Done():
			return drainEnded(ctx, models[0], last, a.limit())
		case <-time.After(a.pollEvery()):
		}
	}
}

// drainEnded is the reason a drain stopped without reaching zero: the asker gave up, or
// the safety net ended it.
func drainEnded(ctx context.Context, model string, last int, limit time.Duration) error {
	if ctx.Err() != nil {
		return fmt.Errorf("not putting %q to sleep: the request ended with %d still in flight or placed; it stays awake: %w", model, last, ctx.Err())
	}
	return fmt.Errorf("not putting %q to sleep: %d requests were still in flight or placed after the %s safety limit; it stays awake", model, last, limit)
}

// namesAt is every name the server at endpoint answers to.
func namesAt(spec aiv1alpha1.ModelServerSpec, endpoint string) []string {
	for _, g := range byEndpoint(serversOf(spec)) {
		if g.endpoint == endpoint {
			return g.models
		}
	}
	return nil
}

// sleepWhenIdle publishes the server's models as draining, waits until nothing holds the
// server, then puts it to sleep. The draining state is published before anything is read.
// The order of the two sides is total. A call placed on the model claims a placement ticket
// and then reads the server's state afresh; if it reads draining it releases the ticket and
// is placed again (it waits for this switch on the operator's turn, or goes to another
// awake provider). A ticket claimed before this switch read the tickets is counted and holds
// the switch; a ticket claimed after sees the draining state, because the state was
// published before the tickets were read. What is guaranteed: the server sleeps only when,
// at the moment of the last read, the engine reports no running or queued request and no
// call placed on its models (other than one waiting for this switch) holds a ticket.
// What remains: only calls that hold no ticket, which the wait cannot see: a ticket with no
// model name, and a caller outside the placement path that sends to vLLM directly. A call
// the placement path placed holds its ticket from the claim until it ends, so it is
// covered whether or not the engine has received it. vLLM has no call that refuses new
// requests, so nothing else stops a ticketless call. The caller's re-read of the state
// after its claim fails open: a state it cannot read leaves its ticket in place, and if
// this switch had already listed the tickets the call can be cut off. The draining state is cleared when
// the switch ends, whether the server slept or stayed awake.
func (a *Adapter) sleepWhenIdle(ctx context.Context, models []string, endpoint string, level int) error {
	if err := engine.NotifyDraining(ctx, models, true); err != nil {
		return fmt.Errorf("not putting %q to sleep: could not publish it as draining: %w; it stays awake", models[0], err)
	}
	defer func() { _ = engine.NotifyDraining(context.WithoutCancel(ctx), models, false) }()
	if err := a.drain(ctx, endpoint, models); err != nil {
		return err
	}
	if err := a.sleep(ctx, endpoint, level); err != nil {
		return fmt.Errorf("putting %q to sleep: %w", models[0], err)
	}
	return nil
}
