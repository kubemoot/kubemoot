/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package scheduler

import (
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/internal/scheduler/record"
)

const (
	testNamespace  = "team-a"
	otherNamespace = "team-b"
	pilotCrew      = "homelab-pilot"
	shortCrew      = "pilot"
)

func TestNew_AppliesDefaults(t *testing.T) {
	p := New(nil)
	if p.Interval != DefaultInterval {
		t.Errorf("default interval: got %v, want %v", p.Interval, DefaultInterval)
	}
	if p.Now == nil {
		t.Error("Now should be set to a non-nil function")
	}
}

func TestFire_RequiresCrew(t *testing.T) {
	p := New(nil)
	err := p.fire(t.Context(), &record.Record{Namespace: testNamespace, Kind: record.KindReminder, Message: "hi"})
	if err == nil {
		t.Error("expected error when crew is empty")
	}
}

func TestFire_RequiresNamespace(t *testing.T) {
	p := New(nil)
	err := p.fire(t.Context(), &record.Record{Crew: "x", Kind: record.KindReminder, Message: "hi"})
	if err == nil {
		t.Error("expected error when namespace is empty")
	}
}

func TestFire_FollowupRequiresQuery(t *testing.T) {
	p := New(nil)
	err := p.fire(t.Context(), &record.Record{Namespace: testNamespace, Crew: "x", Kind: record.KindFollowup})
	if err == nil {
		t.Error("expected error when followup has no query")
	}
}

func TestFire_ReminderRequiresMessage(t *testing.T) {
	p := New(nil)
	err := p.fire(t.Context(), &record.Record{Namespace: testNamespace, Crew: "x", Kind: record.KindReminder})
	if err == nil {
		t.Error("expected error when reminder has no message")
	}
}

func TestFire_UnknownKindRejected(t *testing.T) {
	p := New(nil)
	err := p.fire(t.Context(), &record.Record{Namespace: testNamespace, Crew: "x", Kind: "bogus", Query: "q", Message: "m"})
	if err == nil {
		t.Error("expected error on unknown kind")
	}
}

