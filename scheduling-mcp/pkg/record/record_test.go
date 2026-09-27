package record

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEffectiveKind_DefaultsToFollowup(t *testing.T) {
	r := Record{}
	if got := r.EffectiveKind(); got != KindFollowup {
		t.Errorf("empty Kind → EffectiveKind: got %q, want %q", got, KindFollowup)
	}
	r.Kind = KindReminder
	if got := r.EffectiveKind(); got != KindReminder {
		t.Errorf("Kind=reminder → EffectiveKind: got %q, want %q", got, KindReminder)
	}
}

func TestHasSource(t *testing.T) {
	r := Record{}
	if r.HasSource() {
		t.Error("empty SourceThreadID should report false")
	}
	r.SourceThreadID = "thread-7"
	if !r.HasSource() {
		t.Error("non-empty SourceThreadID should report true")
	}
}

func TestRecord_RoundTrip(t *testing.T) {
	in := Record{
		ScheduleID:     "abc-123",
		Kind:           KindReminder,
		TriggerAt:      time.Date(2026, 5, 16, 9, 0, 0, 0, time.UTC),
		ScheduledBy:    "scheduler-advisor",
		Namespace:      "team-a",
		Crew:           "homelab-pilot",
		Channel:        "general",
		Message:        "Water the plants",
		Reason:         "user asked while we were discussing dishwasher",
		SourceThreadID: "thread-42",
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Record
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("round-trip mismatch:\n got=%+v\nwant=%+v", out, in)
	}
}

func TestRecord_OmitemptyFields(t *testing.T) {
	r := Record{
		ScheduleID: "id",
		TriggerAt:  time.Now(),
		Crew:       "homelab-pilot",
		Query:      "What's up?",
		Kind:       KindFollowup,
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	for _, omitted := range []string{"scheduledBy", "channel", "message", "reason", "sourceThreadId"} {
		if strings.Contains(s, omitted) {
			t.Errorf("expected %q to be omitted when empty, got: %s", omitted, s)
		}
	}
}

func TestRecord_NamespaceIsAlwaysSerialized(t *testing.T) {
	raw, err := json.Marshal(Record{ScheduleID: "id", Namespace: "team-a", Crew: "pilot"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"namespace":"team-a"`) {
		t.Errorf("namespace missing from %s", raw)
	}
}
