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
	"testing"
)

// fakeEnv returns a getenv func backed by a map, for testing the gating logic
// without touching the process environment or a live NATS.
func fakeEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestMaybeWriteTranscript_NoKeyIsNoop(t *testing.T) {
	// No TRANSCRIPT_KEY → standalone test → no write attempted, no error.
	err := maybeWriteTranscript(&RunOutcome{}, fakeEnv(map[string]string{}))
	if err != nil {
		t.Errorf("no-key path must be a clean no-op, got: %v", err)
	}
}

func TestMaybeWriteTranscript_KeyButNoNatsIsNoop(t *testing.T) {
	// TRANSCRIPT_KEY set but NATS_URL empty → skip (don't attempt a dial),
	// never error (transcript capture must not fail the run).
	err := maybeWriteTranscript(&RunOutcome{},
		fakeEnv(map[string]string{"TRANSCRIPT_KEY": "ns/suite/run/s0-i1.json"}))
	if err != nil {
		t.Errorf("missing NATS_URL must be a clean no-op, got: %v", err)
	}
}

func TestFirstThreadID(t *testing.T) {
	if got := firstThreadID(nil); got != "" {
		t.Errorf("empty events → empty threadId, got %q", got)
	}
	events := []SignalEvent{
		{Type: "connected"},
		{Type: "thread_found", ThreadID: "abc-123"},
		{Type: "synthesis", ThreadID: "abc-123"},
	}
	if got := firstThreadID(events); got != "abc-123" {
		t.Errorf("firstThreadID = %q, want abc-123", got)
	}
}

func TestRunOutcomeMarshalsTranscript(t *testing.T) {
	// The transcript blob is the marshaled RunOutcome — verify the discussion
	// events + metadata round-trip so the dashboard drill-down has real data.
	outcome := &RunOutcome{
		Assertions:     []AssertionResult{{Raw: "synthesis is non-empty", Passed: true, Message: "ok"}},
		ConversationID: "conv-1",
		ThreadID:       "thread-1",
		Question:       "Are pods healthy?",
		StartedAt:      "2026-06-01T00:00:00Z",
		DurationMs:     1234,
		Events: []SignalEvent{
			{Type: "phase", Agent: "k8s-workloads", Status: "evaluating", GPU: "rtx-5090"},
			{Type: "finding", Agent: "k8s-workloads", Signal: "agree", Summary: "47/49 running"},
			{Type: "synthesis", Content: "All healthy.", ThreadID: "thread-1"},
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
	if back.ThreadID != "thread-1" || back.DurationMs != 1234 {
		t.Errorf("metadata round-trip lost: %+v", back)
	}
	if back.Events[1].Signal != "agree" {
		t.Errorf("event detail lost: %+v", back.Events[1])
	}
}
