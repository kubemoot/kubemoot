package nats

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/operator/pkg/natsconn/natstest"
)

const (
	runningPhase = "Running"
	phaseKey     = "phase"
)

// counter subscribes to every subject and counts what arrives.
func counter(t *testing.T, p *Publisher) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	sub, err := p.Subscribe(">", func(_ *nats.Msg) { n.Add(1) })
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return &n
}

// settle flushes the connection and waits until the count is stable at want or gives up.
func settle(t *testing.T, p *Publisher, n *atomic.Int64, want int64) {
	t.Helper()
	p.mu.Lock()
	_ = p.conn.Flush()
	p.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for n.Load() < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // lets an unwanted extra message arrive
	if got := n.Load(); got != want {
		t.Fatalf("messages = %d, want %d", got, want)
	}
}

func TestPublishOnChange_SendsOncePerChange(t *testing.T) {
	env := natstest.Start(t, natstest.Options{})
	p := NewPublisher(env.URL)
	defer p.Close()
	n := counter(t, p)

	same := map[string]any{phaseKey: runningPhase, "ready": true}
	for i := 0; i < 3; i++ {
		if err := p.PublishOnChange("kubemoot.operator.mcpserver.a.status", same); err != nil {
			t.Fatal(err)
		}
	}
	settle(t, p, n, 1)

	_ = p.PublishOnChange("kubemoot.operator.mcpserver.a.status", map[string]any{phaseKey: "Failed", "ready": false})
	settle(t, p, n, 2)

	// Changing back is a change from the last published payload.
	_ = p.PublishOnChange("kubemoot.operator.mcpserver.a.status", same)
	settle(t, p, n, 3)
}

func TestPublishOnChange_TracksEachSubjectSeparately(t *testing.T) {
	env := natstest.Start(t, natstest.Options{})
	p := NewPublisher(env.URL)
	defer p.Close()
	n := counter(t, p)

	payload := map[string]any{phaseKey: runningPhase}
	_ = p.PublishOnChange("kubemoot.operator.mcpserver.a.status", payload)
	_ = p.PublishOnChange("kubemoot.operator.mcpserver.b.status", payload)
	_ = p.PublishOnChange("kubemoot.operator.mcpserver.a.status", payload)
	settle(t, p, n, 2)
}

func TestPublishOnChange_RestartPublishesOnce(t *testing.T) {
	env := natstest.Start(t, natstest.Options{})
	first := NewPublisher(env.URL)
	defer first.Close()
	n := counter(t, first)
	payload := map[string]any{"verdict": "use"}

	_ = first.PublishOnChange("kubemoot.chronicle.a.verdict", payload)
	_ = first.PublishOnChange("kubemoot.chronicle.a.verdict", payload)
	settle(t, first, n, 1)

	restarted := NewPublisher(env.URL) // an operator restart has no memory of the last payload
	defer restarted.Close()
	_ = restarted.PublishOnChange("kubemoot.chronicle.a.verdict", payload)
	_ = restarted.PublishOnChange("kubemoot.chronicle.a.verdict", payload)
	settle(t, first, n, 2)
}

func TestPublishOnChange_ForgetPublishedAllowsRepublish(t *testing.T) {
	env := natstest.Start(t, natstest.Options{})
	p := NewPublisher(env.URL)
	defer p.Close()
	n := counter(t, p)
	payload := map[string]any{phaseKey: runningPhase}

	_ = p.PublishOnChange("s.a", payload)
	p.ForgetPublished("s.a")
	_ = p.PublishOnChange("s.a", payload)
	settle(t, p, n, 2)
}

func TestPublishOnChange_UnconfiguredIsNoOpAndUnencodableErrors(t *testing.T) {
	t.Setenv("NATS_URL", "")
	p := NewPublisher("")
	if err := p.PublishOnChange("s.a", map[string]any{"a": 1}); err != nil {
		t.Fatalf("unconfigured publisher must be a no-op, got %v", err)
	}
	configured := &Publisher{url: "nats://127.0.0.1:1"}
	if err := configured.PublishOnChange("s.a", make(chan int)); err == nil {
		t.Fatal("a payload that cannot be encoded must return an error")
	}
}

func TestPublishOnChange_UnsentPayloadIsNotRemembered(t *testing.T) {
	p := &Publisher{url: "nats://127.0.0.1:1"} // nothing listens here
	if err := p.PublishOnChange("s.a", map[string]any{"a": 1}); err != nil {
		t.Fatalf("a failed connection is not an error, got %v", err)
	}
	if len(p.lastSent) != 0 {
		t.Fatal("a payload that did not go out must be tried again, not remembered")
	}
}
