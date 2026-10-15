package api

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

const (
	actThread = "th-1"
	actCoord  = "coordinator"
	actGPU    = "ollama-rig0"
	actAgent  = "compute"
	actModel  = "qwen3:14b"
)

// fixedLiveness reports the listed agents alive and every other agent not.
type fixedLiveness map[string]bool

func (f fixedLiveness) alive(_ context.Context, agent string) bool { return f[agent] }

var actT0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func actMsg(agent, messageType string, meta map[string]interface{}) natsMessage {
	return natsMessage{ThreadID: actThread, AgentName: agent, MessageType: messageType, Metadata: meta}
}

// trackerAt feeds msgs to a new tracker, one second apart from actT0.
func trackerAt(msgs ...natsMessage) *activityTracker {
	a := newActivityTracker()
	for i, m := range msgs {
		a.observe(m, actT0.Add(time.Duration(i)*time.Second))
	}
	return a
}

func doings(agents []AgentActivity) map[string]string {
	out := map[string]string{}
	for _, a := range agents {
		out[a.Agent] = a.Doing
	}
	return out
}

// Long after the last message (past threadBeatWindow), only liveness decides.
var actLater = actT0.Add(10 * time.Minute)

func TestActivityAgentsInAStepAreBusy(t *testing.T) {
	a := trackerAt(
		actMsg(actCoord, msgThreadStart, nil),
		actMsg(actCoord, msgAdvisoryReady, nil),
		actMsg(actAgent, phaseEvaluating, map[string]interface{}{metaGPULabel: actGPU}),
		actMsg("obs", phaseTriaging, nil),
		actMsg(testRulesAgent, phaseWaiting, map[string]interface{}{metaModel: actModel}),
		actMsg(testAgent, phaseTriaging, nil),
		actMsg(testAgent, signalAgree, nil),
	)
	live := fixedLiveness{actCoord: true, actAgent: true, "obs": true, testRulesAgent: true, testAgent: true}
	got := a.busy(context.Background(), actThread, actLater, live)
	want := map[string]string{actAgent: doingEvaluating, "obs": doingTriaging, testRulesAgent: doingWaitingForAccelerator}
	if !reflect.DeepEqual(doings(got), want) {
		t.Fatalf("busy = %+v, want %v", got, want)
	}
	if got[0].Agent != actAgent || got[0].GPU != actGPU || got[2].Model != actModel {
		t.Errorf("details: %+v", got)
	}
}

func TestActivityHeartbeatKeepsTheStepAndItsStart(t *testing.T) {
	a := trackerAt(
		actMsg(actCoord, msgThreadStart, nil),
		actMsg(actAgent, phaseEvaluating, map[string]interface{}{metaGPULabel: actGPU}),
		actMsg(actAgent, msgHeartbeat, nil),
		actMsg(actAgent, phaseEvaluating, nil),
	)
	got := a.busy(context.Background(), actThread, actLater, fixedLiveness{actAgent: true})
	if len(got) != 1 || got[0].Doing != doingEvaluating || got[0].Since != actT0.Add(time.Second).Format(time.RFC3339) {
		t.Fatalf("busy = %+v", got)
	}
}

// A stream that joins mid-evaluation sees only the agent's discussion heartbeats.
func TestActivityHeartbeatAloneMeansEvaluating(t *testing.T) {
	a := trackerAt(actMsg(actAgent, msgHeartbeat, map[string]interface{}{metaGPULabel: actGPU}))
	got := a.busy(context.Background(), actThread, actT0, fixedLiveness{})
	if len(got) != 1 || got[0].Doing != doingEvaluating || got[0].GPU != actGPU {
		t.Fatalf("busy = %+v", got)
	}
}