func TestThreadSubject(t *testing.T) {
	got := threadSubject(&record.Record{Namespace: testNamespace, Crew: pilotCrew}, "general", "thread-7")
	want := "kubemoot.discuss.team-a.homelab-pilot.general.thread-7"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestThreadSubject_SameCrewInTwoNamespacesDiffers(t *testing.T) {
	a := threadSubject(&record.Record{Namespace: testNamespace, Crew: shortCrew}, "general", "t")
	b := threadSubject(&record.Record{Namespace: otherNamespace, Crew: shortCrew}, "general", "t")
	if a == b {
		t.Errorf("subjects collide: %q", a)
	}
}

func TestRecordScope_RejectsUnscopedRecords(t *testing.T) {
	for _, rec := range []record.Record{
		{Crew: shortCrew},
		{Namespace: testNamespace},
		{Namespace: "team.a", Crew: shortCrew},
	} {
		if _, err := recordScope(&rec); err == nil {
			t.Errorf("recordScope(%+v) accepted an unscoped record", rec)
		}
	}
	if s, err := recordScope(&record.Record{Namespace: testNamespace, Crew: shortCrew}); err != nil || s.Namespace != testNamespace || s.Crew != shortCrew {
		t.Errorf("recordScope = %+v, %v", s, err)
	}
}

func TestFireMetadata_IncludesEffectiveKind(t *testing.T) {
	r := &record.Record{
		ScheduleID:  "abc",
		ScheduledBy: "scheduler-advisor",
		// Kind intentionally empty — should fall back to followup.
	}
	m := fireMetadata(r, false)
	if m["kind"] != record.KindFollowup {
		t.Errorf("kind: got %v, want %v", m["kind"], record.KindFollowup)
	}
	if m["scheduleId"] != "abc" {
		t.Errorf("scheduleId: got %v, want %v", m["scheduleId"], "abc")
	}
}

func TestFireMetadata_OmitsEmptyFields(t *testing.T) {
	r := &record.Record{Kind: record.KindReminder}
	m := fireMetadata(r, false)
	if _, ok := m["reason"]; ok {
		t.Error("empty reason should be omitted")
	}
	if _, ok := m["sourceThreadId"]; ok {
		t.Error("empty sourceThreadId should be omitted")
	}
	if _, ok := m["userQuery"]; ok {
		t.Error("userQuery should be omitted when includeUserQuery=false")
	}
}

func TestFireMetadata_InlineKeepsSourceThreadId(t *testing.T) {
	r := &record.Record{
		ScheduleID:     "abc",
		Kind:           record.KindFollowup,
		SourceThreadID: "thread-1",
		Query:          "what about disk?",
	}
	m := fireMetadata(r, false) // inline fire
	if m["sourceThreadId"] != "thread-1" {
		t.Errorf("inline fire should set sourceThreadId, got %v", m["sourceThreadId"])
	}
	if _, ok := m["originalSourceThreadId"]; ok {
		t.Error("inline fire should not set originalSourceThreadId")
	}
	if _, ok := m["degraded"]; ok {
		t.Error("inline fire should not set degraded flag")
	}
	if _, ok := m["userQuery"]; ok {
		t.Error("inline fire should not echo the user query in metadata")
	}
}

func TestFireMetadata_DegradedNewThreadFlagsOriginal(t *testing.T) {
	r := &record.Record{
		ScheduleID:     "abc",
		Kind:           record.KindFollowup,
		SourceThreadID: "thread-deleted",
		Query:          "what about disk?",
	}
	m := fireMetadata(r, true) // degraded → new-thread fire
	if m["originalSourceThreadId"] != "thread-deleted" {
		t.Errorf("degraded fire should set originalSourceThreadId, got %v", m["originalSourceThreadId"])
	}
	if m["degraded"] != true {
		t.Errorf("degraded fire should set degraded=true, got %v", m["degraded"])
	}
	if _, ok := m["sourceThreadId"]; ok {
		t.Error("new-thread fire must not advertise sourceThreadId — message lives on a fresh thread")
	}
	if m["userQuery"] != "what about disk?" {
		t.Errorf("new-thread fire should echo userQuery, got %v", m["userQuery"])
	}
}

// TestFire_DegradesToNewThreadWhenSourceMissing pins the orphan-thread fix:
// when the source thread has been deleted/aged-out, the poller must NOT
// publish a synthetic message on the dead subject (which would silently
// no-op against fresh ThreadStates and effectively swallow the schedule);
// it must degrade to the new-thread mode instead.
func TestFire_DegradesToNewThreadWhenSourceMissing(t *testing.T) {
	cases := []struct {
		name string
		rec  record.Record
	}{
		{
			name: "followup with missing source",
			rec: record.Record{
				ScheduleID:     "abc",
				Namespace:      testNamespace,
				Crew:           pilotCrew,
				Channel:        "general",
				Kind:           record.KindFollowup,
				Query:          "re-check disk",
				SourceThreadID: "thread-gone",
			},
		},
		{
			name: "reminder with missing source",
			rec: record.Record{
				ScheduleID:     "abc",
				Namespace:      testNamespace,
				Crew:           pilotCrew,
				Channel:        "general",
				Kind:           record.KindReminder,
				Message:        "ping me",
				SourceThreadID: "thread-gone",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := New(nil)
			var checkedStream, checkedSubject string
			p.SubjectHasMessages = func(stream, subject string) bool {
				checkedStream = stream
				checkedSubject = subject
				return false // simulate deleted thread
			}
			// Without a publisher, fire reaches the new-thread branch and
			// the publish itself is a no-op — that's enough to confirm the
			// branch was taken: the checker was consulted with the right
			// stream + subject.
			if err := p.fire(t.Context(), &tc.rec); err != nil {
				t.Fatalf("fire returned error: %v", err)
			}
			if checkedStream != DiscussStreamName {
				t.Errorf("stream: got %q, want %q", checkedStream, DiscussStreamName)
			}
			wantSubject := "kubemoot.discuss.team-a.homelab-pilot.general.thread-gone"
			if checkedSubject != wantSubject {
				t.Errorf("subject: got %q, want %q", checkedSubject, wantSubject)
			}
		})
	}
}

// TestFire_DoesNotCheckWhenNoSource — no SourceThreadID → no JetStream
// roundtrip needed, the fire is unambiguously new-thread.
func TestFire_DoesNotCheckWhenNoSource(t *testing.T) {
	p := New(nil)
	called := false
	p.SubjectHasMessages = func(string, string) bool {
		called = true
		return true
	}
	rec := &record.Record{
		ScheduleID: "abc",
		Namespace:  testNamespace,
		Crew:       pilotCrew,
		Channel:    "general",
		Kind:       record.KindFollowup,
		Query:      "x",
	}
	if err := p.fire(t.Context(), rec); err != nil {
		t.Fatalf("fire returned error: %v", err)
	}
	if called {
		t.Error("SubjectHasMessages should not be called when record has no SourceThreadID")
	}
}

// TestSourceExists_NoCheckerReturnsTrue — when no checker is wired
// (publisher nil, no NATS), the scheduler is optimistic about source
// existence; the publish path will no-op anyway.
func TestSourceExists_NoCheckerReturnsTrue(t *testing.T) {
	p := New(nil)
	if !p.sourceExists(&record.Record{Namespace: testNamespace, Crew: "crew"}, "general", "thread-x") {
		t.Error("expected sourceExists to be optimistic when no checker is wired")
	}
}

// Smoke test: time arithmetic isn't broken.
func TestPoller_NowReturnsCurrentTime(t *testing.T) {
	p := New(nil)
	// Start() sets Now if nil — invoke once to mimic that defaulting.
	if p.Now == nil {
		p.Now = time.Now
	}
	delta := time.Since(p.Now())
	if delta < 0 {
		delta = -delta
	}
	if delta > time.Second {
		t.Errorf("Now() drift too large: %v", delta)
	}
}
