package bus_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	kbus "github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/mesh"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/a2a"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/bus"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/bustest"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/obs"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

// consumerInfo reads the agent's durable consumer.
func consumerInfo(t *testing.T, p *pod) *nats.ConsumerInfo {
	t.Helper()
	ci, err := p.js.ConsumerInfo(kbus.DiscussStream, id.ConsumerName())
	if err != nil {
		t.Fatal(err)
	}
	return ci
}

func TestTheOperatorsDurableConsumerIsBoundAndTheMessageSettledAfterTheSignal(t *testing.T) {
	p := newPod(t, options{consumer: true}, answer("bound"))
	p.advisory(t, "j1", agent)
	p.nextOf(t, agree)
	deadline := time.Now().Add(waitFor)
	for consumerInfo(t, p).NumAckPending != 0 || consumerInfo(t, p).AckFloor.Consumer == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the message was never acknowledged: %+v", consumerInfo(t, p))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A turn that outlasts the consumer's AckWait must not be delivered again while it runs: the sidecar
// reports progress for as long as the turn lives. The consumer here has the operator's shape (explicit
// acknowledgement, MaxDeliver 3) with an AckWait of 3 seconds and a turn of 5.
func TestAProgressAckKeepsALongTurnFromBeingDeliveredAgain(t *testing.T) {
	p := newPod(t, options{consumer: true, ackWait: 3 * time.Second, progressSec: 1}, func(ctx context.Context, _ *sdk.Message,
		_ *turn.Meter) (a2a.Result, error) {
		time.Sleep(5 * time.Second)
		return answer("worth the wait")(nil, nil, nil)
	})
	p.advisory(t, "j2", agent)
	p.nextOf(t, agree)
	ci := consumerInfo(t, p)
	if d := p.deliveries(t, "duplicate"); ci.NumRedelivered != 0 || d != 0 {
		t.Fatalf("a live turn was delivered again (%v duplicates): %+v", d, ci)
	}
	if n := p.h.calls.Load(); n != 1 {
		t.Fatalf("%d turns ran", n)
	}
}

// The control for the test above: without progress acks the same turn is delivered again, which is
// what the sidecar prevents.
func TestWithoutProgressAcksTheSameTurnIsDeliveredAgain(t *testing.T) {
	p := newPod(t, options{consumer: true, ackWait: time.Second, progressSec: 3600}, func(ctx context.Context, _ *sdk.Message,
		_ *turn.Meter) (a2a.Result, error) {
		time.Sleep(2500 * time.Millisecond)
		return answer("slow")(nil, nil, nil)
	})
	p.advisory(t, "j3", agent)
	p.nextOf(t, agree)
	if p.deliveries(t, "duplicate") == 0 {
		t.Fatal("the control must show a redelivery, or the test above proves nothing")
	}
}

func TestAMalformedMessageIsTerminatedNotRedelivered(t *testing.T) {
	p := newPod(t, options{consumer: true, ackWait: time.Second}, answer("never"))
	if err := p.nc.Publish(id.DiscussSubject(general, "j4"), []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	ci := consumerInfo(t, p)
	if ci.NumRedelivered != 0 || ci.NumAckPending != 0 {
		t.Fatalf("a message that can never be processed must be settled: %+v", ci)
	}
	if p.h.calls.Load() != 0 {
		t.Fatal("no turn for garbage")
	}
}

func TestAMessageForAnotherAgentIsAcknowledgedAtOnce(t *testing.T) {
	p := newPod(t, options{consumer: true, ackWait: time.Second}, answer("never"))
	p.advisory(t, "j5", "someone-else")
	time.Sleep(2500 * time.Millisecond)
	if ci := consumerInfo(t, p); ci.NumPending != 0 || ci.NumAckPending != 0 || ci.NumRedelivered != 0 {
		t.Fatalf("%+v", ci)
	}
}

func TestOneTurnAtATimeAndTheWaitingMessageIsKeptAlive(t *testing.T) {
	gate := make(chan struct{})
	p := newPod(t, options{consumer: true, ackWait: 3 * time.Second, progressSec: 1}, func(ctx context.Context, _ *sdk.Message,
		_ *turn.Meter) (a2a.Result, error) {
		<-gate
		return answer("ok")(nil, nil, nil)
	})
	p.advisory(t, "k1", agent)
	p.nextOf(t, evaluating)
	p.advisory(t, "k2", agent)
	time.Sleep(4500 * time.Millisecond) // longer than AckWait: the queued message must still be kept alive
	if n := p.h.calls.Load(); n != 1 {
		t.Fatalf("concurrency 1 allows one running turn, %d are", n)
	}
	close(gate)
	p.nextOf(t, agree)
	p.nextOf(t, agree)
	if d := p.deliveries(t, "duplicate"); d != 0 {
		t.Fatalf("the queued message was delivered again %v times", d)
	}
}

func TestALargeContributionIsSpilledAndAnnounced(t *testing.T) {
	big := strings.Repeat("node-row\n", 800) // 7200 bytes
	p := newPod(t, options{}, answer(big))
	p.advisory(t, "l1", agent)
	m := p.nextOf(t, agree)
	if !strings.HasPrefix(m.Content, "[ARTIFACT key=") || contains(m.Content, "node-row") {
		t.Fatalf("the inline content is a marker with no data: %.120s", m.Content)
	}
	ref, _ := m.Metadata["artifact"].(map[string]any)
	key, _ := ref["key"].(string)
	if !strings.HasPrefix(key, "homelab/pilot/l1/"+agent+"/agree-") || ref["bytes"] != float64(len(big)) {
		t.Fatalf("%v", ref)
	}
	store, err := p.js.ObjectStore(mesh.ArtifactBucket)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetBytes(key)
	if err != nil || string(got) != big {
		t.Fatalf("the full contribution must be in the store: %v", err)
	}
	select {
	case notice := <-p.artifact:
		bustest.Validate(t, "artifact-notice.json", notice)
	case <-time.After(waitFor):
		t.Fatal("the materializer was not told")
	}
}

func TestASmallContributionStaysInline(t *testing.T) {
	p := newPod(t, options{}, answer(strings.Repeat("x", 4000)))
	p.advisory(t, "l2", agent)
	m := p.nextOf(t, agree)
	if len(m.Content) != 4000 || m.Metadata["artifact"] != nil {
		t.Fatalf("%d %v", len(m.Content), m.Metadata["artifact"])
	}
}

func TestWithoutJetStreamALargeContributionIsTruncatedAndSaysWhy(t *testing.T) {
	p := newPod(t, options{}, answer(strings.Repeat("y", 9000)))
	p.pub.JS = nil
	p.advisory(t, "l3", agent)
	m := p.nextOf(t, agree)
	if len(m.Content) != 5000 || m.Metadata["lastError"] == nil {
		t.Fatalf("%d %v", len(m.Content), m.Metadata)
	}
}

func TestTheTraceContinuesFromTheIncomingMessageThroughTheHarnessToEverySignal(t *testing.T) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.TraceContext{})
	const trace1 = "0af7651916cd43dd8448eb211c80319c"
	p := newPod(t, options{}, answer("traced"))
	hdr := nats.Header{}
	hdr.Set(obs.TraceparentHeader, "00-"+trace1+"-b7ad6b7169203331-01")
	p.sendWith(t, typeAdvisory, "coordinator", "m1", "", map[string]any{"innerCircle": []any{agent}}, hdr)
	for _, typ := range []kbus.DiscussionMessageMessageType{evaluating, agree} {
		m := p.nextOf(t, typ)
		if !strings.Contains(m.Traceparent, trace1) {
			t.Errorf("%s carries traceparent %q, want trace %s", typ, m.Traceparent, trace1)
		}
	}
	p.h.mu.Lock()
	defer p.h.mu.Unlock()
	if sc := trace.SpanContextFromContext(p.h.ctxs[0]); sc.TraceID().String() != trace1 {
		t.Errorf("the harness ran in trace %s, want %s", sc.TraceID(), trace1)
	}
}

