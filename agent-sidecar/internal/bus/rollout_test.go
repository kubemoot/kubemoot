package bus_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/nats-io/nats.go"

	kbus "github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/mesh"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/a2a"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/bus"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/bustest"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

// logBuf collects what a runner logs.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) count(sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Count(l.b.String(), sub)
}

// newer is a second sidecar of the same agent on the first pod's server and consumer, as the pod a
// rolling update starts beside the old one. Its harness counts the turns it takes.
type newer struct {
	r     *bus.Runner
	turns atomic.Int32
	log   logBuf
	state chan bool
	done  chan error
}

func newerPod(p *pod, script func(context.Context, *sdk.Message, *turn.Meter) (a2a.Result, error)) *newer {
	n := &newer{state: make(chan bool, 16), done: make(chan error, 1)}
	if script == nil {
		script = answer("newer")
	}
	h := &harness{reg: turn.NewRegistry(), canceled: make(chan sdk.TaskID, 4),
		do: func(ctx context.Context, m *sdk.Message, mt *turn.Meter) (a2a.Result, error) {
			n.turns.Add(1)
			return script(ctx, m, mt)
		}}
	n.r = &bus.Runner{ID: p.runner.ID, Bus: p.runner.Bus, Turn: p.runner.Turn, Conn: p.nc, JS: p.js,
		Harness: h, Registry: h.reg, Pub: p.pub, Planner: p.planner, Tools: p.tools, Metrics: p.metrics,
		Aliases: p.runner.Aliases, ModelAliases: p.runner.ModelAliases, NakDelay: 100 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(&n.log, nil)), WarnEvery: 300 * time.Millisecond}
	return n
}

// hold runs the runner's subscription until ctx ends.
func (n *newer) hold(ctx context.Context) {
	go func() { n.done <- n.r.Hold(ctx, func(on bool) { n.state <- on }) }()
}

// next waits for the runner's next change of subscription state.
func (n *newer) next(t *testing.T) bool {
	t.Helper()
	select {
	case on := <-n.state:
		return on
	case <-time.After(waitFor):
		t.Fatal("the subscription state never changed")
		return false
	}
}

// waitLogged waits until the runner has logged text.
func (n *newer) waitLogged(t *testing.T, text string) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for n.log.count(text) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("never logged %q", text)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitBound waits until the server counts the consumer's subscription, which it learns of after
// Subscribe returns.
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
	if sub, err := newerPod(p, nil).r.Subscribe(context.Background()); err == nil {
		_ = sub.Unsubscribe()
		t.Fatal("two subscriptions bound one push consumer")
	}
}

