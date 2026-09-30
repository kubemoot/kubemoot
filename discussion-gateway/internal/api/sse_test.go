/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
	"github.com/nats-io/nats.go/jetstream"
)

// The coordinator dual-publishes synthesis to the broadcast subject AND the
// channel subject; both copies share one messageId. The wildcard consumer
// matches both, so the deduper must let the first through and drop the second.
func TestMessageDeduper_DropsDuplicateMessageID(t *testing.T) {
	d := newMessageDeduper()

	if !d.firstSight("syn-1") {
		t.Fatal("first sight of syn-1 should be processed")
	}
	if d.firstSight("syn-1") {
		t.Fatal("second sight of syn-1 (dual-publish) should be dropped")
	}
}

// Distinct messages must all pass through: dedup is per messageId, not global.
func TestMessageDeduper_DistinctIDsPass(t *testing.T) {
	d := newMessageDeduper()

	for _, id := range []string{"a", "b", "c"} {
		if !d.firstSight(id) {
			t.Fatalf("distinct id %q should be processed", id)
		}
	}
}

// Empty messageId is never treated as a duplicate: some message types omit it,
// and collapsing them all to one "" key would silently swallow real events.
func TestMessageDeduper_EmptyIDNeverDeduped(t *testing.T) {
	d := newMessageDeduper()

	for i := 0; i < 3; i++ {
		if !d.firstSight("") {
			t.Fatal("empty messageId must always be processed")
		}
	}
}

// All per-agent consensus verdicts must be persisted to the SSE/transcript
// stream: agree/concern/block/failure as findings carrying their Signal, and
// stand_aside as a done-phase that still carries its signal. block and failure
// were previously dropped (no case). See [[Persist Consensus Signals in Transcripts]].
func TestTranslateAndEmit_PersistsAllConsensusSignals(t *testing.T) {
	cases := []struct {
		msgType    string
		wantType   string
		wantSignal string
	}{
		{"agree", "finding", "agree"},
		{"concern", "finding", "concern"},
		{"block", "finding", "block"},
		{"failure", "finding", "failure"},
		{"stand_aside", "phase", "stand_aside"},
	}
	for _, c := range cases {
		var got []SSEEvent
		translateAndEmit(natsMessage{MessageType: c.msgType, AgentName: "k8s", Content: "x"}, func(e SSEEvent) { got = append(got, e) })
		if len(got) != 1 {
			t.Fatalf("%s: emitted %d events, want 1", c.msgType, len(got))
		}
		if got[0].Type != c.wantType || got[0].Signal != c.wantSignal {
			t.Errorf("%s: got Type=%q Signal=%q, want Type=%q Signal=%q",
				c.msgType, got[0].Type, got[0].Signal, c.wantType, c.wantSignal)
		}
		if got[0].Agent != "k8s" {
			t.Errorf("%s: agent = %q, want k8s", c.msgType, got[0].Agent)
		}
	}
}

// threadStart is a thread_start for conv-1 stored at published with sequence seq.
func threadStart(thread string, published time.Time, seq uint64) discussMsg {
	return discussMsg{
		data:      natsMessage{MessageType: "thread_start", ThreadID: thread, Metadata: map[string]interface{}{"conversationId": "conv-1"}},
		published: published,
		seq:       seq,
	}
}

// TestThreadFinderSkipsAnEarlierTurnsThread: within the stream's look-back window the
// previous turn's thread_start (same conversation) comes first; the finder waits for the
// thread that started after this turn's request was queued.
func TestThreadFinderSkipsAnEarlierTurnsThread(t *testing.T) {
	queued := time.Date(2026, 9, 27, 6, 25, 40, 0, time.UTC)
	tf := &threadFinder{conversationID: "conv-1", notBefore: queued.Add(-clockSkew)}
	var events []SSEEvent
	emit := func(e SSEEvent) { events = append(events, e) }

	tf.process(threadStart("turn-1", queued.Add(-29*time.Second), 1), emit)
	if tf.threadID != "" {
		t.Fatal("the previous turn's thread must not be taken")
	}
	old := discussMsg{data: natsMessage{MessageType: "synthesis", ThreadID: "turn-1", Content: "old answer"}, published: queued.Add(-time.Second), seq: 2}
	if tf.process(old, emit) {
		t.Fatal("messages of the previous turn must not be translated")
	}
	tf.process(threadStart("turn-2", queued.Add(3*time.Second), 3), emit)
	if tf.threadID != "turn-2" {
		t.Fatalf("threadID = %q, want turn-2", tf.threadID)
	}
	if len(events) != 1 || events[0].Type != "thread_found" || events[0].ID != "turn-2:3" {
		t.Fatalf("want only thread_found with id turn-2:3, got %+v", events)
	}
}

