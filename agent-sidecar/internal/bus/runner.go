package bus

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	kbus "github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/mesh"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/a2a"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/obs"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/placement"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

// Harness is the A2A client of the agent's harness: it takes one turn and can cancel a task.
type Harness interface {
	Do(ctx context.Context, msg *sdk.Message, onTask func(sdk.TaskID)) (a2a.Result, error)
	Cancel(ctx context.Context, id sdk.TaskID) error
}

// ToolGuard checks, before a turn starts, that the tools the agent declares are on the gateway.
type ToolGuard interface {
	// Missing returns the declared tools the gateway does not list, with a description of the
	// tool source for the failure signal. A gateway that cannot be reached reports every declared
	// tool missing.
	Missing(ctx context.Context) (tools []string, detail string)
}

// Default heartbeat timing while a turn runs: the first beat after 15 seconds, then every 30.
const (
	defaultHeartbeatFirst = 15 * time.Second
	defaultHeartbeatEvery = 30 * time.Second
	defaultNakDelay       = 5 * time.Second
	planTimeout           = 10 * time.Second
)

// Runner reads the crew's discussion subjects and answers the turns that select this agent.
type Runner struct {
	ID      mesh.Identity
	Bus     mesh.Bus
	Turn    mesh.Turn
	Conn    *nats.Conn
	JS      nats.JetStreamContext
	Harness Harness
	// Registry and Pub are required.
	Registry *turn.Registry
	Pub      *Publisher
	// Optional collaborators.
	Planner      placement.Planner
	Tools        ToolGuard
	Instructions Instructions
	Metrics      *obs.Metrics
	Log          *slog.Logger
	// Aliases are the models each alias stands for, named to the placement service when a plan is made;
	// ModelAliases is what the turn tells the harness it may name.
	Aliases      map[string][]string
	ModelAliases map[string]any
	// HeartbeatFirst and HeartbeatEvery time the heartbeat signal while a turn runs; zero means 15 s and 30 s.
	HeartbeatFirst, HeartbeatEvery time.Duration
	// NakDelay is how long JetStream waits before delivering again a message whose terminal signal
	// could not be published; zero means 5 s.
	NakDelay time.Duration
	// WarnEvery is how often the wait for the consumer is logged again; zero means a minute.
	WarnEvery time.Duration
	// MaxDeliver is the consumer's delivery limit; zero means the operator's 3.
	MaxDeliver int

	admit *Admission
	once  sync.Once
	sem   chan struct{}
	base  context.Context
	wg    sync.WaitGroup

	mu     sync.Mutex
	active map[string][]*running
	closed bool // Wait began: a message arriving now starts no turn
}

// running is one turn in flight, reachable by thread so a stop can end it.
type running struct {
	cancel context.CancelFunc
	task   sdk.TaskID
	mu     sync.Mutex
}

// taskID is the harness task the turn is working, once the harness has named it.
func (t *running) taskID() sdk.TaskID {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.task
}

// cancelHarness asks the harness to cancel the turn's task, if it has named one. It is best effort:
// a harness that cannot cancel finishes on its own and the sidecar has already stopped waiting.
func (t *running) cancelHarness(h Harness, log *slog.Logger) {
	id := t.taskID()
	if id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Cancel(ctx, id); err != nil {
		log.Debug("the harness did not cancel the task", "task", id, "error", err)
	}
}

func (r *Runner) setup() {
	r.once.Do(func() {
		r.admit = &Admission{Agent: r.ID.Agent, Role: r.Pub.Role}
		r.sem = make(chan struct{}, max(r.Turn.Concurrency, 1))
		r.base = context.Background()
		r.active = map[string][]*running{}
		if r.Log == nil {
			r.Log = slog.Default()
		}
	})
}

func (r *Runner) count(result string) {
	if r.Metrics != nil {
		r.Metrics.Delivery(result)
	}
}

