package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
)

const (
	eventsPath = "/api/v1/discussions/pilot/events"
	convBob    = "conv-bob"
	waitEvent  = 10 * time.Second
	agentSched = "scheduler"
	pilotCrew  = "pilot"
)

// eventFeed is an open conversation event stream: its events, in order, with their ids.
type eventFeed struct {
	events chan bus.ConversationEvent
}

// openEvents opens the event stream and returns once it is connected, so everything
// published after is seen.
func openEvents(t *testing.T, base, token, query string, header ...string) *eventFeed {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+eventsPath+query, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events: status %d", resp.StatusCode)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	feed := &eventFeed{events: make(chan bus.ConversationEvent, 64)}
	go feed.read(bufio.NewScanner(resp.Body))
	if first := feed.next(t); first.Type != bus.ConversationEventTypeConnected {
		t.Fatalf("first event %+v, want connected", first)
	}
	return feed
}

func (f *eventFeed) read(sc *bufio.Scanner) {
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var ev bus.ConversationEvent
			if json.Unmarshal([]byte(v), &ev) == nil {
				f.events <- ev
			}
		}
	}
}

// next is the next event, failing when none comes.
func (f *eventFeed) next(t *testing.T) bus.ConversationEvent {
	t.Helper()
	select {
	case ev := <-f.events:
		return ev
	case <-time.After(waitEvent):
		t.Fatal("no event arrived")
		return bus.ConversationEvent{}
	}
}

// updates collects the conversation_updated events up to and including the one for the
// sentinel conversation, which the test publishes last: whatever the stream was going to
// send for another conversation has been sent by then.
func (f *eventFeed) updates(t *testing.T, sentinel string) []bus.ConversationEvent {
	t.Helper()
	var got []bus.ConversationEvent
	for {
		ev := f.next(t)
		if ev.Type != bus.ConversationEventTypeConversationUpdated {
			continue
		}
		got = append(got, ev)
		if ev.ConversationID == sentinel {
			return got
		}
	}
}

func conversationsOf(events []bus.ConversationEvent) string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.ConversationID+":"+string(e.Reason))
	}
	return strings.Join(out, ",")
}

// answerOn publishes a synthesis on a thread.
func answerOn(t *testing.T, o ownedSetup, thread, id string) {
	t.Helper()
	publishDiscuss(t, o.js, msgOf(thread, id, msgSynthesis, "late answer"))
}

func TestACallerHearsOnlyOfTheirOwnConversations(t *testing.T) {
	o := newOwnedSetup(t, false)
	o.ask(t, tokenAlice, convA)
	o.ask(t, tokenBob, convBob)
	publishDiscuss(t, o.js, turnStart("ta", convA, "q"))
	publishDiscuss(t, o.js, turnStart("tb", convBob, "q"))
	alice := openEvents(t, o.srv.URL, tokenAlice, "")
	bob := openEvents(t, o.srv.URL, tokenBob, "")

	answerOn(t, o, "ta", "a-s")
	answerOn(t, o, "tb", "b-s")
	publishDiscuss(t, o.js, msgOf("ta", "a-s2", msgSynthesis, "again"))
	publishDiscuss(t, o.js, msgOf("tb", "b-s2", msgSynthesis, "again"))

	if got := conversationsOf(alice.updates(t, convA)); got != convA+":answer" {
		t.Fatalf("alice heard %q, want only her own", got)
	}
	if got := conversationsOf(bob.updates(t, convBob)); got != convBob+":answer" {
		t.Fatalf("bob heard %q, want only his own", got)
	}
}