// TestThreadFinderWithoutARequestTimeTakesTheFirstMatch keeps the behaviour for a
// stream whose request time is unknown.
func TestThreadFinderWithoutARequestTimeTakesTheFirstMatch(t *testing.T) {
	tf := &threadFinder{conversationID: "conv-1"}
	tf.process(threadStart("t", time.Now().Add(-time.Minute), 1), func(SSEEvent) {})
	if tf.threadID != "t" {
		t.Fatal("with no request time the first matching thread is taken")
	}
}

// TestThreadFinderFollowsARestartedThread is the rollout defect: the coordinator that
// started thread A was replaced, its successor took the request again and answered
// under thread B. The stream must move to B; A never closes.
func TestThreadFinderFollowsARestartedThread(t *testing.T) {
	queued := time.Date(2026, 9, 30, 19, 36, 36, 0, time.UTC)
	tf := &threadFinder{conversationID: "conv-1", notBefore: queued.Add(-clockSkew)}
	var events []SSEEvent
	emit := func(e SSEEvent) { events = append(events, e) }

	tf.process(threadStart("A", queued, 10), emit)
	if !tf.process(discussMsg{data: natsMessage{MessageType: "triaging", ThreadID: "A"}, seq: 11}, emit) {
		t.Fatal("thread A's own messages are followed until it is superseded")
	}
	tf.process(threadStart("B", queued.Add(16*time.Second), 20), emit)
	if tf.threadID != "B" {
		t.Fatalf("threadID = %q, want B", tf.threadID)
	}
	if tf.process(discussMsg{data: natsMessage{MessageType: "agree", ThreadID: "A"}, seq: 21}, emit) {
		t.Fatal("the abandoned thread's messages must be dropped")
	}
	if !tf.process(discussMsg{data: natsMessage{MessageType: "synthesis", ThreadID: "B"}, seq: 22}, emit) {
		t.Fatal("the new thread's messages must be followed")
	}
	if len(events) != 2 || events[1].ThreadID != "B" || events[1].ID != "B:20" {
		t.Fatalf("want thread_found A then B, got %+v", events)
	}
}

// A second copy of the followed thread's thread_start (a dual publish with its own id)
// is not a restart.
func TestThreadFinderIgnoresARepeatOfTheFollowedThreadStart(t *testing.T) {
	tf := &threadFinder{conversationID: "conv-1"}
	var events []SSEEvent
	emit := func(e SSEEvent) { events = append(events, e) }
	tf.process(threadStart("A", time.Time{}, 1), emit)
	tf.process(threadStart("A", time.Time{}, 2), emit)
	if len(events) != 1 {
		t.Fatalf("want one thread_found, got %+v", events)
	}
}

// Messages of the thread seen before its thread_start are replayed when it is found;
// only the last event of that set carries the thread_start's id, so a client that drops
// partway through resumes before it. Other threads' are not replayed, and the buffer is
// bounded.
func TestThreadFinderReplaysBufferedMessagesOfItsThread(t *testing.T) {
	tf := &threadFinder{conversationID: "conv-1"}
	var events []SSEEvent
	emit := func(e SSEEvent) { events = append(events, e) }
	for i := 0; i < maxBufferedMessages+5; i++ {
		tf.process(discussMsg{data: natsMessage{MessageType: "agree", ThreadID: "other"}, seq: uint64(i + 1)}, emit)
	}
	if len(tf.buf) != maxBufferedMessages {
		t.Fatalf("buffer = %d, want %d", len(tf.buf), maxBufferedMessages)
	}
	tf.process(discussMsg{data: natsMessage{MessageType: "concern", ThreadID: "A", Content: "early"}, seq: 900}, emit)
	tf.process(threadStart("A", time.Time{}, 901), emit)
	if len(events) != 2 || events[0].ID != "" || events[1].Summary != "early" || events[1].ID != "A:901" {
		t.Fatalf("want thread_found then the buffered concern, got %+v", events)
	}
	if tf.buf != nil {
		t.Fatal("the buffer is emptied once the thread is found")
	}
}

// A resumed stream already knows its thread and follows it from the first message.
func TestNewThreadFinderResumesTheClientsThread(t *testing.T) {
	tf := newThreadFinder(streamRequest{conversationID: "conv-1", resume: &resumePoint{threadID: "B", afterSeq: 7}})
	if !tf.process(discussMsg{data: natsMessage{MessageType: "synthesis", ThreadID: "B"}, seq: 8}, func(SSEEvent) {}) {
		t.Fatal("a resumed stream follows the client's thread")
	}
}

func TestEventIDRoundTripsThroughParseResumePoint(t *testing.T) {
	id := eventID("da5be99e-e0a6-4c6f-a2a5-db504be93bca", 4242)
	p, err := parseResumePoint(id)
	if err != nil || p.threadID != "da5be99e-e0a6-4c6f-a2a5-db504be93bca" || p.afterSeq != 4242 {
		t.Fatalf("round trip of %q: %+v, %v", id, p, err)
	}
	if eventID("", 3) != "" || eventID("t", 0) != "" {
		t.Fatal("an event without a thread or sequence has no id")
	}
}

