package bus_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/nats-io/nats.go"

	kbus "github.com/kubemoot/kubemoot/operator/pkg/bus"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/a2a"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/bus"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/bustest"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

// When the terminal signal cannot be published the message is asked for again and the turn runs again;
// the second delivery must not be taken for a duplicate of the first.
func TestATerminalSignalThatCouldNotBePublishedIsRetried(t *testing.T) {
	p := newPod(t, options{consumer: true, ackWait: 30 * time.Second}, answer("eventually"))
	var refused atomic.Bool
	p.pub.Send = func(m *nats.Msg) error {
		if !refused.Load() && contains(string(m.Data), `"messageType":"agree"`) {
			refused.Store(true)
			return errors.New("nats: connection closed")
		}
		return p.nc.PublishMsg(m)
	}
	p.advisory(t, "s1", agent)
	m := p.nextOf(t, agree)
	if m.Content != "eventually" || p.h.calls.Load() != 2 {
		t.Fatalf("want the answer from a second run, got %q after %d runs", m.Content, p.h.calls.Load())
	}
}

func TestATurnWaitingForASlotSaysSoAndCanBeStoppedWhileItWaits(t *testing.T) {
	gate := make(chan struct{})
	p := newPod(t, options{}, func(ctx context.Context, _ *sdk.Message, _ *turn.Meter) (a2a.Result, error) {
		select {
		case <-gate:
		case <-ctx.Done():
		}
		return answer("ok")(nil, nil, nil)
	})
	p.advisory(t, "q1", agent)
	p.nextOf(t, evaluating)
	p.advisory(t, "q2", agent)
	for {
		m := p.next(t)
		if m.ThreadID == "q2" && m.MessageType == waiting && m.Metadata["reason"] == "queued" {
			break
		}
	}
	p.send(t, "stop_requested", "human", "q2", nil)
	time.Sleep(400 * time.Millisecond) // the stop is delivered separately from the slot coming free
	close(gate)
	p.nextOf(t, agree) // q1 finishes
	p.quiet(t, 500*time.Millisecond)
	if n := p.h.calls.Load(); n != 1 {
		t.Fatalf("the stopped, queued turn must never reach the harness; %d turns ran", n)
	}
}

func TestShutdownCancelsTurnsInFlightAndWaitsForThem(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	p := newPodWith(t, ctx, options{}, func(c context.Context, _ *sdk.Message, _ *turn.Meter) (a2a.Result, error) {
		close(started)
		<-c.Done()
		return a2a.Result{}, c.Err()
	})
	p.advisory(t, "z1", agent)
	<-started
	cancel()
	done := make(chan struct{})
	go func() { p.runner.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(waitFor):
		t.Fatal("a turn in flight outlived the shutdown")
	}
	for {
		select {
		case m := <-p.got:
			if m.MessageType != evaluating {
				t.Fatalf("a turn cut by shutdown publishes no terminal signal, got %s", m.MessageType)
			}
		case <-time.After(300 * time.Millisecond):
			return
		}
	}
}

func TestALifecycleWithNoTimingsDefaultsInsteadOfPanicking(t *testing.T) {
	p := newPod(t, options{}, answer("x"))
	life := make(chan kbus.DiscussionMessage, 2)
	rc := bustest.Connect(t, p.url)
	if _, err := rc.Subscribe("kubemoot.lifecycle.>", func(m *nats.Msg) {
		var msg kbus.DiscussionMessage
		_ = json.Unmarshal(m.Data, &msg)
		life <- msg
	}); err != nil {
		t.Fatal(err)
	}
	_ = rc.Flush()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &bus.Lifecycle{Pub: p.pub, Ready: func() bool { return false }, NATS: func() bool { return true },
		Harness: func() bool { return true }}
	go l.Run(ctx)
	select {
	case m := <-life:
		if m.MessageType != kbus.DiscussionMessageMessageTypeWaking {
			t.Errorf("%s", m.MessageType)
		}
	case <-time.After(waitFor):
		t.Fatal("no waking signal")
	}
}
