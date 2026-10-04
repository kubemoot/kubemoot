/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Fixture values shared by the runner tests.
const (
	assertSynthesisNonEmpty = "synthesis is non-empty"
	sseThreadFound          = "thread_found"
	sseSynthesis            = "synthesis"
	testThreadID            = "thread-1"
)

// recordingEnv is a getenv backed by m that records every key read, without
// touching the process environment or a live NATS, so a test can see how far
// maybeWriteTranscript got before it returned.
func recordingEnv(m map[string]string) (func(string) string, *[]string) {
	var read []string
	return func(k string) string {
		read = append(read, k)
		return m[k]
	}, &read
}

func TestMaybeWriteTranscript_NoKeyIsNoop(t *testing.T) {
	// No TRANSCRIPT_KEY: a standalone test, so it stops before reading NATS_URL.
	getenv, read := recordingEnv(map[string]string{})
	maybeWriteTranscript(&RunOutcome{}, getenv)
	if strings.Join(*read, ",") != "TRANSCRIPT_KEY" {
		t.Errorf("no-key path must stop at TRANSCRIPT_KEY, read %v", *read)
	}
}

func TestMaybeWriteTranscript_KeyButNoNatsIsNoop(t *testing.T) {
	// TRANSCRIPT_KEY set but NATS_URL empty: skip before choosing a bucket or dialing.
	getenv, read := recordingEnv(map[string]string{"TRANSCRIPT_KEY": "ns/suite/run/s0-i1.json"})
	maybeWriteTranscript(&RunOutcome{}, getenv)
	if strings.Join(*read, ",") != "TRANSCRIPT_KEY,NATS_URL" {
		t.Errorf("missing NATS_URL must stop before the bucket, read %v", *read)
	}
}

func TestAnsweringThreadID(t *testing.T) {
	if got := answeringThreadID(nil); got != "" {
		t.Errorf("empty events: want empty threadId, got %q", got)
	}
	events := []SignalEvent{
		{Type: "connected"},
		{Type: sseThreadFound, ThreadID: "abc-123"},
		{Type: sseSynthesis},
	}
	if got := answeringThreadID(events); got != "abc-123" {
		t.Errorf("answeringThreadID = %q, want abc-123", got)
	}
	restarted := []SignalEvent{
		{Type: sseThreadFound, ThreadID: "abandoned"},
		{Type: "phase"},
		{Type: sseThreadFound, ThreadID: "answering"},
		{Type: sseSynthesis},
	}
	if got := answeringThreadID(restarted); got != "answering" {
		t.Errorf("after a coordinator restart: answeringThreadID = %q, want answering", got)
	}
}

func TestRunOutcomeMarshalsTranscript(t *testing.T) {
	// The transcript blob is the marshaled RunOutcome — verify the discussion
	// events + metadata round-trip so the dashboard drill-down has real data.
	outcome := &RunOutcome{
		Assertions:     []AssertionResult{{Raw: assertSynthesisNonEmpty, Passed: true, Message: "ok"}},
		ConversationID: "conv-1",
		ThreadID:       testThreadID,
		Question:       "Are pods healthy?",
		StartedAt:      "2026-06-01T00:00:00Z",
		DurationMs:     1234,
		Events: []SignalEvent{
			{Type: "phase", Agent: "k8s-workloads", Status: "evaluating", GPU: "rtx-5090"},
			{Type: "finding", Agent: "k8s-workloads", Signal: "agree", Summary: "47/49 running"},
			{Type: sseSynthesis, Content: "All healthy.", ThreadID: testThreadID},
		},
	}
	blob, err := json.Marshal(outcome)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back RunOutcome
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Events) != 3 {
		t.Errorf("events round-trip: got %d, want 3", len(back.Events))
	}
	if back.ThreadID != testThreadID || back.DurationMs != 1234 {
		t.Errorf("metadata round-trip lost: %+v", back)
	}
	if back.Events[1].Signal != "agree" {
		t.Errorf("event detail lost: %+v", back.Events[1])
	}
}