// The quiet conversation's thread started before the stream opened and long before the
// news: the gateway finds its conversation from the stream, so the late answer resurfaces.
func TestALateAnswerOnAQuietConversationResurfaces(t *testing.T) {
	o := newOwnedSetup(t, false)
	o.ask(t, tokenAlice, convA)
	publishDiscuss(t, o.js, turnStart("quiet", convA, "q"))
	publishDiscuss(t, o.js, msgOf("quiet", "q-close", msgThreadClose, ""))
	for i := 0; i < 5; i++ {
		publishDiscuss(t, o.js, msgOf("noise", "n"+strconv.Itoa(i), signalAgree, "x"))
	}
	feed := openEvents(t, o.srv.URL, tokenAlice, "")

	publishDiscuss(t, o.js, msgOf("quiet", "late", msgSynthesis, "the answer that came late"))

	ev := feed.next(t)
	if ev.Type != bus.ConversationEventTypeConversationUpdated || ev.ConversationID != convA ||
		ev.Reason != bus.ConversationEventReasonAnswer || ev.ThreadID != "quiet" || ev.At == "" {
		t.Fatalf("event %+v", ev)
	}
	if strings.Contains(ev.At, "late answer") {
		t.Fatal("an event carries no content")
	}
}

func TestAScheduledReminderAndFollowupSurfaceTheirConversation(t *testing.T) {
	o := newOwnedSetup(t, false)
	o.ask(t, tokenAlice, convA)
	publishDiscuss(t, o.js, turnStart("ta", convA, "q"))
	feed := openEvents(t, o.srv.URL, tokenAlice, "")

	reminder := msgOf("ta", "r1", msgReminder, "drink water")
	reminder["agentName"] = agentSched
	reminder["metadata"] = map[string]string{metaScheduleID: "s1", metaKind: kindReminder}
	publishDiscuss(t, o.js, reminder)
	followup := msgOf("ta", "f1", msgReply, "ask again")
	followup["agentName"] = agentHuman
	followup["metadata"] = map[string]string{metaScheduleID: "s2"}
	publishDiscuss(t, o.js, followup)
	// A reply a person typed is not news: the person is already looking.
	typed := msgOf("ta", "p1", msgReply, "hello")
	typed["agentName"] = agentHuman
	publishDiscuss(t, o.js, typed)
	answerOn(t, o, "ta", "end")

	want := convA + ":reminder," + convA + ":followup," + convA + ":answer"
	var got []bus.ConversationEvent
	for len(got) < 3 {
		if ev := feed.next(t); ev.Type == bus.ConversationEventTypeConversationUpdated {
			got = append(got, ev)
		}
	}
	if conversationsOf(got) != want {
		t.Fatalf("heard %q, want %q", conversationsOf(got), want)
	}
}