func TestActivityCoordinatorSteps(t *testing.T) {
	start := actMsg(actCoord, msgThreadStart, nil)
	cases := []struct {
		name string
		msgs []natsMessage
		want string
	}{
		{"reasoning before the advisory", []natsMessage{start}, doingReasoning},
		{"waiting for woken agents", []natsMessage{start, actMsg(actCoord, msgAdvisoryReady, nil)}, doingWaitingForAgents},
		{"waiting for a review", []natsMessage{start, actMsg(actCoord, msgAdvisory, nil), actMsg(actCoord, msgReviewReady, nil)}, doingWaitingForAgents},
		{"synthesizing once every agent is done", []natsMessage{
			start, actMsg(actCoord, msgAdvisoryReady, nil),
			actMsg(testAgent, phaseTriaging, nil), actMsg(testAgent, signalAgree, nil),
		}, doingSynthesizing},
		{"waiting for an agent that stopped heartbeating", []natsMessage{
			start, actMsg(actCoord, msgAdvisoryReady, nil), actMsg(testAgent, phaseEvaluating, nil),
		}, doingWaitingForAgents},
	}
	for _, c := range cases {
		got := trackerAt(c.msgs...).busy(context.Background(), actThread, actLater, fixedLiveness{actCoord: true})
		if want := []AgentActivity{{Agent: actCoord, Doing: c.want}}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: busy = %+v, want %+v", c.name, got, want)
		}
	}
}

func TestActivityNobodyBusy(t *testing.T) {
	start := actMsg(actCoord, msgThreadStart, nil)
	cases := []struct {
		name   string
		a      *activityTracker
		thread string
		live   fixedLiveness
		now    time.Time
	}{
		{"unknown thread", trackerAt(start), "other", fixedLiveness{actCoord: true}, actLater},
		{"no thread followed yet", trackerAt(start), "", fixedLiveness{actCoord: true}, actLater},
		{"answer out", trackerAt(start, actMsg(actCoord, msgSynthesis, nil)), actThread, fixedLiveness{actCoord: true}, actLater},
		{"thread closed", trackerAt(start, actMsg(actCoord, msgThreadClose, nil)), actThread, fixedLiveness{actCoord: true}, actLater},
		{"coordinator gone", trackerAt(start), actThread, fixedLiveness{}, actLater},
		{"agent gone, coordinator gone", trackerAt(start, actMsg(testAgent, phaseTriaging, nil)), actThread, fixedLiveness{}, actLater},
	}
	for _, c := range cases {
		if got := c.a.busy(context.Background(), c.thread, c.now, c.live); len(got) != 0 {
			t.Errorf("%s: busy = %+v, want nobody", c.name, got)
		}
	}
}

// A recent thread message counts as alive without a liveness heartbeat.
func TestActivityRecentThreadMessageMeansAlive(t *testing.T) {
	a := trackerAt(actMsg(actCoord, msgThreadStart, nil), actMsg(testAgent, phaseTriaging, nil))
	recent := actT0.Add(threadBeatWindow)
	if got := a.busy(context.Background(), actThread, recent, fixedLiveness{}); len(got) != 1 || got[0].Agent != testAgent {
		t.Fatalf("busy = %+v", got)
	}
}

func TestActivityIgnoresMessagesWithoutAThread(t *testing.T) {
	a := newActivityTracker()
	a.observe(natsMessage{AgentName: testAgent, MessageType: phaseWaking}, actT0)
	if len(a.threads) != 0 {
		t.Fatalf("threads = %v", a.threads)
	}
}

func TestActivityEventOnlyWhenBusy(t *testing.T) {
	if _, ok := activityEvent(nil, actT0); ok {
		t.Error("an event with no busy agents")
	}
	agents := []AgentActivity{{Agent: testAgent, Doing: doingTriaging}}
	e, ok := activityEvent(agents, actT0)
	if !ok || e.Type != eventActivity || e.At != testStamp || !reflect.DeepEqual(e.Agents, agents) {
		t.Fatalf("event = %+v", e)
	}
	data, _ := json.Marshal(e)
	want := `{"type":"activity","agents":[{"agent":"k8s","doing":"triaging"}],"at":"` + testStamp + `"}`
	if string(data) != want {
		t.Errorf("json = %s, want %s", data, want)
	}
}

func TestStaleAfterUsesTheHeartbeatsInterval(t *testing.T) {
	for in, want := range map[int]time.Duration{30: time.Minute, 0: 2 * time.Minute, -5: 2 * time.Minute, 7200: 2 * time.Minute} {
		if got := staleAfter(in); got != want {
			t.Errorf("staleAfter(%d) = %s, want %s", in, got, want)
		}
	}
}

