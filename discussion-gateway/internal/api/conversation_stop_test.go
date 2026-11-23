package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	convQueued    = "conv-queued"
	threadRunning = "thread-running"
	stopSubject   = "kubemoot.discuss.ns.pilot.>"
	requestSubj   = "kubemoot.request.ns.pilot"
)

// stopSafetyNet is the wait a test gives a coordinator that does not answer.
const stopSafetyNet = 300 * time.Millisecond

// withStopWait shortens the safety net of a stop for the test.
func withStopWait(t *testing.T, d time.Duration) {
	t.Helper()
	was := stopConfirmWait
	stopConfirmWait = d
	t.Cleanup(func() { stopConfirmWait = was })
}

// coordinatorThatStops answers each stop_requested from the gateway the way the coordinator does:
// the synthesis with the stopped text and outcome, then the thread_close.
func coordinatorThatStops(t *testing.T, js jetstream.JetStream) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	consumer, err := js.CreateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		AckPolicy: jetstream.AckNonePolicy, DeliverPolicy: jetstream.DeliverNewPolicy, FilterSubject: stopSubject})
	if err != nil {
		t.Fatal(err)
	}
	cc, err := consumer.Consume(func(msg jetstream.Msg) {
		m, ok := toDiscussMsg(msg)
		if !ok || m.data.MessageType != msgStopRequested {
			return
		}
		synthesis := msgOf(m.data.ThreadID, m.data.ThreadID+"-stopped", msgSynthesis, "Stopped by the person who asked")
		synthesis["metadata"] = map[string]string{metaOutcome: outcomeStopped}
		publishDiscuss(t, js, synthesis)
		publishDiscuss(t, js, msgOf(m.data.ThreadID, m.data.ThreadID+"-closed", msgThreadClose, ""))
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cc.Stop)
}

