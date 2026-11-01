/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package coordchaos

import "testing"

// prefix returns the first n entries of the healthy thread's log and the messages that
// came before the nth event (by stream sequence).
func prefix(b *builder, n int) Thread {
	th := b.thread()
	if n >= len(th.Events) {
		return th
	}
	cut := th.Events[n-1].Seq
	out := Thread{ID: th.ID, Events: th.Events[:n]}
	for _, m := range th.Msgs {
		if m.Seq <= cut {
			out.Msgs = append(out.Msgs, m)
		}
	}
	return out
}

// Each prefix of the healthy log sits at exactly one point or none; this walks every prefix.
func TestEveryPrefixSitsAtMostAtOneLogPoint(t *testing.T) {
	b := healthyThread("a")
	total := len(b.evs)
	reached := map[Point]bool{}
	for n := 1; n <= total; n++ {
		th := prefix(b, n)
		var at []Point
		for _, p := range AllPoints {
			if p.Holds(th) {
				at = append(at, p)
				reached[p] = true
			}
		}
		if len(at) > 1 {
			t.Fatalf("prefix %d sits at %v", n, at)
		}
	}
	for _, p := range AllPoints {
		if p == PointReviewStarted || p == PointThreadClose {
			continue // no review phase in the fixture; thread_close is tested on its own
		}
		if !reached[p] {
			t.Errorf("no prefix of the healthy log reaches %s", p)
		}
	}
}

func TestPointFrontiers(t *testing.T) {
	b := healthyThread("a")
	th := b.thread()
	byType := func(typ string) int {
		for i, e := range th.Events {
			if e.Type == typ {
				return i
			}
		}
		t.Fatalf("no %s", typ)
		return 0
	}
	cases := []struct {
		point Point
		upTo  int // events kept
		want  bool
	}{
		{PointThreadOpened, byType(EvThreadOpened) + 1, true},
		{PointThreadOpened, byType(EvSubcommitteeConvened) + 1, false},
		{PointSubcommitteeConvened, byType(EvSubcommitteeConvened) + 1, true},
		{PointSubcommitteeConvened, byType(EvEvaluationStarted) + 1, false},
		{PointEvaluationStarted, byType(EvEvaluationStarted) + 1, true},
		{PointEvaluationStarted, byType(EvSettled) + 1, false},
		{PointSynthesisStarted, byType(EvSynthesisStarted) + 1, true},
		{PointSynthesisStarted, byType(EvSynthesisDone) + 1, false},
		{PointSynthesisDone, byType(EvSynthesisDone) + 1, true},
		{PointThreadClose, byType(EvThreadClosed) + 1, false},
	}
	for _, c := range cases {
		got := c.point.Holds(prefix(b, c.upTo))
		if got != c.want {
			t.Errorf("%s with %d events: got %t, want %t", c.point, c.upTo, got, c.want)
		}
	}
}

func TestThreadCloseHoldsBetweenTheMessageAndTheEvent(t *testing.T) {
	th := healthyThread("a").thread()
	th.Events = th.Events[:len(th.Events)-1]
	if !PointThreadClose.Holds(th) {
		t.Fatal("thread_close published and thread_closed not logged should hold")
	}
	if PointSynthesisDone.Holds(th) {
		t.Fatal("the answer is published, so synthesis_done no longer holds")
	}
}

func TestReviewStartedFrontier(t *testing.T) {
	b := newBuilder("a")
	b.ev(EvThreadOpened, EventData{}).ev(EvSubcommitteeConvened, EventData{}).ev(EvEvaluationStarted, EventData{})
	b.ev(EvSettled, EventData{}).ev(EvReviewStarted, EventData{})
	if !PointReviewStarted.Holds(b.thread()) {
		t.Fatal("review_started should hold")
	}
	b.ev(EvSynthesisStarted, EventData{})
	if PointReviewStarted.Holds(b.thread()) {
		t.Fatal("review_started should stop holding once synthesis starts")
	}
}

func TestSynthesisDoneStopsHoldingOncePublished(t *testing.T) {
	b := newBuilder("a")
	b.ev(EvThreadOpened, EventData{}).ev(EvSynthesisDone, EventData{Outcome: reasonDirect})
	if !PointSynthesisDone.Holds(b.thread()) {
		t.Fatal("answer logged and unpublished should hold")
	}
	b.msg(MsgSynthesis)
	if PointSynthesisDone.Holds(b.thread()) {
		t.Fatal("answer published should not hold")
	}
}

func TestMessageFrontiersForControlMode(t *testing.T) {
	b := newBuilder("a")
	b.msg(MsgThreadStart)
	if !PointThreadOpened.HoldsMsgs(b.thread()) {
		t.Fatal("thread_start alone is the opened point")
	}
	b.msg(MsgAdvisoryReady)
	if !PointEvaluationStarted.HoldsMsgs(b.thread()) || PointThreadOpened.HoldsMsgs(b.thread()) {
		t.Fatal("advisory_ready moves to evaluation")
	}
	b.msg("agree")
	if !PointSynthesisStarted.HoldsMsgs(b.thread()) || PointEvaluationStarted.HoldsMsgs(b.thread()) {
		t.Fatal("a terminal signal moves to synthesis")
	}
	b.msg(MsgSynthesis)
	if PointSynthesisStarted.HoldsMsgs(b.thread()) {
		t.Fatal("the answer ends synthesis")
	}
	if PointSynthesisDone.HoldsMsgs(b.thread()) {
		t.Fatal("no log, no synthesis_done point")
	}
}

func TestParsePointAndNarrow(t *testing.T) {
	for _, p := range AllPoints {
		got, err := ParsePoint(string(p))
		if err != nil || got != p {
			t.Fatalf("%s: %v %v", p, got, err)
		}
	}
	if _, err := ParsePoint("nope"); err == nil {
		t.Fatal("unknown point accepted")
	}
	if !PointSynthesisDone.Narrow() || PointSynthesisStarted.Narrow() {
		t.Fatal("narrow classification wrong")
	}
}