func TestKVLivenessReadsTheAgentStateBucket(t *testing.T) {
	_, js := jetStreamServer(t)
	ctx := context.Background()
	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: agentStateBucket})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	put := func(agent string, at time.Time) {
		data, _ := json.Marshal(map[string]interface{}{"agent": agent, "timestamp": at.Format(time.RFC3339Nano), "intervalSeconds": 30})
		if _, err := kv.Put(ctx, "ns."+agent, data); err != nil {
			t.Fatal(err)
		}
	}
	put("fresh", now.Add(-50*time.Second))
	put("stale", now.Add(-61*time.Second))
	if _, err := kv.Put(ctx, "ns.garbled", []byte("not json")); err != nil {
		t.Fatal(err)
	}
	live := livenessFor(ctx, js, "ns")
	for agent, want := range map[string]bool{"fresh": true, "stale": false, "garbled": false, "missing": false} {
		if got := live.alive(ctx, agent); got != want {
			t.Errorf("alive(%s) = %v, want %v", agent, got, want)
		}
	}
}

func TestLivenessWithoutTheBucketTrustsTheThread(t *testing.T) {
	_, js := jetStreamServer(t)
	if live := livenessFor(context.Background(), js, "ns"); !live.alive(context.Background(), "anyone") {
		t.Fatal("without the bucket every agent in a step counts as alive")
	}
}

// fakeMsg is a JetStream message with only its data and metadata.
type fakeMsg struct {
	jetstream.Msg
	data []byte
	seq  uint64
}

func (m fakeMsg) Data() []byte { return m.data }

func (m fakeMsg) Metadata() (*jetstream.MsgMetadata, error) {
	return &jetstream.MsgMetadata{Sequence: jetstream.SequencePair{Stream: m.seq}, Timestamp: time.Now()}, nil
}

func jsonMsg(t *testing.T, seq uint64, m natsMessage) jetstream.Msg {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return fakeMsg{data: data, seq: seq}
}

// While an agent is busy the stream carries activity events at the reporter's
// interval; after the thread closes it ends.
func TestProcessMessagesEmitsActivityWhileAnAgentIsBusy(t *testing.T) {
	ch := make(chan jetstream.Msg, 4)
	start := threadStart(actThread, time.Time{}, 1).data
	start.AgentName, start.MessageID = actCoord, "s"
	evaluating := actMsg(actAgent, phaseEvaluating, map[string]interface{}{metaGPULabel: actGPU})
	evaluating.MessageID = "e"
	ch <- jsonMsg(t, 1, start)
	ch <- jsonMsg(t, 2, evaluating)
	reporter := newActivityReporter(fixedLiveness{})
	reporter.interval = 10 * time.Millisecond
	f := follower{tf: &threadFinder{conversationID: testConv}, dedup: newMessageDeduper(), activity: reporter}

	events := make(chan SSEEvent, 64)
	done := make(chan error, 1)
	go func() { done <- processMessages(context.Background(), ch, f, func(e SSEEvent) { events <- e }) }()

	var activity *SSEEvent
	for activity == nil {
		if e := <-events; e.Type == eventActivity {
			activity = &e
		}
	}
	if len(activity.Agents) != 1 || activity.Agents[0].Agent != actAgent || activity.Agents[0].GPU != actGPU {
		t.Fatalf("activity = %+v", activity)
	}
	closing := actMsg(actCoord, msgThreadClose, nil)
	closing.MessageID = "c"
	ch <- jsonMsg(t, 3, closing)
	if err := <-done; err != nil {
		t.Fatalf("processMessages: %v", err)
	}
}

// Before the thread is found, and when nobody is busy, no activity is sent.
func TestReportSendsNothingWhenNobodyIsBusy(t *testing.T) {
	r := newActivityReporter(fixedLiveness{})
	r.observe(discussMsg{data: actMsg(actCoord, msgThreadStart, nil), published: actT0})
	r.now = func() time.Time { return actLater }
	emit := func(e SSEEvent) { t.Errorf("emitted %+v", e) }
	r.report(context.Background(), "", emit)
	r.report(context.Background(), actThread, emit)
}
