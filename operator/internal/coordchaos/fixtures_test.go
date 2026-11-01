/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package coordchaos

import (
	"fmt"
	"time"
)

const (
	tContext  = "kind-chaos"
	tHello    = "hello"
	tBoom     = "boom"
	kMismatch = "checks.mismatch"
	kFenced   = "fenced"
	vAnswer   = "one_answer_on_bus"
)

// builder assembles a thread's log and messages in stream order for fixtures.
type builder struct {
	id   string
	conv string
	evs  []Event
	msgs []Msg
	seq  uint64
	t0   time.Time
}

func newBuilder(id string) *builder {
	return &builder{id: id, conv: "conv-" + id, t0: time.Unix(1_000_000, 0)}
}

func (b *builder) next() uint64 { b.seq++; return b.seq }

func (b *builder) ev(typ string, d EventData) *builder {
	n := b.next()
	b.evs = append(b.evs, Event{
		Seq: n, EventID: fmt.Sprintf("%s/%d/%s", b.id, n, typ), Type: typ, ThreadID: b.id,
		Data: d, Seen: b.t0.Add(time.Duration(n) * time.Second),
	})
	return b
}

func (b *builder) evID(typ, id string, d EventData) *builder {
	b.ev(typ, d)
	b.evs[len(b.evs)-1].EventID = id
	return b
}

func (b *builder) msg(typ string) *builder { return b.msgID(typ, fmt.Sprintf("%s/%d", typ, b.seq+1)) }

func (b *builder) msgID(typ, id string) *builder {
	n := b.next()
	m := Msg{
		Seq: n, MessageID: id, ThreadID: b.id, MessageType: typ, AgentName: "agent",
		Seen: b.t0.Add(time.Duration(n) * time.Second),
	}
	if typ == MsgThreadStart {
		m.ConversationID = b.conv
	}
	b.msgs = append(b.msgs, m)
	return b
}

// copyOn republishes the last message of typ, with the same id, on another subject,
// as the coordinator does for an answer and a thread_close (channel and broadcast).
func (b *builder) copyOn(typ, subject string) *builder {
	for i := len(b.msgs) - 1; i >= 0; i-- {
		if b.msgs[i].MessageType == typ {
			m := b.msgs[i]
			m.Seq, m.Subject = b.next(), subject
			b.msgs = append(b.msgs, m)
			return b
		}
	}
	return b
}

func (b *builder) thread() Thread {
	return Thread{ID: b.id, Events: append([]Event(nil), b.evs...), Msgs: append([]Msg(nil), b.msgs...)}
}

// healthyThread is a thread that ran to its answer with nothing going wrong.
func healthyThread(id string) *builder {
	b := newBuilder(id)
	b.ev(EvThreadOpened, EventData{ConversationID: b.conv}).msg(MsgThreadStart)
	b.ev(EvPhaseEntered, EventData{Phase: "ADVISORY"})
	b.ev(EvSubcommitteeConvened, EventData{}).msg(MsgAdvisory).msg(MsgSubcommittee)
	b.ev(EvEvaluationStarted, EventData{}).msg(MsgAdvisoryReady)
	b.msg("agree").ev(EvSignalApplied, EventData{Signal: "agree"})
	b.ev(EvSettled, EventData{})
	b.ev(EvSynthesisStarted, EventData{})
	b.ev(EvSynthesisDone, EventData{Outcome: reasonSynthesized}).msg(MsgSynthesis).msg(MsgThreadClose)
	b.ev(EvThreadClosed, EventData{Reason: reasonSynthesized})
	return b
}

// resumedThread is a healthy thread that a second coordinator took over after the
// first stopped at evaluation.
func resumedThread(id string) *builder {
	b := newBuilder(id)
	b.ev(EvThreadOpened, EventData{ConversationID: b.conv}).msg(MsgThreadStart)
	b.ev(EvSubcommitteeConvened, EventData{}).msg(MsgAdvisory).msg(MsgSubcommittee)
	b.ev(EvEvaluationStarted, EventData{}).msg(MsgAdvisoryReady)
	b.ev(EvCoordinatorResumed, EventData{FromSeq: 3})
	b.msg("agree").ev(EvSignalApplied, EventData{Signal: "agree"})
	b.ev(EvSettled, EventData{}).ev(EvSynthesisStarted, EventData{})
	b.ev(EvSynthesisDone, EventData{Outcome: reasonSynthesized}).msg(MsgSynthesis).msg(MsgThreadClose)
	b.ev(EvThreadClosed, EventData{Reason: reasonSynthesized})
	return b
}

func observe(asker int, ths ...*builder) Observation {
	o := Observation{AskerSyntheses: asker, Metrics: map[string]float64{}}
	for _, b := range ths {
		o.Threads = append(o.Threads, b.thread())
	}
	return o
}