func TestParseResumePointRejectsMalformedIDs(t *testing.T) {
	if p, err := parseResumePoint(""); p != nil || err != nil {
		t.Fatalf("empty id: want no resume point, got %+v, %v", p, err)
	}
	for _, bad := range []string{"no-sequence", ":12", "t:", "t:x", "t:0", "t:-1"} {
		if _, err := parseResumePoint(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestConsumerConfigStartPoints(t *testing.T) {
	scope, _ := crewscope.New("ns", "crew")
	now := time.Date(2026, 9, 30, 20, 0, 0, 0, time.UTC)

	resumed := consumerConfig(scope, streamRequest{resume: &resumePoint{threadID: "t", afterSeq: 41}}, now)
	if resumed.DeliverPolicy != jetstream.DeliverByStartSequencePolicy || resumed.OptStartSeq != 42 {
		t.Errorf("resume: got %+v", resumed)
	}
	unknown := consumerConfig(scope, streamRequest{}, now)
	if unknown.DeliverPolicy != jetstream.DeliverByStartTimePolicy || !unknown.OptStartTime.Equal(now.Add(-startTimeOffset)) {
		t.Errorf("unknown request time: got %v", unknown.OptStartTime)
	}
	queued := now.Add(-3 * time.Minute)
	known := consumerConfig(scope, streamRequest{notBefore: queued}, now)
	if !known.OptStartTime.Equal(queued) {
		t.Errorf("known request time: start = %v, want %v", known.OptStartTime, queued)
	}
	ancient := consumerConfig(scope, streamRequest{notBefore: now.Add(-48 * time.Hour)}, now)
	if !ancient.OptStartTime.Equal(now.Add(-maxLookBack)) {
		t.Errorf("an old request time is bounded: start = %v", ancient.OptStartTime)
	}
	if known.FilterSubject != scope.DiscussFilter() || known.AckPolicy != jetstream.AckNonePolicy {
		t.Errorf("filter and ack policy: got %+v", known)
	}
}

// Waiting for GPU capacity is a phase of its own, naming the model the agent waits
// for, and a stand-aside carries the agent's reason, so a client can tell "the GPUs
// were busy" from "the agent had nothing to add".
func TestTranslateAndEmit_WaitingAndStandAsideReasons(t *testing.T) {
	cases := []struct {
		name string
		in   natsMessage
		want SSEEvent
	}{
		{"waiting", natsMessage{MessageType: "waiting", AgentName: "rules", Metadata: map[string]interface{}{"model": "qwen3:14b", "reason": "gpu-busy"}},
			SSEEvent{Type: "phase", Agent: "rules", Status: "waiting", Model: "qwen3:14b", Reason: "gpu-busy"}},
		{"stand_aside with reason", natsMessage{MessageType: "stand_aside", AgentName: "rules", Metadata: map[string]interface{}{"reason": "gpu-busy"}},
			SSEEvent{Type: "phase", Agent: "rules", Status: "done", StoodAside: true, Signal: "stand_aside", Reason: "gpu-busy"}},
		{"stand_aside without reason", natsMessage{MessageType: "stand_aside", AgentName: "rules"},
			SSEEvent{Type: "phase", Agent: "rules", Status: "done", StoodAside: true, Signal: "stand_aside"}},
		{"evaluating", natsMessage{MessageType: "evaluating", AgentName: "rules", Metadata: map[string]interface{}{"gpuLabel": "ollama-rig1"}},
			SSEEvent{Type: "phase", Agent: "rules", Status: "evaluating", GPU: "ollama-rig1"}},
		{"ready", natsMessage{MessageType: "ready", AgentName: "rules"},
			SSEEvent{Type: "phase", Agent: "rules", Status: "ready"}},
	}
	for _, c := range cases {
		var got []SSEEvent
		translateAndEmit(c.in, func(e SSEEvent) { got = append(got, e) })
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestTranslateAndEmit_IgnoresNoiseAndUnknown(t *testing.T) {
	for _, mt := range []string{"heartbeat", "advisory_ready", "something-new"} {
		translateAndEmit(natsMessage{MessageType: mt}, func(e SSEEvent) { t.Errorf("%s emitted %+v", mt, e) })
	}
	var got []SSEEvent
	translateAndEmit(natsMessage{MessageType: "concern", Content: "careful"}, func(e SSEEvent) { got = append(got, e) })
	if len(got) != 1 || got[0].Summary != "careful" || got[0].Content != "" {
		t.Errorf("concern: got %+v", got)
	}
}
