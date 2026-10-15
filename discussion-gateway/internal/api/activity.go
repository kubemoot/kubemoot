package api

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

const (
	// eventActivity is the SSE event that reports who is busy on the followed thread.
	eventActivity = "activity"
	// activityInterval is how often an activity event is sent while someone is busy.
	activityInterval = 15 * time.Second
	// threadBeatWindow is how recent an agent's last thread message must be to count
	// as alive without its liveness heartbeat: an evaluating agent publishes a
	// discussion heartbeat every 30 seconds, so two periods plus slack.
	threadBeatWindow = 75 * time.Second
	// agentStateBucket holds each agent's liveness heartbeat, keyed <namespace>.<agent>.
	// It is the agent runtime's default bucket name (agent.heartbeat.kv-bucket).
	agentStateBucket = "kubemoot_agent_state"
	// fallbackBeatInterval is the heartbeat interval assumed when a heartbeat carries
	// none, and maxBeatInterval the largest one taken as plausible. The staleness rule
	// is the dashboard's (dashboard/src/lib/agent-liveness.ts).
	fallbackBeatInterval = 60
	maxBeatInterval      = 3600
	// livenessReadTimeout bounds one heartbeat read, so a slow NATS cannot hold up the
	// stream's event loop.
	livenessReadTimeout = 2 * time.Second
)

// What a busy agent is doing, as the activity event reports it.
const (
	doingWaking                = "waking"
	doingTriaging              = "triaging"
	doingEvaluating            = "evaluating"
	doingWaitingForAccelerator = "waiting_for_accelerator"
	doingReasoning             = "reasoning"
	doingWaitingForAgents      = "waiting_for_agents"
	doingSynthesizing          = "synthesizing"
)

// NATS discussion message types that change what the coordinator is doing.
const (
	msgAdvisory      = "advisory"
	msgAdvisoryReady = "advisory_ready"
	msgReviewReady   = "review_ready"
	msgHeartbeat     = "heartbeat"
)

// Terminal signals older agents send in place of agree and stand_aside.
const (
	signalContribution = "contribution"
	signalDecline      = "decline"
)

// Discussion message metadata keys: the GPU an agent runs on and the model it uses
// or waits for.
const (
	metaGPULabel = "gpuLabel"
	metaModel    = "model"
)

// terminalSignals end an agent's part in a thread.
var terminalSignals = map[string]bool{
	signalAgree: true, signalContribution: true, signalConcern: true, signalStandAside: true,
	signalDecline: true, signalBlock: true, signalFailure: true,
}

// AgentActivity is one busy agent in an activity event.
type AgentActivity struct {
	Agent string `json:"agent"`
	// Doing is waking, triaging, evaluating, waiting_for_accelerator, reasoning,
	// waiting_for_agents, or synthesizing.
	Doing string `json:"doing"`
	GPU   string `json:"gpu,omitempty"`
	Model string `json:"model,omitempty"`
	// Since is when the agent started this step (RFC 3339).
	Since string `json:"since,omitempty"`
}

// agentStep is the step an agent is in on a thread.
type agentStep struct {
	doing, gpu, model string
	since             time.Time
}

// threadActivity is what the thread's own messages say about who is working on it.
type threadActivity struct {
	coordinator string
	// advised is set once the coordinator published its advisory: its reasoning step
	// is over.
	advised bool
	// awaitingAgents is set when the coordinator woke agents and none has reported since.
	awaitingAgents bool
	synthesized    bool
	steps          map[string]agentStep
	lastSeen       map[string]time.Time
}

func newThreadActivity() *threadActivity {
	return &threadActivity{steps: map[string]agentStep{}, lastSeen: map[string]time.Time{}}
}

// activityTracker follows every thread the stream sees, so the followed thread's
// state is complete whenever the thread is found.
type activityTracker struct {
	threads map[string]*threadActivity
}

func newActivityTracker() *activityTracker {
	return &activityTracker{threads: map[string]*threadActivity{}}
}

