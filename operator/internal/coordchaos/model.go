/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package coordchaos is the coordinator chaos test: it asks a crew a real question,
// waits on state (the coordinator log and the discussion stream) until the thread
// reaches a chosen point, kills or freezes the coordinator there, and checks from the
// same state that the thread still settled once and the asker got one answer. It is a
// test tool. Nothing in it runs during normal operation and it changes no discussion
// logic; the only things it writes are a question to the crew and the fault.
//
// The pure parts (the points, the checks, the report) take plain values so that
// fixtures of event sequences test them without a cluster. The cluster, the bus and the
// asker sit behind small interfaces that the runner depends on.
package coordchaos

import (
	"fmt"
	"time"
)

// Event is one coordinator log event as the harness reads it. Only the fields the checks
// use are kept, and unknown fields and types are ignored, as the bus contract tells every
// reader to do.
type Event struct {
	Seq         uint64    `json:"seq"`
	EventID     string    `json:"eventId"`
	Type        string    `json:"type"`
	ThreadID    string    `json:"threadId"`
	Round       int       `json:"round"`
	At          string    `json:"at"`
	Coordinator string    `json:"coordinator"`
	Data        EventData `json:"data"`
	// Seen is when the harness received the event, on the harness clock.
	Seen time.Time `json:"-"`
}

// EventData holds the data fields the checks read.
type EventData struct {
	Phase          string   `json:"phase,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	Outcome        string   `json:"outcome,omitempty"`
	Basis          string   `json:"basis,omitempty"`
	ConversationID string   `json:"conversationId,omitempty"`
	UserQuery      string   `json:"userQuery,omitempty"`
	Agent          string   `json:"agent,omitempty"`
	Signal         string   `json:"signal,omitempty"`
	MessageID      string   `json:"messageId,omitempty"`
	RequestSeq     int64    `json:"requestSeq,omitempty"`
	FromSeq        int64    `json:"fromSeq,omitempty"`
	Members        []string `json:"members,omitempty"`
	Roster         []string `json:"roster,omitempty"`
}

// Event type names, as the bus contract spells them.
const (
	EvThreadOpened          = "thread_opened"
	EvPhaseEntered          = "phase_entered"
	EvSubcommitteeConvened  = "subcommittee_convened"
	EvEvaluationStarted     = "evaluation_started"
	EvReviewStarted         = "review_started"
	EvSignalApplied         = "signal_applied"
	EvFailureRecorded       = "failure_recorded"
	EvSettled               = "settled"
	EvSynthesisStarted      = "synthesis_started"
	EvSynthesisDone         = "synthesis_done"
	EvThreadClosed          = "thread_closed"
	EvCoordinatorResumed    = "coordinator_resumed"
	MsgThreadStart          = "thread_start"
	MsgAdvisory             = "advisory"
	MsgSubcommittee         = "subcommittee"
	MsgAdvisoryReady        = "advisory_ready"
	MsgReviewReady          = "review_ready"
	MsgSynthesis            = "synthesis"
	MsgThreadClose          = "thread_close"
	signalAgree             = "agree"
	signalConcern           = "concern"
	signalBlock             = "block"
	signalStandAside        = "stand_aside"
	signalFailure           = "failure"
	reasonSynthesized       = "synthesized"
	reasonCapacity          = "capacity"
	reasonNoData            = "no_data"
	reasonDirect            = "direct"
	reasonFallback          = "fallback"
	metricResumes           = "kubemoot_coordinator_resumes_total"
	metricChecks            = "kubemoot_coordinator_log_checks_total"
	metricFenced            = "kubemoot_coordinator_log_fenced_total"
	metricAppendFailures    = "kubemoot_coordinator_log_append_failures_total"
	metricThreadsUnlogged   = "kubemoot_coordinator_threads_unlogged_total"
	envEventLog             = "KUBEMOOT_COORDINATOR_EVENT_LOG"
	envResume               = "KUBEMOOT_COORDINATOR_RESUME"
	envLease                = "KUBEMOOT_COORDINATOR_LEASE"
	envResumeMaxAge         = "KUBEMOOT_COORDINATOR_RESUME_MAX_AGE"
	defaultLeaseBucket      = "kubemoot_coordinator_leases"
	streamCoordinator       = "KUBEMOOT_COORDINATOR"
	streamDiscuss           = "KUBEMOOT_DISCUSS"
	flagTrue                = "true"
	coordinatorSubjectRoot  = "kubemoot.coordinator."
	discussionSubjectRoot   = "kubemoot.discuss."
	defaultPollInterval     = 200 * time.Millisecond
	defaultCoordinatorAgent = "coordinator"
)

// Msg is one discussion stream message of a crew.
type Msg struct {
	Seq            uint64    `json:"seq"`
	Subject        string    `json:"subject"`
	MessageID      string    `json:"messageId"`
	ThreadID       string    `json:"threadId"`
	AgentName      string    `json:"agentName"`
	MessageType    string    `json:"messageType"`
	Channel        string    `json:"channel,omitempty"`
	ConversationID string    `json:"conversationId,omitempty"`
	Seen           time.Time `json:"-"`
}

// Thread is everything the harness has read about one thread, in stream order.
type Thread struct {
	ID     string
	Events []Event
	Msgs   []Msg
}

// Count returns how many events of the type the thread's log holds.
func (t Thread) Count(eventType string) int {
	n := 0
	for i := range t.Events {
		if t.Events[i].Type == eventType {
			n++
		}
	}
	return n
}

// Has reports whether the log holds an event of the type.
func (t Thread) Has(eventType string) bool { return t.Count(eventType) > 0 }

// Closed reports whether the log holds thread_closed.
func (t Thread) Closed() bool { return t.Has(EvThreadClosed) }

// First returns the first event of the type.
func (t Thread) First(eventType string) (Event, bool) {
	for i := range t.Events {
		if t.Events[i].Type == eventType {
			return t.Events[i], true
		}
	}
	return Event{}, false
}

// MsgCount returns how many discussion messages of the type the thread holds.
func (t Thread) MsgCount(messageType string) int {
	n := 0
	for i := range t.Msgs {
		if t.Msgs[i].MessageType == messageType {
			n++
		}
	}
	return n
}

// MsgsOf returns the thread's messages of the type.
func (t Thread) MsgsOf(messageType string) []Msg {
	var out []Msg
	for i := range t.Msgs {
		if t.Msgs[i].MessageType == messageType {
			out = append(out, t.Msgs[i])
		}
	}
	return out
}

// terminalSignals counts the agent signals that end an agent's evaluation.
func (t Thread) terminalSignals() int {
	n := 0
	for i := range t.Msgs {
		switch t.Msgs[i].MessageType {
		case signalAgree, signalConcern, signalBlock, signalStandAside, signalFailure:
			n++
		}
	}
	return n
}

// EventIDs returns the thread's event ids in log order.
func (t Thread) EventIDs() []string {
	ids := make([]string, 0, len(t.Events))
	for i := range t.Events {
		ids = append(ids, t.Events[i].EventID)
	}
	return ids
}

// Types returns the thread's event types in log order, with a run of the same type
// collapsed to "type xN", for evidence in a report.
func (t Thread) Types() []string {
	var out []string
	run := 0
	for i := range t.Events {
		typ := t.Events[i].Type
		if i > 0 && t.Events[i-1].Type == typ {
			run++
			out[len(out)-1] = fmt.Sprintf("%s x%d", typ, run)
			continue
		}
		run = 1
		out = append(out, typ)
	}
	return out
}