func TestTheTraceCanComeFromTheBodyWhenTheHeaderIsAbsent(t *testing.T) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.TraceContext{})
	const trace2 = "1af7651916cd43dd8448eb211c80319d"
	p := newPod(t, options{}, answer("traced"))
	msg := kbus.DiscussionMessage{MessageID: "tb1", ThreadID: "m2", AgentName: "coordinator", MessageType: typeAdvisory,
		Channel: general, Traceparent: "00-" + trace2 + "-b7ad6b7169203331-01"}
	data, _ := jsonOf(msg)
	if err := p.nc.Publish(id.DiscussSubject(general, "m2"), data); err != nil {
		t.Fatal(err)
	}
	if m := p.nextOf(t, agree); !strings.Contains(m.Traceparent, trace2) {
		t.Errorf("%q", m.Traceparent)
	}
}

func TestPublishedSignalsCarryTheTraceInTheNATSHeaderToo(t *testing.T) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.TraceContext{})
	p := newPod(t, options{}, answer("x"))
	rc := bustest.Connect(t, p.url)
	headers := make(chan string, 8)
	if _, err := rc.Subscribe("kubemoot.discuss.>", func(m *nats.Msg) {
		if strings.Contains(string(m.Data), `"agentName":"`+agent+`"`) {
			headers <- m.Header.Get(obs.TraceparentHeader)
		}
	}); err != nil {
		t.Fatal(err)
	}
	_ = rc.Flush()
	p.advisory(t, "m3", agent)
	select {
	case h := <-headers:
		if !strings.HasPrefix(h, "00-") {
			t.Errorf("header %q", h)
		}
	case <-time.After(waitFor):
		t.Fatal("no signal")
	}
}

