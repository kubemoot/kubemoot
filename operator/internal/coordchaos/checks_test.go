/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package coordchaos

import (
	"strings"
	"testing"
)

func names(vs []Violation) string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.Check)
	}
	return strings.Join(out, ",")
}

func protected(min, max int) Expect {
	return Expect{Mode: ModeProtected, ResumedMin: min, ResumedMax: max}
}

func TestHealthyThreadHasNoViolations(t *testing.T) {
	if vs := Evaluate(observe(1, healthyThread("a")), protected(0, 0)); len(vs) != 0 {
		t.Fatalf("violations on a healthy thread: %s", Summary(vs))
	}
}

func TestResumedThreadHasNoViolations(t *testing.T) {
	if vs := Evaluate(observe(1, resumedThread("a")), protected(1, 1)); len(vs) != 0 {
		t.Fatalf("violations on a resumed thread: %s", Summary(vs))
	}
}

func TestAnswerOnChannelAndBroadcastIsOneAnswer(t *testing.T) {
	th := healthyThread("a")
	th.copyOn(MsgSynthesis, "kubemoot.discuss.ns.crew.general.a").copyOn(MsgThreadClose, "kubemoot.discuss.ns.crew.general.a")
	if vs := Evaluate(observe(1, th), protected(0, 0)); len(vs) != 0 {
		t.Fatalf("the same answer and close on two subjects is one answer: %s", Summary(vs))
	}
	th.copyOn(MsgSynthesis, "kubemoot.discuss.ns.crew.general.a")
	if vs := Evaluate(observe(1, th), protected(0, 0)); !strings.Contains(Summary(vs), "duplicate_message_id") {
		t.Fatalf("the same answer twice on one subject is a repeat, got %s", Summary(vs))
	}
}

func TestViolations(t *testing.T) {
	dupSynth := healthyThread("a")
	dupSynth.msgID(MsgSynthesis, "other-answer")
	dupID := healthyThread("a")
	dupID.msgID(MsgSynthesis, dupID.msgs[len(dupID.msgs)-2].MessageID)
	twoCloses := healthyThread("a")
	twoCloses.msgID(MsgThreadClose, "second-close")
	twoBriefs := healthyThread("a")
	twoBriefs.msgID(MsgAdvisory, "second-brief")
	badReason := newBuilder("a")
	badReason.ev(EvThreadOpened, EventData{ConversationID: badReason.conv}).msg(MsgThreadStart)
	badReason.msg(MsgSynthesis).msg(MsgThreadClose).ev(EvThreadClosed, EventData{Reason: "zombie"})
	mismatch := healthyThread("a")
	mismatch.evs[len(mismatch.evs)-1].Data.Reason = reasonFallback
	twoClosed := healthyThread("a")
	twoClosed.ev(EvThreadClosed, EventData{Reason: reasonSynthesized})
	dupEvent := healthyThread("a")
	dupEvent.evID(EvSettled, dupEvent.evs[1].EventID, EventData{})

	cases := []struct {
		name   string
		obs    Observation
		exp    Expect
		expect string
	}{
		{"second answer id", observe(1, dupSynth), protected(0, 0), "one_answer_on_bus,duplicate_message"},
		{"same answer id twice", observe(1, dupID), protected(0, 0), "one_answer_on_bus,duplicate_message_id"},
		{"two closes", observe(1, twoCloses), protected(0, 0), "one_thread_close,duplicate_message"},
		{"two briefs", observe(1, twoBriefs), protected(0, 0), "duplicate_message"},
		{"asker got none", observe(0, healthyThread("a")), protected(0, 0), "one_answer_to_asker"},
		{"asker got two", observe(2, healthyThread("a")), protected(0, 0), "one_answer_to_asker"},
		{"zombie reason", observe(1, badReason), protected(0, 0), "closed_reason"},
		{"reason differs from answer", observe(1, mismatch), protected(0, 0), "closed_reason_matches_answer"},
		{"closed logged twice", observe(1, twoClosed), protected(0, 0), "one_thread_closed_event"},
		{"duplicate event id", observe(1, dupEvent), protected(0, 0), "unique_event_ids"},
		{"resume missing", observe(1, healthyThread("a")), protected(1, 1), "coordinator_resumed"},
		{"resumed too often", observe(1, resumedThread("a")), protected(0, 0), "coordinator_resumed"},
		{"second thread", observe(1, healthyThread("a"), healthyThread("b")), protected(0, 0),
			"one_thread_per_question,one_answer_on_bus,one_thread_close,coordinator_resumed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := names(Evaluate(c.obs, c.exp))
			if c.name == "second thread" {
				if !strings.Contains(got, "one_thread_per_question") {
					t.Fatalf("got %q", got)
				}
				return
			}
			if got != c.expect {
				t.Fatalf("got %q, want %q", got, c.expect)
			}
		})
	}
}

func TestOpenThreadAndMetrics(t *testing.T) {
	o := observe(1, healthyThread("a"))
	o.OpenAtEnd = []string{"a"}
	o.Metrics = map[string]float64{kMismatch: 1, "threads_unlogged": 2, "append_failures": 0, "fenced": 3}
	got := names(Evaluate(o, protected(0, 0)))
	if got != "thread_left_open,coordinator_metrics,coordinator_metrics" {
		t.Fatalf("got %q", got)
	}
}

func TestControlModeSkipsLogChecksAndFlagsAbandonedThread(t *testing.T) {
	old := newBuilder("old")
	old.msg(MsgThreadStart).msg(MsgAdvisory)
	fresh := newBuilder("new")
	fresh.msg(MsgThreadStart).msg(MsgSynthesis).msg(MsgThreadClose)
	got := names(Evaluate(observe(1, old, fresh), Expect{Mode: ModeControl}))
	if got != "one_thread_per_question,thread_abandoned" {
		t.Fatalf("got %q", got)
	}
}

func TestSummary(t *testing.T) {
	if got := Summary([]Violation{{Check: "a", Detail: "x"}, {Check: "b", Detail: "y"}}); got != "a: x; b: y" {
		t.Fatalf("got %q", got)
	}
}
