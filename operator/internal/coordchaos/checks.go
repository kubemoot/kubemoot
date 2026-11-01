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
	"sort"
	"strings"
)

// Mode says whether the coordinator under test has its protections on.
type Mode string

const (
	// ModeProtected runs against a coordinator with the event log and resume on (and
	// leases for the cases that need them). Every violation is a failure.
	ModeProtected Mode = "protected"
	// ModeControl runs against a coordinator with the flags off and documents what a kill
	// loses today. A violation is the expected result and is reported as such.
	ModeControl Mode = "control"
)

// Violation is one broken invariant with the evidence that shows it.
type Violation struct {
	Check    string   `json:"check"`
	Detail   string   `json:"detail"`
	Evidence []string `json:"evidence,omitempty"`
}

// Observation is the state a check reads after a trial: the threads the question started,
// what the asker's stream delivered, and the counters the coordinators reported.
type Observation struct {
	// Threads are the threads started for the question, oldest first.
	Threads []Thread
	// AskerSyntheses counts the synthesis events the asker's stream delivered; -1 when
	// the asker was not read.
	AskerSyntheses int
	// OpenAtEnd are threads of this run the log shows still open after the trial settled.
	OpenAtEnd []string
	// Metrics holds counter deltas summed over the coordinator pods, by name.
	Metrics map[string]float64
}

// Expect is what a trial should show.
type Expect struct {
	Mode Mode
	// ResumedMin and ResumedMax bound the coordinator_resumed events of the thread.
	ResumedMin, ResumedMax int
}

// allowedReasons are the thread_closed reasons of a thread that reached its answer.
var allowedReasons = map[string]bool{
	reasonSynthesized: true, reasonCapacity: true, reasonDirect: true, reasonFallback: true,
}

// singletonMessages are coordinator messages a thread publishes once.
var singletonMessages = []string{
	MsgAdvisory, MsgSubcommittee, MsgAdvisoryReady, MsgReviewReady, MsgSynthesis, MsgThreadClose,
}

// repeatForbidden are the singletons whose id must never appear twice on the stream.
// The others carry derived ids and a repeat after the stream's duplicate window is
// dropped by every consumer, so only a second distinct id counts for them.
var repeatForbidden = map[string]bool{MsgAdvisory: true, MsgSynthesis: true, MsgThreadClose: true}

type check func(Observation, Expect) []Violation

var allChecks = []check{
	checkOneThread, checkAnswer, checkClose, checkMessageIDs, checkReason,
	checkResumed, checkEventIDs, checkOpen, checkMetrics,
}

// Evaluate runs every check and returns the violations in a stable order.
func Evaluate(o Observation, e Expect) []Violation {
	out := make([]Violation, 0, len(allChecks))
	for _, c := range allChecks {
		out = append(out, c(o, e)...)
	}
	return out
}

func threadIDs(o Observation) []string {
	ids := make([]string, 0, len(o.Threads))
	for _, t := range o.Threads {
		ids = append(ids, t.ID)
	}
	return ids
}

// totalMsgs counts one question's messages of a type on the busiest subject. The
// coordinator publishes an answer and a thread_close to the thread's channel subject and
// to its broadcast subject by design, so each subject carries one; a second on the same
// subject is a real repeat.
func totalMsgs(o Observation, messageType string) int {
	per := map[string]int{}
	for _, t := range o.Threads {
		for _, m := range t.MsgsOf(messageType) {
			per[m.Subject]++
		}
	}
	most := 0
	for _, n := range per {
		most = max(most, n)
	}
	return most
}

func checkOneThread(o Observation, _ Expect) []Violation {
	if len(o.Threads) == 1 {
		return nil
	}
	return []Violation{{
		Check:    "one_thread_per_question",
		Detail:   fmt.Sprintf("the question started %d threads, want 1", len(o.Threads)),
		Evidence: threadIDs(o),
	}}
}

func checkAnswer(o Observation, _ Expect) []Violation {
	var out []Violation
	if n := totalMsgs(o, MsgSynthesis); n != 1 {
		out = append(out, Violation{
			Check:  "one_answer_on_bus",
			Detail: fmt.Sprintf("%d synthesis messages on the discussion stream, want 1", n),
		})
	}
	if o.AskerSyntheses >= 0 && o.AskerSyntheses != 1 {
		out = append(out, Violation{
			Check:  "one_answer_to_asker",
			Detail: fmt.Sprintf("the asker received %d answers, want 1", o.AskerSyntheses),
		})
	}
	return out
}

func checkClose(o Observation, e Expect) []Violation {
	var out []Violation
	if n := totalMsgs(o, MsgThreadClose); n != 1 {
		out = append(out, Violation{
			Check:  "one_thread_close",
			Detail: fmt.Sprintf("%d thread_close messages, want 1", n),
		})
	}
	if e.Mode != ModeProtected {
		return out
	}
	for _, t := range o.Threads {
		if n := t.Count(EvThreadClosed); n != 1 {
			out = append(out, Violation{
				Check:    "one_thread_closed_event",
				Detail:   fmt.Sprintf("thread %s logged thread_closed %d times, want 1", t.ID, n),
				Evidence: t.Types(),
			})
		}
	}
	return out
}

func checkMessageIDs(o Observation, _ Expect) []Violation {
	out := make([]Violation, 0, len(singletonMessages)*len(o.Threads))
	for _, t := range o.Threads {
		for _, typ := range singletonMessages {
			out = append(out, duplicateIDs(t, typ)...)
		}
	}
	return out
}

