package bus_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/a2a"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/bus"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

// newerPod is a second sidecar of the same agent on the first pod's server and consumer, as the pod
// a rolling update starts beside the old one. Its harness counts the turns it takes.
func newerPod(p *pod, turns *atomic.Int32) *bus.Runner {
	h := &harness{reg: turn.NewRegistry(), canceled: make(chan sdk.TaskID, 4),
		do: func(ctx context.Context, m *sdk.Message, mt *turn.Meter) (a2a.Result, error) {
			turns.Add(1)
			return answer("newer")(ctx, m, mt)
		}}
	return &bus.Runner{ID: p.runner.ID, Bus: p.runner.Bus, Turn: p.runner.Turn, Conn: p.nc, JS: p.js,
		Harness: h, Registry: h.reg, Pub: p.pub, Planner: p.planner, Tools: p.tools, Metrics: p.metrics,
		Aliases: p.runner.Aliases, ModelAliases: p.runner.ModelAliases, NakDelay: 100 * time.Millisecond}
}

// waitBound waits until the server counts the consumer's subscription, which it learns of after Subscribe returns.
func waitBound(t *testing.T, p *pod) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for !consumerInfo(t, p).PushBound {
		if time.Now().After(deadline) {
			t.Fatal("the consumer never showed a bound subscription")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestASecondSubscriberForTheSameAgentIsRefusedWhileTheFirstIsActive(t *testing.T) {
	p := newPod(t, options{consumer: true}, answer("older"))
	waitBound(t, p)
	var turns atomic.Int32
	if sub, err := newerPod(p, &turns).Subscribe(context.Background()); err == nil {
		_ = sub.Unsubscribe()
		t.Fatal("two subscriptions bound one push consumer")
	}
}

// During the overlap the old pod answers every message and the new pod none; once the old pod
// lets go, the new pod binds and the next message is answered by it alone.
func TestTheNewerPodWaitsForTheOlderOneAndThenTakesOver(t *testing.T) {
	p := newPod(t, options{consumer: true}, answer("older"))
	var turns atomic.Int32
	newer := newerPod(p, &turns)
	waitBound(t, p)
	bound := make(chan *nats.Subscription, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { bound <- newer.SubscribeWhenFree(ctx) }()

	p.advisory(t, "overlap", agent)
	p.nextOf(t, agree)
	select {
	case <-bound:
		t.Fatal("the newer pod bound while the older one held the consumer")
	case <-time.After(time.Second):
	}
	if got := p.h.calls.Load(); got != 1 || turns.Load() != 0 {
		t.Fatalf("during the overlap the older pod took %d turns and the newer %d, want 1 and 0", got, turns.Load())
	}

	_ = p.sub.Unsubscribe()
	var sub *nats.Subscription
	select {
	case sub = <-bound:
	case <-time.After(waitFor):
		t.Fatal("the newer pod never bound after the older one left")
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	p.advisory(t, "after", agent)
	p.nextOf(t, agree)
	if got := p.h.calls.Load(); got != 1 || turns.Load() != 1 {
		t.Fatalf("after the handover the older pod took %d turns and the newer %d, want 1 and 1", got, turns.Load())
	}
}

func TestWaitingForTheConsumerEndsWithTheContext(t *testing.T) {
	p := newPod(t, options{consumer: true}, answer("older"))
	var turns atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *nats.Subscription, 1)
	go func() { done <- newerPod(p, &turns).SubscribeWhenFree(ctx) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case sub := <-done:
		if sub != nil {
			t.Fatal("a subscription came back from a canceled wait")
		}
	case <-time.After(waitFor):
		t.Fatal("the wait outlived its context")
	}
}

func TestAConsumerTheOperatorHasNotCreatedYetIsWaitedFor(t *testing.T) {
	p := newPod(t, options{consumer: true}, answer("older"))
	var turns atomic.Int32
	newer := newerPod(p, &turns)
	newer.Bus.Consumer = "not-created-yet"
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan *nats.Subscription, 1)
	go func() { done <- newer.SubscribeWhenFree(ctx) }()
	select {
	case <-done:
		t.Fatal("subscribed to a consumer that does not exist")
	case <-time.After(500 * time.Millisecond):
	}
}
