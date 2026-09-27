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

// Distinct messages must all pass through — dedup is per messageId, not global.
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

// TestThreadFinderSkipsAnEarlierTurnsThread: within the stream's look-back window the
// previous turn's thread_start (same conversation) comes first; the finder waits for the
// thread that started after this turn's request was queued.
func TestThreadFinderSkipsAnEarlierTurnsThread(t *testing.T) {
	queued := time.Date(2026, 9, 27, 6, 25, 40, 0, time.UTC)
	tf := &threadFinder{conversationID: "conv-1", notBefore: queued.Add(-clockSkew)}
	var events []SSEEvent
	emit := func(e SSEEvent) { events = append(events, e) }
	start := func(thread string) natsMessage {
		return natsMessage{MessageType: "thread_start", ThreadID: thread, Metadata: map[string]interface{}{"conversationId": "conv-1"}}
	}

	if tf.process(start("turn-1"), queued.Add(-29*time.Second), emit) {
		t.Fatal("the previous turn's thread must not be taken")
	}
	if tf.process(natsMessage{MessageType: "synthesis", ThreadID: "turn-1", Content: "old answer"}, queued.Add(-1*time.Second), emit) {
		t.Fatal("messages of the previous turn must not complete the search")
	}
	if !tf.process(start("turn-2"), queued.Add(3*time.Second), emit) {
		t.Fatal("the thread started after the request must be taken")
	}
	if tf.threadID != "turn-2" {
		t.Fatalf("threadID = %q, want turn-2", tf.threadID)
	}
	for _, e := range events {
		if e.Type == "synthesis" {
			t.Fatalf("the old turn's synthesis was emitted: %+v", e)
		}
	}
}

// TestThreadFinderWithoutARequestTimeTakesTheFirstMatch keeps the behaviour for a
// stream whose request another gateway replica queued.
func TestThreadFinderWithoutARequestTimeTakesTheFirstMatch(t *testing.T) {
	tf := &threadFinder{conversationID: "conv-1"}
	msg := natsMessage{MessageType: "thread_start", ThreadID: "t", Metadata: map[string]interface{}{"conversationId": "conv-1"}}
	if !tf.process(msg, time.Now().Add(-time.Minute), func(SSEEvent) {}) {
		t.Fatal("with no request time the first matching thread is taken")
	}
}