// seedAnswered seeds the conversation as seed does, and empties the request queue: the question
// the seed asked was taken by a coordinator and, with its thread open or closed, is no longer queued.
func (c controlSetup) seedAnswered(t *testing.T, open bool) {
	t.Helper()
	c.seed(t, tokenAlice, convCtl, threadCtl, objectName, factKey, open)
	stream, err := c.js.Stream(context.Background(), requestStream)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Purge(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// stopAs sends a stop and decodes the answer.
func (c controlSetup) stopAs(t *testing.T, token, conv string) (int, bus.ConversationControl) {
	t.Helper()
	resp := do(t, http.MethodPost, c.conversationPath(conv, "/stop"), token, "")
	var got bus.ConversationControl
	_ = json.NewDecoder(resp.Body).Decode(&got)
	return resp.StatusCode, got
}

// stateOf reads as the owner the state of a conversation.
func (c controlSetup) stateOf(t *testing.T, conv string) (int, bus.ConversationState) {
	t.Helper()
	resp := do(t, http.MethodGet, c.conversationPath(conv, "/state"), tokenAlice, "")
	var got bus.ConversationState
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, got
}

// queuedOf counts the questions the request stream holds for the crew.
func (c controlSetup) queuedOf(t *testing.T) int {
	t.Helper()
	stream, err := c.js.Stream(context.Background(), requestStream)
	if err != nil {
		t.Fatal(err)
	}
	info, err := stream.Info(context.Background(), jetstream.WithSubjectFilter(requestSubj))
	if err != nil {
		t.Fatal(err)
	}
	return int(info.State.Subjects[requestSubj])
}

// coordinatorTakes makes the crew's coordinator pull the next question without acknowledging it,
// as it does while it works on it.
func (c controlSetup) coordinatorTakes(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	consumer, err := c.js.CreateOrUpdateConsumer(ctx, requestStream, jetstream.ConsumerConfig{
		Durable: "request-ns-pilot", FilterSubject: requestSubj, AckPolicy: jetstream.AckExplicitPolicy, MaxAckPending: 1})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := consumer.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for range batch.Messages() {
	}
}

func TestStopDuringARunningTurnWaitsForTheCoordinatorAndSaysWhatHappened(t *testing.T) {
	c := newControlSetup(t)
	c.seedAnswered(t, true)
	coordinatorThatStops(t, c.js)

	code, got := c.stopAs(t, tokenAlice, convCtl)

	if code != http.StatusOK || got.Outcome != bus.ConversationControlOutcomeStopped {
		t.Fatalf("%d %+v, want 200 stopped", code, got)
	}
	if strings.Join(got.Stopped, ",") != threadCtl || strings.Join(got.Threads, ",") != threadCtl {
		t.Fatalf("threads %v, stopped %v", got.Threads, got.Stopped)
	}
	if !strings.Contains(got.Message, "stopped") || got.Cancelled != 0 {
		t.Fatalf("message %q cancelled %d", got.Message, got.Cancelled)
	}
	_, state := c.stateOf(t, convCtl)
	if state.State != bus.TurnStateStopped || state.Turns[0].State != bus.TurnStateStopped {
		t.Fatalf("after the stop the turn is %+v, want stopped", state)
	}
}

func TestStopSaysItIsUnconfirmedWhenTheCoordinatorDoesNotAnswer(t *testing.T) {
	withStopWait(t, stopSafetyNet)
	c := newControlSetup(t)
	c.seedAnswered(t, true)

	code, got := c.stopAs(t, tokenAlice, convCtl)

	if code != http.StatusAccepted || got.Outcome != bus.ConversationControlOutcomeUnconfirmed {
		t.Fatalf("%d %+v, want 202 unconfirmed", code, got)
	}
	if len(got.Stopped) != 0 || !strings.Contains(got.Message, "may still be running") {
		t.Fatalf("an unconfirmed stop claims %+v", got)
	}
	_, state := c.stateOf(t, convCtl)
	if turn := state.Turns[0]; state.State != bus.TurnStateRunning || !turn.StopRequested {
		t.Fatalf("a turn that did not confirm is still running with the stop requested: %+v", state)
	}
}

func TestStopOnAQueuedQuestionCancelsItFromTheQueue(t *testing.T) {
	c := newControlSetup(t)
	c.ask(t, tokenAlice, convQueued)
	if code, state := c.stateOf(t, convQueued); code != http.StatusOK || state.State != bus.TurnStateQueued {
		t.Fatalf("before the stop: %d %+v", code, state)
	}

	code, got := c.stopAs(t, tokenAlice, convQueued)

	if code != http.StatusOK || got.Outcome != bus.ConversationControlOutcomeCancelled || got.Cancelled != 1 {
		t.Fatalf("%d %+v, want 200 cancelled 1", code, got)
	}
	if len(got.Threads) != 0 || !strings.Contains(got.Message, "never saw it") {
		t.Fatalf("a cancelled question has no thread: %+v", got)
	}
	c.assertNothingQueuedOrReadable(t, convQueued)
}

// assertNothingQueuedOrReadable checks a cancelled question is out of the queue and leaves no state.
func (c controlSetup) assertNothingQueuedOrReadable(t *testing.T, conv string) {
	t.Helper()
	if left := c.queuedOf(t); left != 0 {
		t.Fatalf("the question is still queued: %d", left)
	}
	if code, _ := c.stateOf(t, conv); code != http.StatusNotFound {
		t.Fatalf("a cancelled question left nothing to read: %d, want 404", code)
	}
}

func TestStopOnAQueuedQuestionLeavesAnotherConversationsQuestionQueued(t *testing.T) {
	c := newControlSetup(t)
	c.ask(t, tokenAlice, convQueued)
	c.ask(t, tokenBob, "conv-bob-queued")

	if code, got := c.stopAs(t, tokenAlice, convQueued); code != http.StatusOK || got.Cancelled != 1 {
		t.Fatalf("%d %+v", code, got)
	}
	if left := c.queuedOf(t); left != 1 {
		t.Fatalf("another conversation's question was removed: %d left, want 1", left)
	}
}

func TestStopOfARunningTurnAlsoRemovesTheFollowUpQueuedBehindIt(t *testing.T) {
	c := newControlSetup(t)
	c.seedAnswered(t, true)
	coordinatorThatStops(t, c.js)
	c.ask(t, tokenAlice, convCtl)

	code, got := c.stopAs(t, tokenAlice, convCtl)

	if code != http.StatusOK || got.Outcome != bus.ConversationControlOutcomeStopped || got.Cancelled != 1 {
		t.Fatalf("%d %+v, want stopped with 1 cancelled", code, got)
	}
	if !strings.Contains(got.Message, "queued question(s) removed") || c.queuedOf(t) != 0 {
		t.Fatalf("message %q, queued %d", got.Message, c.queuedOf(t))
	}
}

func TestStopOnAQuestionTheCrewJustTookSaysThereIsNothingToStopYet(t *testing.T) {
	c := newControlSetup(t)
	c.ask(t, tokenAlice, convQueued)
	c.coordinatorTakes(t)

	code, _ := c.stopAs(t, tokenAlice, convQueued)

	if code != http.StatusConflict {
		t.Fatalf("%d, want 409: a question taken whose discussion has not begun cannot be stopped yet", code)
	}
	if c.queuedOf(t) != 1 {
		t.Fatal("a question the crew took was removed from under it")
	}
	if _, state := c.stateOf(t, convQueued); state.State != bus.TurnStateRunning {
		t.Fatalf("a taken question is running: %+v", state)
	}
}

func TestStopAfterTheAnswerChangesNothingAndSaysSo(t *testing.T) {
	c := newControlSetup(t)
	c.seedAnswered(t, false)
	before := c.lastOnTheThread(t)

	code, got := c.stopAs(t, tokenAlice, convCtl)

	if code != http.StatusOK || got.Outcome != bus.ConversationControlOutcomeNothingToStop {
		t.Fatalf("%d %+v, want 200 nothing_to_stop", code, got)
	}
	if len(got.Threads) != 0 || len(got.Stopped) != 0 || !strings.Contains(got.Message, "already finished") {
		t.Fatalf("a no-op stop claims %+v", got)
	}
	if after := c.lastOnTheThread(t); after.MessageID != before.MessageID {
		t.Fatalf("a stop after the answer published %+v", after)
	}
}

func TestStopOfAnUnknownConversationIsNotFound(t *testing.T) {
	c := newControlSetup(t)
	c.ask(t, tokenAlice, "conv-nothing-yet")
	// asked and cancelled before: no thread, nothing queued
	c.stopAs(t, tokenAlice, "conv-nothing-yet")
	if code, _ := c.stopAs(t, tokenAlice, "conv-nothing-yet"); code != http.StatusNotFound {
		t.Fatalf("%d, want 404", code)
	}
}

// publishPause publishes a pause or resume the way the gateway or a person sends it.
func (c controlSetup) publishControl(t *testing.T, thread, id, messageType string) {
	t.Helper()
	publishDiscuss(t, c.js, msgOf(thread, id, messageType, ""))
}

func TestTheStateOfAConversationSaysRunningPausedAndFinished(t *testing.T) {
	c := newControlSetup(t)
	c.seedAnswered(t, true)

	if _, state := c.stateOf(t, convCtl); state.State != bus.TurnStateRunning ||
		state.Turns[0].ThreadID != threadCtl || state.Turns[0].Question != "q of "+convCtl {
		t.Fatalf("an open thread is running with its question: %+v", state)
	}
	c.publishControl(t, threadCtl, "p1", msgThreadPause)
	if _, state := c.stateOf(t, convCtl); state.State != bus.TurnStatePaused {
		t.Fatalf("after a pause: %+v", state)
	}
	c.publishControl(t, threadCtl, "r1", msgThreadResume)
	if _, state := c.stateOf(t, convCtl); state.State != bus.TurnStateRunning {
		t.Fatalf("after a resume: %+v", state)
	}
	publishDiscuss(t, c.js, msgOf(threadCtl, "s1", msgSynthesis, "the answer"))
	publishDiscuss(t, c.js, msgOf(threadCtl, "c1", msgThreadClose, ""))
	_, state := c.stateOf(t, convCtl)
	if state.State != bus.TurnStateFinished || state.Turns[0].EndedAt == "" || state.Turns[0].StartedAt == "" {
		t.Fatalf("a closed thread is finished with its times: %+v", state)
	}
}

func TestAStopAfterTheCloseDoesNotMakeAFinishedTurnStopped(t *testing.T) {
	c := newControlSetup(t)
	c.seedAnswered(t, false)
	c.publishControl(t, threadCtl, "late-stop", msgStopRequested)

	_, state := c.stateOf(t, convCtl)

	if state.State != bus.TurnStateFinished || state.Turns[0].StopRequested {
		t.Fatalf("a late stop changed a finished turn: %+v", state)
	}
}

func TestTheStateListsQueuedTurnsBehindARunningOneWithTheirPlace(t *testing.T) {
	c := newControlSetup(t)
	c.seedAnswered(t, true)
	c.ask(t, tokenBob, "conv-bob-first")
	c.ask(t, tokenAlice, convCtl)

	_, state := c.stateOf(t, convCtl)

	if state.State != bus.TurnStateRunning || len(state.Turns) != 2 {
		t.Fatalf("%+v", state)
	}
	queued := state.Turns[1]
	if queued.State != bus.TurnStateQueued || queued.ThreadID != "" || queued.QueuePosition != 2 {
		t.Fatalf("the follow-up waits behind one question, so it is second: %+v", queued)
	}
}

func TestTheStateOfAQueuedOnlyConversationIsQueuedAndOfAnUnknownOneIsNotFound(t *testing.T) {
	c := newControlSetup(t)
	c.ask(t, tokenAlice, convQueued)
	if _, state := c.stateOf(t, convQueued); state.State != bus.TurnStateQueued ||
		state.Turns[0].QueuePosition != 1 || state.Turns[0].Question != "hi" {
		t.Fatalf("%+v", state)
	}
	if code, _ := c.stateOf(t, "conv-unknown"); code != http.StatusNotFound {
		t.Fatalf("%d, want 404", code)
	}
	if code, _ := c.stateOf(t, "bad.id"); code != http.StatusBadRequest {
		t.Fatalf("%d, want 400", code)
	}
}

func TestTheStateIsForTheOwnerAndAnOperatorWithAReason(t *testing.T) {
	c := newControlSetup(t)
	c.seedAnswered(t, true)
	url := c.conversationPath(convCtl, "/state")
	for _, tc := range []struct {
		who    string
		token  string
		header []string
		want   int
	}{
		{"a stranger", tokenBob, nil, http.StatusNotFound},
		{"an operator without a reason", tokenAdmin, nil, http.StatusBadRequest},
		{"an operator who may read", tokenReader, []string{reasonHeader, reasonText}, http.StatusOK},
		{"no token", "", nil, http.StatusUnauthorized},
		{"the owner", tokenAlice, nil, http.StatusOK},
	} {
		if got := do(t, http.MethodGet, url, tc.token, "", tc.header...).StatusCode; got != tc.want {
			t.Errorf("state by %s: %d, want %d", tc.who, got, tc.want)
		}
	}
}

func TestStopIsRefusedToEveryoneButTheOwnerAndStillRemovesNothing(t *testing.T) {
	c := newControlSetup(t)
	c.ask(t, tokenAlice, convQueued)
	if code, _ := c.stopAs(t, tokenBob, convQueued); code != http.StatusNotFound {
		t.Fatalf("another caller: %d, want 404", code)
	}
	if c.queuedOf(t) != 1 {
		t.Fatal("a refused stop removed the question")
	}
}

func TestTheReducerReadsAStopThatClosedAndAReplyThatReopened(t *testing.T) {
	r := newThreadReducer(convCtl)
	at := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	feed := func(seq uint64, messageType, agent string, meta map[string]interface{}) {
		r.observe(discussMsg{seq: seq, published: at.Add(time.Duration(seq) * time.Second), data: bus.DiscussionMessage{
			ThreadID: threadCtl, MessageType: bus.DiscussionMessageMessageType(messageType), AgentName: agent, Metadata: meta}})
	}
	start := turnStart(threadCtl, convCtl, "q")
	r.observe(discussMsg{seq: 1, published: at, data: bus.DiscussionMessage{ThreadID: threadCtl,
		MessageType: msgThreadStart, Metadata: start["metadata"].(map[string]interface{})}})
	feed(2, msgStopRequested, agentNameGateway, nil)
	if got := r.threads()[0]; !got.stopRequested || got.state() != bus.TurnStateRunning {
		t.Fatalf("a stop sent is a request, not a state: %+v", got)
	}
	feed(3, msgSynthesis, "coordinator", map[string]interface{}{metaOutcome: outcomeStopped})
	feed(4, msgThreadClose, "coordinator", nil)
	if got := r.threads()[0]; got.state() != bus.TurnStateStopped || got.stopRequested {
		t.Fatalf("a close on the stopped text is a stop: %+v", got)
	}
	feed(5, msgReply, agentHuman, nil)
	if got := r.threads()[0]; got.state() != bus.TurnStateRunning || got.stopped || !got.endedAt.IsZero() {
		t.Fatalf("a person's reply reopens the turn as a fresh round: %+v", got)
	}
	if r.lastSeq != 5 {
		t.Fatalf("last sequence %d", r.lastSeq)
	}
}

func TestConversationStateFollowsTheLatestTurnWhenNothingIsLive(t *testing.T) {
	turns := func(states ...bus.TurnState) []bus.TurnStatus {
		out := make([]bus.TurnStatus, 0, len(states))
		for _, s := range states {
			out = append(out, bus.TurnStatus{State: s})
		}
		return out
	}
	for _, tc := range []struct {
		turns []bus.TurnStatus
		want  bus.TurnState
	}{
		{turns(bus.TurnStateFinished, bus.TurnStateStopped), bus.TurnStateStopped},
		{turns(bus.TurnStateStopped, bus.TurnStateFinished), bus.TurnStateFinished},
		{turns(bus.TurnStatePaused, bus.TurnStateQueued), bus.TurnStatePaused},
		{turns(bus.TurnStateQueued, bus.TurnStateRunning), bus.TurnStateRunning},
		{turns(bus.TurnStateFinished, bus.TurnStateQueued), bus.TurnStateQueued},
		{nil, bus.TurnStateFinished},
	} {
		if got := conversationStateFrom(tc.turns); got != tc.want {
			t.Errorf("%v: %s, want %s", tc.turns, got, tc.want)
		}
	}
}

func TestTheStreamTellsAStoppedAnswerFromARealOneByItsOutcome(t *testing.T) {
	stopped := map[string]interface{}{metaOutcome: outcomeStopped}
	for _, tc := range []struct {
		name    string
		in      bus.DiscussionMessage
		outcome string
	}{
		{"a stopped synthesis", bus.DiscussionMessage{MessageType: msgSynthesis, Content: "Stopped", Metadata: stopped}, outcomeStopped},
		{"a stopped close", bus.DiscussionMessage{MessageType: msgThreadClose, Metadata: stopped}, outcomeStopped},
		{"an answer", bus.DiscussionMessage{MessageType: msgSynthesis, Content: "13 nodes Ready"}, ""},
		{"a normal close", bus.DiscussionMessage{MessageType: msgThreadClose}, ""},
	} {
		var got []SSEEvent
		translateAndEmit(tc.in, func(e SSEEvent) { got = append(got, e) })
		if len(got) != 1 || got[0].Outcome != tc.outcome {
			t.Errorf("%s: %+v, want outcome %q", tc.name, got, tc.outcome)
		}
	}
}