// observe records what one discussion message says, at the time it was published.
func (a *activityTracker) observe(m natsMessage, at time.Time) {
	if m.ThreadID == "" {
		return
	}
	if m.MessageType == msgThreadClose {
		delete(a.threads, m.ThreadID)
		return
	}
	th, ok := a.threads[m.ThreadID]
	if !ok {
		th = newThreadActivity()
		a.threads[m.ThreadID] = th
	}
	th.observe(m, at)
}

// coordinatorUpdates are the messages that move the coordinator's own step.
var coordinatorUpdates = map[string]func(*threadActivity, natsMessage){
	msgThreadStart:   func(th *threadActivity, m natsMessage) { th.coordinator = m.AgentName },
	msgAdvisory:      func(th *threadActivity, _ natsMessage) { th.advised = true },
	msgAdvisoryReady: func(th *threadActivity, _ natsMessage) { th.advised, th.awaitingAgents = true, true },
	msgReviewReady:   func(th *threadActivity, _ natsMessage) { th.advised, th.awaitingAgents = true, true },
	msgSynthesis:     func(th *threadActivity, _ natsMessage) { th.synthesized = true },
}

func (th *threadActivity) observe(m natsMessage, at time.Time) {
	if m.AgentName != "" {
		th.lastSeen[m.AgentName] = at
	}
	if update, ok := coordinatorUpdates[m.MessageType]; ok {
		update(th, m)
		return
	}
	if m.AgentName == "" || m.AgentName == th.coordinator {
		return
	}
	th.awaitingAgents = false
	if terminalSignals[m.MessageType] {
		delete(th.steps, m.AgentName)
		return
	}
	if step, ok := agentStepOf(m, th.steps[m.AgentName], at); ok {
		th.steps[m.AgentName] = step
	}
}

// agentStepOf is the step a progress message puts an agent in, given the step it was
// in. A discussion heartbeat keeps the current step, or means evaluating when the
// stream joined after the agent's evaluating message.
func agentStepOf(m natsMessage, current agentStep, at time.Time) (agentStep, bool) {
	step := agentStep{gpu: metaString(m, metaGPULabel), model: metaString(m, metaModel), since: at}
	switch m.MessageType {
	case phaseWaking, phaseReady:
		step.doing = doingWaking
	case phaseTriaging:
		step.doing = doingTriaging
	case phaseEvaluating:
		step.doing = doingEvaluating
	case phaseWaiting:
		step.doing = doingWaitingForAccelerator
	case msgHeartbeat:
		if current.doing != "" {
			return current, true
		}
		step.doing = doingEvaluating
	default:
		return agentStep{}, false
	}
	if current.doing == step.doing {
		step.since = current.since
	}
	return step, true
}

// livenessSource reports whether an agent's process is alive.
type livenessSource interface {
	alive(ctx context.Context, agent string) bool
}

// busy lists who is working on the thread now: each agent in a step that is alive,
// and the coordinator, when no agent is, with its own step. An agent counts as alive
// when its liveness heartbeat is fresh or it published on the thread within
// threadBeatWindow. Nothing is busy once the synthesis is out or the coordinator is
// gone.
func (a *activityTracker) busy(
	ctx context.Context, threadID string, now time.Time, live livenessSource,
) []AgentActivity {
	th, ok := a.threads[threadID]
	if !ok || th.synthesized {
		return nil
	}
	isAlive := func(agent string) bool {
		seen, ok := th.lastSeen[agent]
		return (ok && now.Sub(seen) <= threadBeatWindow) || live.alive(ctx, agent)
	}
	var out []AgentActivity
	for _, agent := range sortedKeys(th.steps) {
		if isAlive(agent) {
			out = append(out, th.steps[agent].activity(agent))
		}
	}
	if len(out) > 0 || th.coordinator == "" || !isAlive(th.coordinator) {
		return out
	}
	return []AgentActivity{{Agent: th.coordinator, Doing: th.coordinatorStep()}}
}

// coordinatorStep is what the coordinator is doing while no agent is busy: reasoning
// before its advisory, waiting for the agents it woke or that have not finished, and
// otherwise synthesizing the answer.
func (th *threadActivity) coordinatorStep() string {
	switch {
	case !th.advised:
		return doingReasoning
	case th.awaitingAgents || len(th.steps) > 0:
		return doingWaitingForAgents
	default:
		return doingSynthesizing
	}
}

