package bus

import (
	"context"
	"errors"
	"strings"
	"time"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	kbus "github.com/kubemoot/kubemoot/operator/pkg/bus"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/a2a"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/obs"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

const (
	failureTools       = "tools_unavailable"
	failureDeadline    = "loop_time_exceeded"
	failureInterrupted = "interrupted"
	heartbeatText      = "Queued or running inference"
)

// turnRun is one turn from admission to the terminal signal.
type turnRun struct {
	running
	r      *Runner
	d      delivery
	msg    kbus.DiscussionMessage
	thread Thread
	ctx    context.Context
	meter  *turn.Meter
	key    string
	start  time.Time
}

// run works the turn end to end and settles the message once the terminal signal is out. A turn
// that was stopped by its thread ending publishes nothing.
func (t *turnRun) run() {
	defer t.cancel()
	ctx, span := obs.Tracer().Start(t.ctx, "turn", trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(attribute.String("kubemoot.thread", t.msg.ThreadID),
			attribute.String("kubemoot.trigger", string(t.msg.MessageType)), attribute.String("kubemoot.agent", t.r.ID.Agent)))
	defer span.End()
	t.ctx = ctx
	if !t.acquire() {
		t.settleStopped()
		return
	}
	defer func() { <-t.r.sem }()
	go t.r.plan(ctx, t.msg)
	t.start = time.Now()
	t.key = newKey()
	t.meter = t.r.Registry.Open(t.key, t.info())
	defer t.r.Registry.Close(t.key)
	t.signal(kbus.DiscussionMessageMessageTypeEvaluating, "", nil)
	stopBeats := t.heartbeats()
	out := t.work()
	stopBeats()
	if t.stopped() {
		t.settleStopped()
		return
	}
	t.finish(out, span)
}

// defaultMaxDeliver is the operator's MaxDeliver for every agent's consumer
// (operator/internal/nats/publisher.go). A message delivered this many times is dropped by the
// server when it is given back, so it cannot be given back again.
const defaultMaxDeliver = 3

func (r *Runner) maxDeliver() int {
	if r.MaxDeliver > 0 {
		return r.MaxDeliver
	}
	return defaultMaxDeliver
}

// settleStopped settles a turn that was ended from outside. When its thread ended nobody waits for
// the message, so it is acknowledged. When the sidecar is shutting down the thread is still open, so
// the message is given back and the next pod to bind the consumer gets it; on its last delivery
// the server would drop a give-back silently, so the thread is told the turn was interrupted.
func (t *turnRun) settleStopped() {
	switch {
	case t.r.base.Err() == nil:
		t.d.ack()
	case t.d.delivered() >= uint64(t.r.maxDeliver()):
		t.interrupted()
	default:
		t.giveBack()
	}
}

// giveBack returns the message to the consumer unworked. The admission forgets it, so a delivery
// to this same runner is judged afresh and not dropped as a duplicate, and the wait keeps the
// server from handing it straight back to a pod that is leaving.
func (t *turnRun) giveBack() { t.r.giveBack(t.d, t.msg) }

func (r *Runner) giveBack(d delivery, msg kbus.DiscussionMessage) {
	r.admit.Forget(msg.MessageID, msg.ThreadID)
	d.nak(r.nakDelay())
}

// interrupted tells the thread a turn was cut by shutdown with no delivery left, as a failure
// signal on a context of its own because the turn's context has ended, then acknowledges. If the
// signal cannot be published the message is given back after all.
func (t *turnRun) interrupted() {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.ctx), 5*time.Second)
	defer cancel()
	md := map[string]any{"failureType": failureInterrupted,
		"lastError": "the agent's pod was replaced while the turn ran, and the message has no deliveries left"}
	err := t.r.Pub.Signal(ctx, t.thread, kbus.DiscussionMessageMessageType(a2a.SignalFailure),
		"The turn was interrupted by a restart of the agent.", md)
	if err != nil {
		t.giveBack()
		return
	}
	t.d.ack()
}

// acquire takes a turn slot. A turn that has to wait for one says so on the bus, so the coordinator
// sees a live agent, and gives up if it is stopped while it waits.
func (t *turnRun) acquire() bool {
	select {
	case t.r.sem <- struct{}{}:
	default:
		t.signal(kbus.DiscussionMessageMessageTypeWaiting, "", map[string]any{"reason": "queued"})
		select {
		case t.r.sem <- struct{}{}:
		case <-t.ctx.Done():
			return false
		}
	}
	if t.ctx.Err() != nil { // stopped in the same moment the slot came free
		<-t.r.sem
		return false
	}
	return true
}

// info is what the doors need to know about the turn.
func (t *turnRun) info() turn.Info {
	date, _ := t.msg.Metadata["requestDate"].(string)
	zone, _ := t.msg.Metadata["timeZone"].(string)
	return turn.Info{ThreadID: t.msg.ThreadID, RequestDate: date, TimeZone: zone, OnWaiting: t.waiting}
}

// stopped is true when the turn was ended from outside (the thread closed or a person stopped
// it), as opposed to running past its own deadline.
func (t *turnRun) stopped() bool {
	return errors.Is(t.ctx.Err(), context.Canceled)
}

// waiting publishes that the turn waits for capacity or runs without the placement service.
func (t *turnRun) waiting(reason string) {
	t.signal(kbus.DiscussionMessageMessageTypeWaiting, "", map[string]any{"reason": reason})
}

func (t *turnRun) signal(typ kbus.DiscussionMessageMessageType, content string, md map[string]any) {
	if err := t.r.Pub.Signal(t.ctx, t.thread, typ, content, md); err != nil {
		t.r.Log.Warn("signal not published", "type", typ, "thread", t.thread.ID, "error", err)
	}
}