func TestADualPublishedSynthesisIsOneEvent(t *testing.T) {
	o := newOwnedSetup(t, false)
	o.ask(t, tokenAlice, convA)
	publishDiscuss(t, o.js, turnStart("ta", convA, "q"))
	feed := openEvents(t, o.srv.URL, tokenAlice, "")

	m := msgOf("ta", "same-id", msgSynthesis, "x")
	publishOn(t, o.js, "kubemoot.discuss.ns.pilot.broadcast.ta", m)
	publishOn(t, o.js, "kubemoot.discuss.ns.pilot.web.ta", m)
	publishDiscuss(t, o.js, msgOf("ta", "next", msgSynthesis, "y"))

	if got := conversationsOf(feed.updates(t, convA)); got != convA+":answer" {
		t.Fatalf("the first sentinel is the first event: %q", got)
	}
	if ev := feed.next(t); ev.ConversationID != convA {
		t.Fatalf("event %+v", ev)
	}
	select {
	case ev := <-feed.events:
		t.Fatalf("a third event %+v", ev)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestAnOperatorWithAReasonHearsOfEveryConversationAndOneWithoutIsRefused(t *testing.T) {
	o := newOwnedSetup(t, false)
	o.ask(t, tokenAlice, convA)
	o.ask(t, tokenBob, convBob)
	publishDiscuss(t, o.js, turnStart("ta", convA, "q"))
	publishDiscuss(t, o.js, turnStart("tb", convBob, "q"))

	if got := do(t, http.MethodGet, o.srv.URL+eventsPath+"?all=true", tokenAdmin, "").StatusCode; got != http.StatusBadRequest {
		t.Fatalf("no reason: %d, want 400", got)
	}
	for _, token := range []string{tokenAlice, tokenNoRole} {
		if got := do(t, http.MethodGet, o.srv.URL+eventsPath+"?all=true", token, "", reasonHeader, "why").StatusCode; got != http.StatusNotFound && got != http.StatusForbidden {
			t.Fatalf("%s may not hear everything: %d", token, got)
		}
	}
	admin := openEvents(t, o.srv.URL, tokenAdmin, "?all=true", reasonHeader, "support ticket 41")
	answerOn(t, o, "ta", "a-s")
	answerOn(t, o, "tb", "b-s")
	if got := conversationsOf(admin.updates(t, convBob)); got != convA+":answer,"+convBob+":answer" {
		t.Fatalf("operator heard %q", got)
	}
	// Without all=true an operator is a caller like any other: they own nothing here.
	plain := openEvents(t, o.srv.URL, tokenAdmin, "")
	answerOn(t, o, "ta", "a-s2")
	select {
	case ev := <-plain.events:
		if ev.Type == bus.ConversationEventTypeConversationUpdated {
			t.Fatalf("an operator without all=true heard of %+v", ev)
		}
	case <-time.After(500 * time.Millisecond):
	}
}

func TestAConversationWithNoOwnerIsHeardOfByNoOneButAnOperator(t *testing.T) {
	o := newOwnedSetup(t, false)
	o.ask(t, tokenAlice, convA)
	publishDiscuss(t, o.js, turnStart("orphan", "conv-orphan", "q"))
	publishDiscuss(t, o.js, turnStart("ta", convA, "q"))
	alice := openEvents(t, o.srv.URL, tokenAlice, "")
	publishDiscuss(t, o.js, msgOf("orphan", "o-s", msgSynthesis, "x"))
	answerOn(t, o, "ta", "a-s")
	if got := conversationsOf(alice.updates(t, convA)); got != convA+":answer" {
		t.Fatalf("alice heard %q", got)
	}
}

func TestACrewThatVerifiesNoOneStreamsEveryConversationAsBefore(t *testing.T) {
	nc, js := jetStreamServer(t)
	srv := gatewayServer(t, nc)
	publishDiscuss(t, js, turnStart("t1", "conv-1", "q"))
	feed := openEvents(t, srv.URL, "", "")
	publishDiscuss(t, js, msgOf("t1", "s1", msgSynthesis, "x"))
	ev := feed.next(t)
	if ev.ConversationID != "conv-1" || ev.Reason != bus.ConversationEventReasonAnswer {
		t.Fatalf("event %+v", ev)
	}
}

func TestAnEventStreamResumesAfterTheLastEventIDItHandled(t *testing.T) {
	nc, js := jetStreamServer(t)
	srv := gatewayServer(t, nc)
	publishDiscuss(t, js, turnStart("t1", "conv-1", "q"))
	first := publishDiscuss(t, js, msgOf("t1", "s1", msgSynthesis, "one"))
	second := publishDiscuss(t, js, msgOf("t1", "s2", msgSynthesis, "two"))

	feed := openEvents(t, srv.URL, "", "", "Last-Event-ID", strconv.FormatUint(first, 10))
	ev := feed.next(t)
	if ev.Type != bus.ConversationEventTypeConversationUpdated || ev.ThreadID != "t1" {
		t.Fatalf("event %+v", ev)
	}
	// The id of an event is the stream sequence it came from.
	resp := do(t, http.MethodGet, srv.URL+eventsPath+"?lastEventId=nope", "", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a bad id: %d, want 400", resp.StatusCode)
	}
	again := openEvents(t, srv.URL, "", "?lastEventId="+strconv.FormatUint(second, 10))
	select {
	case ev := <-again.events:
		t.Fatalf("nothing is newer than %d, got %+v", second, ev)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestEventsRefuseACallerWithoutACredentialOrWithoutAccessToTheCrew(t *testing.T) {
	o := newOwnedSetup(t, false)
	if got := do(t, http.MethodGet, o.srv.URL+eventsPath, "", "").StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("no credential: %d, want 401", got)
	}
	if got := do(t, http.MethodGet, o.srv.URL+eventsPath, tokenNoRole, "").StatusCode; got != http.StatusForbidden {
		t.Fatalf("no access to the crew: %d, want 403", got)
	}
}

func TestNewsOfAMessageByItsKind(t *testing.T) {
	scheduled := bus.DiscussionMessageMetadata{metaScheduleID: "s"}
	cases := []struct {
		name string
		msg  bus.DiscussionMessage
		want bus.ConversationEventReason
		news bool
	}{
		{"synthesis", bus.DiscussionMessage{MessageType: msgSynthesis}, bus.ConversationEventReasonAnswer, true},
		{"reminder", bus.DiscussionMessage{MessageType: msgReminder}, bus.ConversationEventReasonReminder, true},
		{"scheduled reply", bus.DiscussionMessage{MessageType: msgReply, AgentName: agentHuman, Metadata: scheduled},
			bus.ConversationEventReasonFollowup, true},
		{"typed reply", bus.DiscussionMessage{MessageType: msgReply, AgentName: agentHuman}, "", false},
		{"scheduled reply from another sender", bus.DiscussionMessage{MessageType: msgReply, AgentName: "k8s", Metadata: scheduled}, "", false},
		{"scheduled new thread", bus.DiscussionMessage{MessageType: msgThreadStart, Metadata: scheduled},
			bus.ConversationEventReasonFollowup, true},
		{"scheduled reminder thread", bus.DiscussionMessage{MessageType: msgThreadStart,
			Metadata: bus.DiscussionMessageMetadata{metaScheduleID: "s", metaKind: kindReminder}},
			bus.ConversationEventReasonReminder, true},
		{"asked thread", bus.DiscussionMessage{MessageType: msgThreadStart}, "", false},
		{"signal", bus.DiscussionMessage{MessageType: signalAgree}, "", false},
		{"close", bus.DiscussionMessage{MessageType: msgThreadClose}, "", false},
	}
	for _, c := range cases {
		got, news := newsOf(c.msg)
		if got != c.want || news != c.news {
			t.Errorf("%s: %q %t, want %q %t", c.name, got, news, c.want, c.news)
		}
	}
}

func TestParseEventID(t *testing.T) {
	for _, c := range []struct {
		id      string
		want    uint64
		wantErr bool
	}{{"", 0, false}, {"42", 42, false}, {"0", 0, true}, {"-1", 0, true}, {"t1:5", 0, true}, {"nope", 0, true}} {
		req, _ := http.NewRequest(http.MethodGet, "http://x/events?lastEventId="+c.id, nil)
		got, err := parseEventID(req)
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("%q: %d, %v", c.id, got, err)
		}
		header, _ := http.NewRequest(http.MethodGet, "http://x/events", nil)
		header.Header.Set("Last-Event-ID", c.id)
		if got, err := parseEventID(header); got != c.want || (err != nil) != c.wantErr {
			t.Errorf("header %q: %d, %v", c.id, got, err)
		}
	}
}

// followOnce runs a follower over the stream after start until it ends or delivers want events.
func followOnce(t *testing.T, js jetstream.JetStream, who audience, start uint64, want int) (*newsFollower, []string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitEvent)
	defer cancel()
	scope := pilotScope()
	cfg := eventsConsumerConfig(scope, start)
	consumer, err := js.CreateOrUpdateConsumer(ctx, streamName, cfg)
	if err != nil {
		t.Fatal(err)
	}
	f := &newsFollower{js: js, scope: scope, who: who, threads: newThreadIndex(),
		seen: newMessageDeduper().bounded(maxSeenMessages), seq: start}
	var ids []string
	err = f.run(ctx, consumer, cfg, func(id string, ev bus.ConversationEvent) {
		if ev.Type == bus.ConversationEventTypeConversationUpdated {
			ids = append(ids, id)
			if len(ids) == want {
				cancel()
			}
		}
	})
	return f, ids, err
}

// A news message whose owner cannot be checked ends the stream without moving past it, so
// the client that reconnects from its last id is sent it again.
func TestANewsMessageWhoseOwnerCannotBeCheckedIsSentAgainOnResume(t *testing.T) {
	_, js := jetStreamServer(t)
	start := publishDiscuss(t, js, turnStart("t1", "conv-1", "q"))
	publishDiscuss(t, js, msgOf("t1", "s1", msgSynthesis, "one"))

	broken := func(context.Context, string) (bool, error) { return false, errors.New("the bucket is gone") }
	f, ids, err := followOnce(t, js, broken, start, 1)
	if err == nil || len(ids) != 0 || f.seq != start {
		t.Fatalf("err %v, events %v, seq %d: want the stream ended at %d with nothing sent", err, ids, f.seq, start)
	}
	_, ids, _ = followOnce(t, js, everyConversation, f.seq, 1)
	if len(ids) != 1 || ids[0] != strconv.FormatUint(start+1, 10) {
		t.Fatalf("events %v, want the message sent again", ids)
	}
}

func TestAThreadWhoseConversationCannotBeLookedUpEndsTheStreamAndIsNotDropped(t *testing.T) {
	_, js := jetStreamServer(t)
	publishDiscuss(t, js, msgOf("old", "o1", signalAgree, "x"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &newsFollower{js: js, scope: pilotScope(), who: everyConversation,
		threads: newThreadIndex(), seen: newMessageDeduper()}
	_, _, err := f.conversationOf(ctx, discussMsg{data: bus.DiscussionMessage{ThreadID: "old", MessageType: msgSynthesis}})
	if err == nil {
		t.Fatal("a lookup that could not be made is an error, not a missing thread")
	}
	if _, cached := f.threads.unknown["old"]; cached {
		t.Fatal("a failed lookup must not be remembered as no conversation")
	}
}

func TestAThreadWithNoStartIsReadForOnceAndAWildcardThreadNeverIs(t *testing.T) {
	_, js := jetStreamServer(t)
	publishDiscuss(t, js, msgOf("old", "o1", signalAgree, "x"))
	f := &newsFollower{js: js, scope: pilotScope(), who: everyConversation,
		threads: newThreadIndex(), seen: newMessageDeduper()}
	ask := func(thread string) bool {
		_, found, err := f.conversationOf(context.Background(), discussMsg{data: bus.DiscussionMessage{ThreadID: thread}})
		if err != nil {
			t.Fatal(err)
		}
		return found
	}
	if ask("old") {
		t.Fatal("a thread with no thread_start has no conversation")
	}
	if _, remembered := f.threads.unknown["old"]; !remembered {
		t.Fatal("the missing start should be remembered")
	}
	for _, bad := range []string{"", "x.y", "a*", "a>", "a b"} {
		if ask(bad) {
			t.Errorf("thread %q", bad)
		}
		if _, remembered := f.threads.unknown[bad]; remembered {
			t.Errorf("thread %q is refused, not looked up", bad)
		}
	}
	f.threads.observe(discussMsg{data: bus.DiscussionMessage{
		ThreadID: "old", MessageType: msgThreadStart, Metadata: bus.DiscussionMessageMetadata{metaConversationID: "conv-9"}}})
	if !ask("old") {
		t.Fatal("a thread_start seen later is found")
	}
}

func TestTheSecondCopyOfTheMessageAResumeStartedAtIsNotSentAgain(t *testing.T) {
	nc, js := jetStreamServer(t)
	srv := gatewayServer(t, nc)
	publishDiscuss(t, js, turnStart("t1", "conv-1", "q"))
	m := msgOf("t1", "same-id", msgSynthesis, "x")
	first := publishDiscuss(t, js, m)
	publishOn(t, js, "kubemoot.discuss.ns.pilot.web.t1", m)
	publishDiscuss(t, js, msgOf("t1", "other", msgSynthesis, "y"))

	feed := openEvents(t, srv.URL, "", "", "Last-Event-ID", strconv.FormatUint(first, 10))
	ev := feed.next(t)
	if ev.Reason != bus.ConversationEventReasonAnswer {
		t.Fatalf("event %+v", ev)
	}
	select {
	case again := <-feed.events:
		t.Fatalf("the twin and the next message are two copies and one message: second event %+v", again)
	case <-time.After(300 * time.Millisecond):
	}
}

func pilotScope() crewscope.Scope { return crewscope.Scope{Namespace: "ns", Crew: pilotCrew} }