func duplicateIDs(t Thread, typ string) []Violation {
	ids := map[string]int{}
	onSubject := map[string]int{}
	for _, m := range t.MsgsOf(typ) {
		ids[m.MessageID]++
		onSubject[m.Subject+" "+m.MessageID]++
	}
	var out []Violation
	if len(ids) > 1 {
		out = append(out, Violation{
			Check:    "duplicate_message",
			Detail:   fmt.Sprintf("thread %s published %d different %s messages, want 1", t.ID, len(ids), typ),
			Evidence: keysOf(ids),
		})
	}
	for key, n := range onSubject {
		if n > 1 && repeatForbidden[typ] {
			out = append(out, Violation{
				Check:    "duplicate_message_id",
				Detail:   fmt.Sprintf("thread %s: %s message (subject and id %s) is on the stream %d times", t.ID, typ, key, n),
				Evidence: []string{key},
			})
		}
	}
	return out
}

func keysOf(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func checkReason(o Observation, e Expect) []Violation {
	if e.Mode != ModeProtected {
		return nil
	}
	var out []Violation
	for _, t := range o.Threads {
		closed, ok := lastEvent(t, EvThreadClosed)
		if !ok {
			continue
		}
		out = append(out, reasonViolations(t, closed)...)
	}
	return out
}

func reasonViolations(t Thread, closed Event) []Violation {
	var out []Violation
	if !allowedReasons[closed.Data.Reason] {
		out = append(out, Violation{
			Check:    "closed_reason",
			Detail:   fmt.Sprintf("thread %s closed with reason %q, want one of synthesized, capacity, direct, fallback", t.ID, closed.Data.Reason),
			Evidence: t.Types(),
		})
	}
	if done, ok := lastEvent(t, EvSynthesisDone); ok && done.Data.Outcome != closed.Data.Reason {
		out = append(out, Violation{
			Check:  "closed_reason_matches_answer",
			Detail: fmt.Sprintf("thread %s: synthesis_done outcome %q, thread_closed reason %q", t.ID, done.Data.Outcome, closed.Data.Reason),
		})
	}
	return out
}

func lastEvent(t Thread, typ string) (Event, bool) {
	for i := len(t.Events) - 1; i >= 0; i-- {
		if t.Events[i].Type == typ {
			return t.Events[i], true
		}
	}
	return Event{}, false
}

func checkResumed(o Observation, e Expect) []Violation {
	if e.Mode != ModeProtected || len(o.Threads) == 0 {
		return nil
	}
	t := o.Threads[0]
	n := t.Count(EvCoordinatorResumed)
	if n >= e.ResumedMin && n <= e.ResumedMax {
		return nil
	}
	return []Violation{{
		Check:    "coordinator_resumed",
		Detail:   fmt.Sprintf("thread %s logged coordinator_resumed %d times, want %d to %d", t.ID, n, e.ResumedMin, e.ResumedMax),
		Evidence: t.Types(),
	}}
}

func checkEventIDs(o Observation, e Expect) []Violation {
	if e.Mode != ModeProtected {
		return nil
	}
	var out []Violation
	for _, t := range o.Threads {
		seen := map[string]int{}
		for _, id := range t.EventIDs() {
			seen[id]++
		}
		for _, id := range keysOf(seen) {
			if seen[id] > 1 {
				out = append(out, Violation{
					Check:    "unique_event_ids",
					Detail:   fmt.Sprintf("thread %s: event %s is in the log %d times", t.ID, id, seen[id]),
					Evidence: []string{id},
				})
			}
		}
	}
	return out
}

func checkOpen(o Observation, e Expect) []Violation {
	if e.Mode == ModeProtected {
		if len(o.OpenAtEnd) == 0 {
			return nil
		}
		return []Violation{{
			Check:    "thread_left_open",
			Detail:   fmt.Sprintf("%d thread(s) still open in the log after the trial settled", len(o.OpenAtEnd)),
			Evidence: o.OpenAtEnd,
		}}
	}
	var out []Violation
	for _, t := range o.Threads {
		if t.MsgCount(MsgThreadClose) == 0 {
			out = append(out, Violation{
				Check:  "thread_abandoned",
				Detail: fmt.Sprintf("thread %s never published thread_close", t.ID),
			})
		}
	}
	return out
}

// Counter names the metrics check reads, and the phrase used when one is non-zero.
var badCounters = []struct{ key, what string }{
	{"checks.mismatch", "the close check found the log and the live state differ"},
	{"threads_unlogged", "a thread was marked unlogged"},
	{"append_failures", "an event append failed"},
}

func checkMetrics(o Observation, e Expect) []Violation {
	if e.Mode != ModeProtected {
		return nil
	}
	var out []Violation
	for _, c := range badCounters {
		if v := o.Metrics[c.key]; v > 0 {
			out = append(out, Violation{
				Check:    "coordinator_metrics",
				Detail:   fmt.Sprintf("%s (%s = %g)", c.what, c.key, v),
				Evidence: []string{c.key},
			})
		}
	}
	return out
}

// Summary renders violations on one line each, for logs.
func Summary(vs []Violation) string {
	lines := make([]string, 0, len(vs))
	for _, v := range vs {
		lines = append(lines, v.Check+": "+v.Detail)
	}
	return strings.Join(lines, "; ")
}