// heartbeats publishes a heartbeat signal while the turn runs, so the coordinator keeps waiting for
// a live agent. The returned stop ends them.
func (t *turnRun) heartbeats() (stop func()) {
	first, every := t.r.HeartbeatFirst, t.r.HeartbeatEvery
	if first <= 0 {
		first = defaultHeartbeatFirst
	}
	if every <= 0 {
		every = defaultHeartbeatEvery
	}
	done := make(chan struct{})
	go func() {
		timer := time.NewTimer(first)
		defer timer.Stop()
		for {
			select {
			case <-done:
				return
			case <-timer.C:
				t.signal(kbus.DiscussionMessageMessageTypeHeartbeat, heartbeatText, nil)
				timer.Reset(every)
			}
		}
	}()
	return func() { close(done) }
}

// work runs the harness and reduces its reply to an outcome. A declared tool the gateway does not
// list ends the turn before the harness is called.
func (t *turnRun) work() a2a.Outcome {
	if t.r.Tools != nil {
		if missing, detail := t.r.Tools.Missing(t.ctx); len(missing) > 0 {
			return a2a.Outcome{Signal: a2a.SignalFailure, FailureType: failureTools,
				Content: "Declared tools unavailable: " + strings.Join(missing, ", "), Reason: detail,
				Evidence: []a2a.Evidence{{Tool: strings.Join(missing, ","), Note: detail}}}
		}
	}
	ctx, cancel := context.WithTimeout(t.ctx, t.r.Turn.Deadline())
	defer cancel()
	res, err := t.r.Harness.Do(ctx, t.message(), t.onTask)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.cancelHarness(t.r.Harness, t.r.Log)
		return a2a.Outcome{Signal: a2a.SignalFailure, FailureType: failureDeadline,
			Reason: "the harness did not answer within " + t.r.Turn.Deadline().String()}
	}
	return a2a.Interpret(res, err)
}

func (t *turnRun) onTask(id sdk.TaskID) {
	t.mu.Lock()
	t.task = id
	t.mu.Unlock()
}

// message builds the A2A message of the turn.
func (t *turnRun) message() *sdk.Message {
	entries := t.r.admit.Thread(t.msg.ThreadID)
	in := a2a.TurnInput{Kind: "discussion", Agent: t.r.ID.Agent, Crew: t.r.ID.Crew, Role: t.r.Pub.Role,
		Phase: phaseOf(t.msg.MessageType), Instructions: t.r.Instructions.For(skillsOf(t.msg)), Thread: entries,
		Models: t.r.ModelAliases, TurnKey: t.key, DeadlineSeconds: t.r.Turn.DeadlineSeconds,
		Now: time.Now().UTC().Format(time.RFC3339)}
	in.RequestDate, in.TimeZone = t.meter.Info().RequestDate, t.meter.Info().TimeZone
	return a2a.NewTurn(t.msg.ThreadID, questionOf(t.msg), in)
}

func phaseOf(t kbus.DiscussionMessageMessageType) string {
	if t == typeReviewReady {
		return a2a.PhaseReview
	}
	return a2a.PhaseEvaluating
}

// questionOf is the person's question when the message carries it, else the message text.
func questionOf(m kbus.DiscussionMessage) string {
	if q, _ := m.Metadata["userQuery"].(string); q != "" {
		return q
	}
	return m.Content
}

func skillsOf(m kbus.DiscussionMessage) []string {
	raw, _ := m.Metadata["selectedSkills"].([]any)
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		if name, ok := s.(string); ok {
			out = append(out, name)
		}
	}
	return out
}

// finish publishes the terminal signal with the facts the sidecar measured, then settles the message.
// A turn whose call no server could take is a capacity stand-aside, not a failure.
func (t *turnRun) finish(out a2a.Outcome, span trace.Span) {
	if kind := t.meter.NoFitKind(); kind != "" && out.Signal == a2a.SignalFailure {
		out = a2a.Outcome{Signal: a2a.SignalStandAside, Reason: kind}
	}
	took := time.Since(t.start)
	md := t.meter.Snapshot()
	md["turnMs"] = took.Milliseconds()
	annotate(md, out)
	span.SetAttributes(attribute.String("kubemoot.signal", out.Signal))
	if out.Signal == a2a.SignalFailure {
		span.SetStatus(codes.Error, out.FailureType)
	}
	content := out.Content
	if out.Signal == a2a.SignalFailure && content == "" {
		content = "The harness did not produce an answer: " + out.FailureType
	}
	if t.r.Metrics != nil {
		t.r.Metrics.Turn(out.Signal, took)
	}
	err := t.r.Pub.Signal(t.ctx, t.thread, kbus.DiscussionMessageMessageType(out.Signal), content, md)
	if err != nil {
		t.r.Log.Warn("terminal signal not published; asking for the message again", "thread", t.thread.ID, "error", err)
		t.giveBack()
		return
	}
	t.d.ack()
}

func (r *Runner) nakDelay() time.Duration {
	if r.NakDelay > 0 {
		return r.NakDelay
	}
	return defaultNakDelay
}

// annotate adds the outcome's reason, failure type and evidence to the metadata.
func annotate(md map[string]any, out a2a.Outcome) {
	if out.Reason != "" {
		md["reason"] = truncate(out.Reason, 300)
	}
	if out.FailureType != "" {
		md["failureType"] = out.FailureType
	}
	if out.Signal == a2a.SignalFailure && out.Reason != "" {
		md["lastError"] = truncate(out.Reason, 500)
	}
	if len(out.Evidence) > 0 {
		md["evidence"] = out.Evidence
	}
}