func TestLifecycleAnnouncesWakingThenReadyAndKeepsTheHeartbeatFresh(t *testing.T) {
	p := newPod(t, options{}, answer("x"))
	life := make(chan kbus.DiscussionMessage, 4)
	rc := bustest.Connect(t, p.url)
	if _, err := rc.Subscribe("kubemoot.lifecycle.>", func(m *nats.Msg) {
		bustest.Validate(t, "discussion-message.json", m.Data)
		var msg kbus.DiscussionMessage
		_ = json.Unmarshal(m.Data, &msg)
		life <- msg
	}); err != nil {
		t.Fatal(err)
	}
	_ = rc.Flush()
	var isReady atomic.Bool
	l := &bus.Lifecycle{Pub: p.pub, Ready: isReady.Load, NATS: func() bool { return true }, Harness: isReady.Load,
		Poll: 50 * time.Millisecond, Beat: 100 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)
	if m := <-life; m.MessageType != kbus.DiscussionMessageMessageTypeWaking {
		t.Fatalf("first %s", m.MessageType)
	}
	select {
	case m := <-life:
		t.Fatalf("not ready yet, but the agent said %s", m.MessageType)
	case <-time.After(200 * time.Millisecond):
	}
	isReady.Store(true)
	if m := <-life; m.MessageType != kbus.DiscussionMessageMessageTypeReady {
		t.Fatalf("then %s", m.MessageType)
	}
	bustest.Validate(t, "agent-heartbeat.json", heartbeatEntry(t, p))
}

// heartbeatEntry waits for the agent's liveness entry and returns its value.
func heartbeatEntry(t *testing.T, p *pod) []byte {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if kv, err := p.js.KeyValue(mesh.AgentStateBucket); err == nil {
			if entry, err := kv.Get(id.HeartbeatKey()); err == nil {
				return entry.Value()
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("no heartbeat entry")
	return nil
}