func (s agentStep) activity(agent string) AgentActivity {
	return AgentActivity{
		Agent: agent, Doing: s.doing, GPU: s.gpu, Model: s.model,
		Since: s.since.UTC().Format(time.RFC3339),
	}
}

func sortedKeys(m map[string]agentStep) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// activityEvent is the SSE event for the busy agents, or false when no one is busy.
func activityEvent(agents []AgentActivity, now time.Time) (SSEEvent, bool) {
	if len(agents) == 0 {
		return SSEEvent{}, false
	}
	return SSEEvent{Type: eventActivity, Agents: agents, At: now.UTC().Format(time.RFC3339)}, true
}

// kvLiveness reads agents' liveness heartbeats from the kubemoot_agent_state bucket.
type kvLiveness struct {
	kv        jetstream.KeyValue
	namespace string
	now       func() time.Time
}

// agentHeartbeat is the part of an agent's liveness heartbeat the gateway reads.
type agentHeartbeat struct {
	Timestamp       time.Time `json:"timestamp"`
	IntervalSeconds int       `json:"intervalSeconds"`
}

// alive reports whether the agent's heartbeat is fresh: no older than two of its own
// intervals, the rule the dashboard uses. A missing or unreadable heartbeat is not.
func (k kvLiveness) alive(ctx context.Context, agent string) bool {
	ctx, cancel := context.WithTimeout(ctx, livenessReadTimeout)
	defer cancel()
	entry, err := k.kv.Get(ctx, k.namespace+"."+agent)
	if err != nil {
		return false
	}
	var hb agentHeartbeat
	if json.Unmarshal(entry.Value(), &hb) != nil || hb.Timestamp.IsZero() {
		return false
	}
	return k.now().Sub(hb.Timestamp) <= staleAfter(hb.IntervalSeconds)
}

// staleAfter is two heartbeat intervals, with the fallback for a missing or
// implausible interval.
func staleAfter(intervalSeconds int) time.Duration {
	if intervalSeconds <= 0 || intervalSeconds > maxBeatInterval {
		intervalSeconds = fallbackBeatInterval
	}
	return 2 * time.Duration(intervalSeconds) * time.Second
}

// unknownLiveness is used when the heartbeat bucket cannot be read: every agent in a
// step counts as alive, and the coordinator's own deadlines bound how long that lasts.
type unknownLiveness struct{}

func (unknownLiveness) alive(context.Context, string) bool { return true }

// livenessFor opens the heartbeat bucket of namespace, or falls back to
// unknownLiveness when it is not there.
func livenessFor(ctx context.Context, js jetstream.JetStream, namespace string) livenessSource {
	kv, err := js.KeyValue(ctx, agentStateBucket)
	if err != nil {
		streamLog.Info("Agent heartbeats unreadable; activity trusts the thread's messages",
			"bucket", agentStateBucket, "error", err.Error())
		return unknownLiveness{}
	}
	return kvLiveness{kv: kv, namespace: namespace, now: time.Now}
}

// activityReporter sends the followed thread's activity at a steady interval.
type activityReporter struct {
	tracker  *activityTracker
	live     livenessSource
	now      func() time.Time
	interval time.Duration
}

func newActivityReporter(live livenessSource) *activityReporter {
	return &activityReporter{tracker: newActivityTracker(), live: live, now: time.Now, interval: activityInterval}
}

// observe records m, published at the given time (zero means now).
func (r *activityReporter) observe(m discussMsg) {
	at := m.published
	if at.IsZero() {
		at = r.now()
	}
	r.tracker.observe(m.data, at)
}

// report emits an activity event for threadID when anyone is busy on it.
func (r *activityReporter) report(ctx context.Context, threadID string, emit func(SSEEvent)) {
	if threadID == "" {
		return
	}
	now := r.now()
	if e, ok := activityEvent(r.tracker.busy(ctx, threadID, now, r.live), now); ok {
		emit(e)
	}
}
