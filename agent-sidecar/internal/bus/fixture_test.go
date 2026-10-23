package bus_test

import (
	"context"
	"encoding/json"
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
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/obs"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/placement"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

const (
	askNodes     = "Which nodes have a GPU?"
	modelQwen    = "qwen3:14b"
	typeAdvisory = "advisory_ready"
	toolFetch    = "fetch"
	crewPilot    = "pilot"
	aliasDefault = "default"
	agent        = "notes-reader"
	general      = "general"
	waitFor      = 10 * time.Second
)

var id = mesh.Identity{Namespace: "homelab", Crew: crewPilot, Agent: agent, Role: "tooler", Channels: []string{general}}

// harness is a scripted harness. do receives the turn context, the A2A message and the turn's meter.
type harness struct {
	do       func(ctx context.Context, msg *sdk.Message, m *turn.Meter) (a2a.Result, error)
	reg      *turn.Registry
	calls    atomic.Int32
	canceled chan sdk.TaskID
	task     sdk.TaskID
	mu       sync.Mutex
	msgs     []*sdk.Message
	ctxs     []context.Context
}

func answer(text string) func(context.Context, *sdk.Message, *turn.Meter) (a2a.Result, error) {
	return func(context.Context, *sdk.Message, *turn.Meter) (a2a.Result, error) {
		return a2a.Result{State: sdk.TaskStateCompleted, Parts: []*sdk.Part{sdk.NewTextPart(text)}}, nil
	}
}

func (h *harness) Do(ctx context.Context, msg *sdk.Message, onTask func(sdk.TaskID)) (a2a.Result, error) {
	h.calls.Add(1)
	h.mu.Lock()
	h.msgs = append(h.msgs, msg)
	h.ctxs = append(h.ctxs, ctx)
	h.mu.Unlock()
	if h.task != "" && onTask != nil {
		onTask(h.task)
	}
	return h.do(ctx, msg, h.meterOf(msg))
}

func (h *harness) meterOf(msg *sdk.Message) *turn.Meter {
	d, _ := json.Marshal(msg.Parts[1].Data())
	var in a2a.TurnInput
	_ = json.Unmarshal(d, &in)
	return h.reg.Lookup(in.TurnKey)
}

func (h *harness) Cancel(_ context.Context, id sdk.TaskID) error {
	select {
	case h.canceled <- id:
	default:
	}
	return nil
}

func (h *harness) firstMessage() *sdk.Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.msgs[0]
}

// pod is a sidecar under test: a runner on a real NATS server with JetStream, and a recorder of
// everything the agent publishes.
type pod struct {
	url      string
	nc       *nats.Conn
	js       nats.JetStreamContext
	runner   *bus.Runner
	pub      *bus.Publisher
	reg      *turn.Registry
	h        *harness
	got      chan kbus.DiscussionMessage
	raw      chan []byte
	artifact chan []byte
	tools    *fakeTools
	planner  *fakePlanner
	metrics  *obs.Metrics
	sub      *nats.Subscription
}