// handle is the entry for every delivered message. It never blocks on a turn: an admitted message
// is worked on its own goroutine while its progress is reported.
func (r *Runner) handle(d delivery) {
	r.setup()
	var msg kbus.DiscussionMessage
	if err := json.Unmarshal(d.data, &msg); err != nil {
		r.count("malformed")
		d.term()
		return
	}
	if msg.Channel == "" {
		msg.Channel = channelOf(d.subject)
	}
	if msg.Channel != "" && !r.ID.Listens(msg.Channel) {
		r.count("ignored")
		d.ack()
		return
	}
	switch v := r.admit.Judge(msg); v {
	case Run:
		r.count("turn")
		r.start(d, msg)
		return
	case Stop:
		r.stopThread(msg.ThreadID)
		r.count("ignored")
	case Duplicate:
		r.count("duplicate")
	case NotSelected:
		r.count("not_selected")
	case Limited:
		r.count("limited")
	default:
		r.count("ignored")
	}
	d.ack()
}

// start works an admitted message: it plans the first model call, reports progress for as long as the
// turn lives, and runs the turn.
func (r *Runner) start(d delivery, msg kbus.DiscussionMessage) {
	t := &turnRun{r: r, d: d, msg: msg, thread: Thread{ID: msg.ThreadID, Channel: msg.Channel}}
	t.ctx, t.cancel = context.WithCancel(r.traceContext(d, msg))
	if !r.admitTurn() {
		t.cancel()
		r.giveBack(d, msg)
		return
	}
	r.register(msg.ThreadID, &t.running)
	go func() {
		defer r.wg.Done()
		stop := r.keepAlive(d)
		defer stop()
		defer r.unregister(msg.ThreadID, &t.running)
		t.run()
	}()
}

// admitTurn counts a turn in flight, unless Wait has begun.
func (r *Runner) admitTurn() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	r.wg.Add(1)
	return true
}

// Wait blocks until every turn in flight has ended; the context given to Subscribe or Hold ending
// cancels them. A message that arrives after Wait began is given back to the consumer unworked.
func (r *Runner) Wait() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.wg.Wait()
}

// traceContext continues the trace of the message: from its NATS header, else from its body fields.
func (r *Runner) traceContext(d delivery, msg kbus.DiscussionMessage) context.Context {
	ctx := obs.ExtractNATS(r.base, &nats.Msg{Header: d.hdr})
	return obs.FromFields(ctx, msg.Traceparent, msg.Tracestate)
}

// keepAlive tells JetStream every ProgressAck that the message is still being worked, until the
// returned stop is called. This is what keeps a turn longer than the consumer's AckWait from being
// delivered again; it reports state (a live turn), not a timeout.
func (r *Runner) keepAlive(d delivery) (stop func()) {
	if d.msg == nil {
		return func() {}
	}
	done := make(chan struct{})
	every := r.Bus.ProgressAck()
	go func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				if d.inProgress() && r.Metrics != nil {
					r.Metrics.ProgressAck()
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// plan asks the placement service to plan the agent's first model call as soon as the agent is
// selected, so a model can load while the harness starts. It is best effort.
func (r *Runner) plan(ctx context.Context, msg kbus.DiscussionMessage) {
	if r.Planner == nil || len(r.Aliases["default"]) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, planTimeout)
	defer cancel()
	err := r.Planner.Plan(ctx, placement.PlanRequest{Namespace: r.ID.Namespace, Crew: r.ID.Crew, Agent: r.ID.Agent,
		ThreadID: msg.ThreadID, Phase: "EVALUATING", Models: r.Aliases["default"], MayUnload: true})
	if err != nil {
		r.Log.Debug("placement plan not made", "thread", msg.ThreadID, "error", err)
	}
}

func (r *Runner) register(thread string, t *running) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active[thread] = append(r.active[thread], t)
}

func (r *Runner) unregister(thread string, t *running) {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.active[thread]
	for i, x := range list {
		if x == t {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(r.active, thread)
		return
	}
	r.active[thread] = list
}

// stopThread ends every turn running for a thread that ended or was stopped by a person: it
// cancels the turn's context and asks the harness to cancel the task. The stopped turn publishes
// nothing: the thread no longer waits for it. The harness is asked on its own goroutine, so a slow
// harness does not hold up the delivery of messages.
func (r *Runner) stopThread(thread string) {
	r.mu.Lock()
	turns := append([]*running(nil), r.active[thread]...)
	r.mu.Unlock()
	for _, t := range turns {
		t.cancel()
		go t.cancelHarness(r.Harness, r.Log)
	}
}

// newKey is the per-turn key the harness sends back as its OpenAI API key and MCP bearer token.
func newKey() string { return uuid.NewString() }