// During the overlap the old pod answers every message and the new pod none; once the old pod
// lets go, the new pod binds and the next message is answered by it alone.
func TestTheNewerPodWaitsForTheOlderOneAndThenTakesOver(t *testing.T) {
	p := newPod(t, options{consumer: true}, answer("older"))
	waitBound(t, p)
	n := newerPod(p, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	n.hold(ctx)
	n.waitLogged(t, "not subscribed to the crew's discussion yet")

	p.advisory(t, "overlap", agent)
	p.nextOf(t, agree)
	if got := p.h.calls.Load(); got != 1 || n.turns.Load() != 0 {
		t.Fatalf("during the overlap the older pod took %d turns and the newer %d, want 1 and 0", got, n.turns.Load())
	}
	select {
	case <-n.state:
		t.Fatal("the newer pod bound while the older one held the consumer")
	default:
	}

	_ = p.sub.Unsubscribe()
	if !n.next(t) {
		t.Fatal("the newer pod reported itself unsubscribed")
	}
	p.advisory(t, "after", agent)
	p.nextOf(t, agree)
	if got := p.h.calls.Load(); got != 1 || n.turns.Load() != 1 {
		t.Fatalf("after the handover the older pod took %d turns and the newer %d, want 1 and 1", got, n.turns.Load())
	}
}

func TestWhileItWaitsTheWarningIsRepeated(t *testing.T) {
	p := newPod(t, options{consumer: true}, answer("older"))
	waitBound(t, p)
	n := newerPod(p, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	n.hold(ctx)
	deadline := time.Now().Add(waitFor)
	for n.log.count("not subscribed to the crew's discussion yet") < 3 {
		if time.Now().After(deadline) {
			t.Fatal("the wait was logged once and then went quiet")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWaitingForTheConsumerEndsWithTheContext(t *testing.T) {
	p := newPod(t, options{consumer: true}, answer("older"))
	waitBound(t, p)
	n := newerPod(p, nil)
	ctx, cancel := context.WithCancel(context.Background())
	n.hold(ctx)
	n.waitLogged(t, "not subscribed")
	cancel()
	select {
	case err := <-n.done:
		if err != nil {
			t.Fatalf("a canceled wait is not an error: %v", err)
		}
	case <-time.After(waitFor):
		t.Fatal("the wait outlived its context")
	}
}

func TestAConsumerTheOperatorHasNotCreatedYetIsWaitedForAndThenBound(t *testing.T) {
	p := newPod(t, options{}, answer("older"))
	n := newerPod(p, nil)
	n.r.Bus.Stream, n.r.Bus.Consumer = kbus.DiscussStream, id.ConsumerName()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	n.hold(ctx)
	n.waitLogged(t, "not subscribed")
	bustest.Consumer(t, p.js, id, 30*time.Second)
	if !n.next(t) {
		t.Fatal("not subscribed once the consumer exists")
	}
}

// What waiting cannot cure ends the wait with the error, so the misconfiguration is seen.
func TestAMisconfigurationEndsTheWaitWithAnError(t *testing.T) {
	p := newPod(t, options{consumer: true}, answer("older"))
	_ = p.sub.Unsubscribe()
	cases := map[string]func(*newer){
		"no JetStream":     func(n *newer) { n.r.JS = nil },
		"a missing stream": func(n *newer) { n.r.Bus.Stream = "NO_SUCH_STREAM" },
		"a closed connection": func(n *newer) {
			c := bustest.Connect(t, p.url)
			n.r.Conn = c
			js := bustest.JetStream(t, c)
			n.r.JS = js
			c.Close()
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			n := newerPod(p, nil)
			mutate(n)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- n.r.Hold(ctx, func(bool) {}) }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("a misconfiguration was waited for forever")
				}
			case <-time.After(waitFor):
				t.Fatal("a misconfiguration was waited for forever")
			}
		})
	}
}

// A subscription that is lost is bound again: the operator deleting and making the consumer again
// gives it a new delivery subject, and the old subscription hears nothing.
func TestALostSubscriptionIsBoundAgain(t *testing.T) {
	p := newPod(t, options{consumer: true}, answer("older"))
	_ = p.sub.Unsubscribe()
	n := newerPod(p, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	n.hold(ctx)
	if !n.next(t) {
		t.Fatal("not subscribed")
	}
	if err := p.js.DeleteConsumer(kbus.DiscussStream, id.ConsumerName()); err != nil {
		t.Fatal(err)
	}
	if n.next(t) {
		t.Fatal("the loss of the consumer was not noticed")
	}
	bustest.Consumer(t, p.js, id, 30*time.Second)
	if !n.next(t) {
		t.Fatal("the subscription was not bound again")
	}
	waitBound(t, p)
	p.advisory(t, "again", agent)
	p.nextOf(t, agree)
	if n.turns.Load() != 1 {
		t.Fatalf("the rebound subscription took %d turns, want 1", n.turns.Load())
	}
}

// A turn cut by the sidecar shutting down is given back, not dropped: the pod that replaces it
// answers the message. (A turn cut because its thread ended is acknowledged; settle_test covers it.)
func TestATurnCutByShutdownIsAnsweredByTheNextPod(t *testing.T) {
	ctx, shutdown := context.WithCancel(context.Background())
	started := make(chan struct{})
	p := newPodWith(t, ctx, options{consumer: true}, func(c context.Context, _ *sdk.Message, _ *turn.Meter) (a2a.Result, error) {
		close(started)
		<-c.Done()
		return a2a.Result{}, c.Err()
	})
	waitBound(t, p)
	n := newerPod(p, nil)
	hctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	n.hold(hctx)
	n.waitLogged(t, "not subscribed")

	p.advisory(t, "cut", agent)
	<-started
	shutdown()
	p.runner.Wait()
	_ = p.sub.Unsubscribe()
	if !n.next(t) {
		t.Fatal("the newer pod did not take over")
	}
	if m := p.nextOf(t, agree); m.ThreadID != "cut" {
		t.Fatalf("answered thread %q", m.ThreadID)
	}
	if n.turns.Load() != 1 {
		t.Fatalf("the newer pod took %d turns, want 1", n.turns.Load())
	}
}

// The crew is told ready only once the sidecar receives its discussion; the heartbeat's NATS flag
// stays the connection, so the agent does not look degraded for a whole rollout.
func TestReadyIsAnnouncedOnlyOnceSubscribed(t *testing.T) {
	p := newPod(t, options{}, answer("x"))
	life := make(chan kbus.DiscussionMessage, 4)
	rc := bustest.Connect(t, p.url)
	if _, err := rc.Subscribe("kubemoot.lifecycle.>", func(m *nats.Msg) {
		var msg kbus.DiscussionMessage
		_ = json.Unmarshal(m.Data, &msg)
		life <- msg
	}); err != nil {
		t.Fatal(err)
	}
	_ = rc.Flush()
	var subscribed atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &bus.Lifecycle{Pub: p.pub, Ready: func() bool { return true }, NATS: func() bool { return true },
		Harness: func() bool { return true }, Subscribed: subscribed.Load, Poll: 20 * time.Millisecond,
		Beat: 50 * time.Millisecond}
	go l.Run(ctx)
	if m := <-life; m.MessageType != kbus.DiscussionMessageMessageTypeWaking {
		t.Fatalf("first signal %s", m.MessageType)
	}
	if !heartbeatNATS(t, p) {
		t.Error("the heartbeat's NATS flag is the connection, not the subscription")
	}
	select {
	case m := <-life:
		t.Fatalf("%s announced before the discussion is received", m.MessageType)
	case <-time.After(150 * time.Millisecond):
	}
	subscribed.Store(true)
	select {
	case m := <-life:
		if m.MessageType != kbus.DiscussionMessageMessageTypeReady {
			t.Fatalf("signal %s", m.MessageType)
		}
	case <-time.After(waitFor):
		t.Fatal("ready never announced")
	}
}

// heartbeatNATS reads the NATS flag of the agent's latest heartbeat, once one is written.
func heartbeatNATS(t *testing.T, p *pod) bool {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for {
		if kv, err := p.js.KeyValue(mesh.AgentStateBucket); err == nil {
			if e, err := kv.Get(id.HeartbeatKey()); err == nil {
				var hb kbus.AgentHeartbeat
				if err := json.Unmarshal(e.Value(), &hb); err != nil {
					t.Fatal(err)
				}
				return hb.Nats
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no heartbeat written")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A turn cut by shutdown is judged afresh when the server delivers it again to the same runner: it
// is not dropped as a duplicate of a message that was never answered.
func TestAGivenBackTurnIsNotADuplicateWhenItComesBackToTheSameRunner(t *testing.T) {
	ctx, shutdown := context.WithCancel(context.Background())
	started := make(chan struct{})
	var once sync.Once
	p := newPodWith(t, ctx, options{consumer: true}, func(c context.Context, _ *sdk.Message, _ *turn.Meter) (a2a.Result, error) {
		once.Do(func() { close(started) })
		<-c.Done()
		return a2a.Result{}, c.Err()
	})
	p.advisory(t, "bounce", agent)
	<-started
	shutdown()
	p.runner.Wait() // the subscription is still bound, so the server hands the message back to this runner
	deadline := time.Now().Add(waitFor)
	for p.deliveries(t, "turn") < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("the redelivered turn was not judged afresh: %v duplicates", p.deliveries(t, "duplicate"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if d := p.deliveries(t, "duplicate"); d != 0 {
		t.Fatalf("the given-back turn was dropped as a duplicate %v times", d)
	}
}

// With no delivery left a give-back would be dropped by the server, so the thread is told instead.
func TestATurnCutByShutdownOnItsLastDeliveryIsReportedInterrupted(t *testing.T) {
	p := newPod(t, options{consumer: true}, answer("older"))
	_ = p.sub.Unsubscribe()
	n := newerPod(p, func(c context.Context, _ *sdk.Message, _ *turn.Meter) (a2a.Result, error) {
		<-c.Done()
		return a2a.Result{}, c.Err()
	})
	n.r.MaxDeliver = 1
	ctx, shutdown := context.WithCancel(context.Background())
	t.Cleanup(shutdown)
	n.hold(ctx)
	if !n.next(t) {
		t.Fatal("not subscribed")
	}
	p.advisory(t, "last", agent)
	for n.turns.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	shutdown()
	n.r.Wait()
	m := p.nextOf(t, kbus.DiscussionMessageMessageTypeFailure)
	if m.Metadata["failureType"] != "interrupted" || m.ThreadID != "last" {
		t.Fatalf("%+v", m)
	}
}