// deliveries is how many bus messages the sidecar counted with the given result.
func (p *pod) deliveries(t *testing.T, result string) float64 {
	t.Helper()
	families, err := p.metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "kubemoot_sidecar_deliveries_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "result" && l.GetValue() == result {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

type fakeTools struct {
	missing []string
	detail  string
}

func (f *fakeTools) Missing(context.Context) ([]string, string) { return f.missing, f.detail }

type fakePlanner struct {
	mu    sync.Mutex
	plans []placement.PlanRequest
}

func (f *fakePlanner) Plan(_ context.Context, r placement.PlanRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plans = append(f.plans, r)
	return nil
}

type options struct {
	consumer    bool          // bind the operator's durable consumer instead of a core subscription
	ackWait     time.Duration // the consumer's AckWait
	progressSec int           // contract bus.progressAckSeconds
	deadline    time.Duration
	concurrency int
	role        string
}

// newPod starts a sidecar whose harness runs script.
func newPod(t *testing.T, o options, script func(context.Context, *sdk.Message, *turn.Meter) (a2a.Result, error)) *pod {
	t.Helper()
	return newPodWith(t, context.Background(), o, script)
}

// newPodWith is newPod with the context the runner's subscription lives in: ending it is the sidecar's shutdown.
func newPodWith(t *testing.T, ctx context.Context, o options,
	script func(context.Context, *sdk.Message, *turn.Meter) (a2a.Result, error)) *pod {
	t.Helper()
	url := bustest.Server(t)
	nc := bustest.Connect(t, url)
	js := bustest.JetStream(t, nc)
	bustest.DiscussStream(t, js)
	if o.ackWait == 0 {
		o.ackWait = 30 * time.Second
	}
	if o.deadline == 0 {
		o.deadline = 30 * time.Second
	}
	if o.concurrency == 0 {
		o.concurrency = 1
	}
	identity := id
	if o.role != "" {
		identity.Role = o.role
	}
	b := mesh.Bus{NATSURL: url, ProgressAckSeconds: o.progressSec}
	if o.consumer {
		bustest.Consumer(t, js, identity, o.ackWait)
		b.Stream, b.Consumer = kbus.DiscussStream, identity.ConsumerName()
	}
	reg := turn.NewRegistry()
	h := &harness{do: script, reg: reg, canceled: make(chan sdk.TaskID, 4)}
	metrics := obs.New(obs.Labels{Namespace: "homelab", Crew: crewPilot, Agent: agent}, "algorithm")
	p := &pod{metrics: metrics, url: url, nc: nc, js: js, reg: reg, h: h, tools: &fakeTools{}, planner: &fakePlanner{},
		got: make(chan kbus.DiscussionMessage, 64), raw: make(chan []byte, 64), artifact: make(chan []byte, 8)}
	p.pub = &bus.Publisher{Conn: nc, JS: js, ID: identity, Role: identity.Role, Harness: "test-harness",
		HarnessImage: "registry/harness@sha256:abc", Model: modelQwen, EnabledTools: []string{toolFetch}, Metrics: metrics}
	p.runner = &bus.Runner{ID: identity, Bus: b, Turn: mesh.Turn{Concurrency: o.concurrency,
		DeadlineSeconds: int(o.deadline / time.Second)}, Conn: nc, JS: js, Harness: h, Registry: reg, Pub: p.pub,
		Planner: p.planner, Tools: p.tools, Metrics: metrics, Aliases: map[string][]string{aliasDefault: {modelQwen}},
		ModelAliases: map[string]any{aliasDefault: aliasDefault}, HeartbeatFirst: 200 * time.Millisecond,
		HeartbeatEvery: 200 * time.Millisecond, NakDelay: 100 * time.Millisecond}
	p.record(t)
	sub, err := p.runner.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.sub = sub
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return p
}

// send publishes a coordinator or human message onto the crew's thread subject.
func (p *pod) send(t *testing.T, typ, from, thread string, md map[string]any) kbus.DiscussionMessage {
	t.Helper()
	return p.sendWith(t, typ, from, thread, "", md, nil)
}

func (p *pod) sendWith(t *testing.T, typ, from, thread, content string, md map[string]any, hdr nats.Header) kbus.DiscussionMessage {
	t.Helper()
	msg := kbus.DiscussionMessage{MessageID: typ + "-" + thread + "-" + time.Now().Format("150405.000000000"),
		ThreadID: thread, AgentName: from, MessageType: kbus.DiscussionMessageMessageType(typ), Content: content,
		Channel: general, Metadata: md}
	data, _ := json.Marshal(msg)
	m := &nats.Msg{Subject: id.DiscussSubject(general, thread), Data: data, Header: hdr}
	if err := p.nc.PublishMsg(m); err != nil {
		t.Fatal(err)
	}
	return msg
}

// advisory publishes the coordinator's advisory_ready convening the named agents (none: everyone).
func (p *pod) advisory(t *testing.T, thread string, circle ...string) kbus.DiscussionMessage {
	t.Helper()
	md := map[string]any{"userQuery": askNodes}
	if len(circle) > 0 {
		in := make([]any, len(circle))
		for i, c := range circle {
			in[i] = c
		}
		md["innerCircle"] = in
	}
	return p.send(t, typeAdvisory, "coordinator", thread, md)
}

// next returns the agent's next published message, validated against the bus contract.
func (p *pod) next(t *testing.T) kbus.DiscussionMessage {
	t.Helper()
	select {
	case raw := <-p.raw:
		bustest.Validate(t, "discussion-message.json", raw)
		return <-p.got
	case <-time.After(waitFor):
		t.Fatal("the agent published nothing")
		return kbus.DiscussionMessage{}
	}
}

// nextOf returns the next message of a type, skipping heartbeats.
func (p *pod) nextOf(t *testing.T, typ kbus.DiscussionMessageMessageType) kbus.DiscussionMessage {
	t.Helper()
	for {
		m := p.next(t)
		if m.MessageType == typ {
			return m
		}
		if m.MessageType != kbus.DiscussionMessageMessageTypeHeartbeat &&
			m.MessageType != kbus.DiscussionMessageMessageTypeEvaluating &&
			m.MessageType != kbus.DiscussionMessageMessageTypeWaiting {
			t.Fatalf("wanted %s, the agent sent %s: %s", typ, m.MessageType, m.Content)
		}
	}
}

// quiet fails if the agent publishes anything within d.
func (p *pod) quiet(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case m := <-p.got:
		t.Fatalf("the agent should be quiet but sent %s", m.MessageType)
	case <-time.After(d):
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func jsonOf(v any) ([]byte, error) { return json.Marshal(v) }

// dataOf decodes the turn profile data part of an A2A message.
func dataOf(t *testing.T, msg *sdk.Message) a2a.TurnInput {
	t.Helper()
	raw, err := json.Marshal(msg.Parts[1].Data())
	if err != nil {
		t.Fatal(err)
	}
	var in a2a.TurnInput
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	return in
}

// record subscribes a recorder on its own connection, so it sees the agent's messages as any reader does.
func (p *pod) record(t *testing.T) {
	t.Helper()
	rc := bustest.Connect(t, p.url)
	if _, err := rc.Subscribe("kubemoot.discuss.>", func(m *nats.Msg) {
		var msg kbus.DiscussionMessage
		if json.Unmarshal(m.Data, &msg) == nil && msg.AgentName == agent {
			p.raw <- m.Data
			p.got <- msg
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := rc.Subscribe("kubemoot.artifacts.>", func(m *nats.Msg) { p.artifact <- m.Data }); err != nil {
		t.Fatal(err)
	}
	if err := rc.Flush(); err != nil {
		t.Fatal(err)
	}
}
