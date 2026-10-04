package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/scheduling-mcp/pkg/record"
)

// Fixture values shared by the handler tests.
const (
	otherCrewScheduleID = "other-1"
	ourMessage          = "ours"
)

// memKV is a goroutine-safe in-memory KV used by handler tests.
type memKV struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newKV() *memKV { return &memKV{m: map[string][]byte{}} }

func (k *memKV) Put(_ context.Context, key string, v []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[key] = append([]byte(nil), v...)
	return nil
}
func (k *memKV) Get(_ context.Context, key string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if v, ok := k.m[key]; ok {
		return append([]byte(nil), v...), nil
	}
	return nil, nil
}
func (k *memKV) Delete(_ context.Context, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.m[key]; !ok {
		return errors.New("not found")
	}
	delete(k.m, key)
	return nil
}
func (k *memKV) Keys(_ context.Context) ([]string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	keys := make([]string, 0, len(k.m))
	for k := range k.m {
		keys = append(keys, k)
	}
	return keys, nil
}

// fixedNow makes tool confirmations deterministic.
var fixedNow = time.Date(2026, 5, 16, 14, 30, 0, 0, time.UTC)

const testNamespace = "team-a"

func newSet() *Set {
	return &Set{KV: newKV(), Namespace: testNamespace, Crew: "homelab-pilot", Now: func() time.Time { return fixedNow }}
}

// putForeign writes a record for another namespace or crew straight into KV.
func putForeign(t *testing.T, s *Set, namespace, crew string) {
	t.Helper()
	foreign := record.Record{
		ScheduleID: otherCrewScheduleID, Kind: record.KindReminder,
		TriggerAt: fixedNow.Add(time.Hour), Namespace: namespace, Crew: crew,
		Channel: "general", Message: "not yours",
	}
	b, _ := json.Marshal(foreign)
	_ = s.KV.Put(t.Context(), foreign.ScheduleID, b)
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// ─── set_reminder ──────────────────────────────────────────────────────

func TestSetReminder_WritesRecordAndConfirms(t *testing.T) {
	s := newSet()
	args := mustJSON(t, map[string]any{
		argWhen:           "in 30 minutes",
		argMessage:        "water the plants",
		argSourceThreadID: "thread-7",
		argReason:         "asked mid-conversation",
	})
	out, err := s.Call(t.Context(), toolSetReminder, args)
	if err != nil {
		t.Fatalf("set_reminder: %v", err)
	}
	if !strings.Contains(out, "Reminder scheduled") {
		t.Errorf("confirmation should mention scheduling: %q", out)
	}

	keys, _ := s.KV.Keys(t.Context())
	if len(keys) != 1 {
		t.Fatalf("expected 1 record, got %d", len(keys))
	}
	raw, _ := s.KV.Get(t.Context(), keys[0])
	var r record.Record
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	if r.Kind != record.KindReminder {
		t.Errorf("kind: got %q, want %q", r.Kind, record.KindReminder)
	}
	if r.Message != "water the plants" {
		t.Errorf("message: got %q", r.Message)
	}
	if r.SourceThreadID != "thread-7" {
		t.Errorf("sourceThreadID: got %q", r.SourceThreadID)
	}
	wantTrigger := fixedNow.Add(30 * time.Minute).UTC()
	if !r.TriggerAt.Equal(wantTrigger) {
		t.Errorf("triggerAt: got %v, want %v", r.TriggerAt, wantTrigger)
	}
	assertScopedToSet(t, s, &r)
}

// assertScopedToSet fails unless the record carries the Set's namespace and crew.
func assertScopedToSet(t *testing.T, s *Set, r *record.Record) {
	t.Helper()
	if r.Namespace != s.Namespace || r.Crew != s.Crew {
		t.Errorf("record scoped to %s/%s, want %s/%s", r.Namespace, r.Crew, s.Namespace, s.Crew)
	}
}

func TestSetReminder_RejectsMissingFields(t *testing.T) {
	s := newSet()
	_, err := s.Call(t.Context(), toolSetReminder, mustJSON(t, map[string]any{argWhen: "in 5m"}))
	if err == nil {
		t.Error("missing message should error")
	}
	_, err = s.Call(t.Context(), toolSetReminder, mustJSON(t, map[string]any{argMessage: "hi"}))
	if err == nil {
		t.Error("missing when should error")
	}
}

func TestSetReminder_RejectsBadTime(t *testing.T) {
	s := newSet()
	_, err := s.Call(t.Context(), toolSetReminder, mustJSON(t, map[string]any{
		argWhen: "sometime soon", argMessage: "hi",
	}))
	if err == nil {
		t.Error("unrecognized time expression should error")
	}
	if n := len(s.KV.(*memKV).m); n != 0 {
		t.Errorf("a rejected time must write nothing, KV holds %d records", n)
	}
}

func TestScheduleFollowup_RejectsBadTimeWithoutWriting(t *testing.T) {
	s := newSet()
	_, err := s.Call(t.Context(), toolScheduleFollowup, mustJSON(t, map[string]any{
		argWhen: "sometime soon", argQuery: "is rig0 healthy?",
	}))
	if err == nil {
		t.Error("unrecognized time expression should error")
	}
	if n := len(s.KV.(*memKV).m); n != 0 {
		t.Errorf("a rejected time must write nothing, KV holds %d records", n)
	}
}

func TestSpecifications_RequiredArguments(t *testing.T) {
	want := map[string][]string{
		toolSetReminder:      {argWhen, argMessage},
		toolScheduleFollowup: {argWhen, argQuery},
		toolListScheduled:    nil,
		toolCancelScheduled:  {argScheduleID},
	}
	specs := newSet().Specifications()
	if len(specs) != len(want) {
		t.Fatalf("got %d tools, want %d", len(specs), len(want))
	}
	for _, spec := range specs {
		name, _ := spec["name"].(string)
		required, has := want[name]
		if !has {
			t.Errorf("unexpected tool %q", name)
			continue
		}
		schema, _ := spec["inputSchema"].(map[string]any)
		got, present := schema["required"]
		if required == nil {
			if present {
				t.Errorf("%s: want no required list, got %v", name, got)
			}
			continue
		}
		if gotList, _ := got.([]string); strings.Join(gotList, ",") != strings.Join(required, ",") {
			t.Errorf("%s: required = %v, want %v", name, got, required)
		}
	}
}

// ─── schedule_followup ─────────────────────────────────────────────────

func TestScheduleFollowup_WritesRecordAndConfirms(t *testing.T) {
	s := newSet()
	args := mustJSON(t, map[string]any{
		argWhen:  "tomorrow at 9am",
		argQuery: "Re-check disk pressure on rig0.",
	})
	out, err := s.Call(t.Context(), toolScheduleFollowup, args)
	if err != nil {
		t.Fatalf("schedule_followup: %v", err)
	}
	if !strings.Contains(out, "Follow-up scheduled") {
		t.Errorf("confirmation should mention follow-up: %q", out)
	}

	keys, _ := s.KV.Keys(t.Context())
	raw, _ := s.KV.Get(t.Context(), keys[0])
	var r record.Record
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	if r.Kind != record.KindFollowup {
		t.Errorf("kind: got %q, want %q", r.Kind, record.KindFollowup)
	}
	if r.Query == "" {
		t.Error("query should be set")
	}
	assertScopedToSet(t, s, &r)
}

// ─── list_scheduled ────────────────────────────────────────────────────

func TestListScheduled_FiltersByCrew(t *testing.T) {
	s := newSet()
	// Write one record for our crew via the tool.
	_, _ = s.Call(t.Context(), toolSetReminder, mustJSON(t, map[string]any{
		argWhen: "in 1h", argMessage: ourMessage,
	}))
	// And one for a different crew directly into KV.
	putForeign(t, s, testNamespace, "other-crew")

	out, err := s.Call(t.Context(), toolListScheduled, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("list_scheduled: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("crew filter failed: %d items, want 1", len(items))
	}
	if d, _ := items[0]["descriptor"].(string); d != ourMessage {
		t.Errorf("descriptor: got %q, want %q", d, ourMessage)
	}
}

// ─── cancel_scheduled ──────────────────────────────────────────────────

func TestCancelScheduled_DeletesOwnCrewRecord(t *testing.T) {
	s := newSet()
	out, _ := s.Call(t.Context(), toolSetReminder, mustJSON(t, map[string]any{
		argWhen: "in 30m", argMessage: ourMessage,
	}))
	// Pull the id from the confirmation by parsing "id=<uuid>".
	id := extractID(out)
	if id == "" {
		t.Fatalf("could not extract id from confirmation: %q", out)
	}
	res, err := s.Call(t.Context(), toolCancelScheduled, mustJSON(t, map[string]any{argScheduleID: id}))
	if err != nil {
		t.Fatalf("cancel_scheduled: %v", err)
	}
	if !strings.Contains(res, "Cancelled") {
		t.Errorf("expected cancellation confirmation, got %q", res)
	}
	keys, _ := s.KV.Keys(t.Context())
	if len(keys) != 0 {
		t.Errorf("record should be deleted, %d remain", len(keys))
	}
}

func TestCancelScheduled_RefusesForeignCrew(t *testing.T) {
	s := newSet()
	putForeign(t, s, testNamespace, "other-crew")

	_, err := s.Call(t.Context(), toolCancelScheduled, mustJSON(t, map[string]any{argScheduleID: otherCrewScheduleID}))
	if err == nil {
		t.Error("cross-crew cancel should fail")
	}
	keys, _ := s.KV.Keys(t.Context())
	if len(keys) != 1 {
		t.Errorf("foreign record should still exist; %d remain", len(keys))
	}
}

// TestSameCrewNameInAnotherNamespaceIsForeign: a crew of the same name in another
// namespace neither sees nor cancels this crew's schedules.
func TestSameCrewNameInAnotherNamespaceIsForeign(t *testing.T) {
	s := newSet()
	putForeign(t, s, "team-b", s.Crew)

	out, err := s.Call(t.Context(), toolListScheduled, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("list_scheduled: %v", err)
	}
	if out != "[]" {
		t.Errorf("another namespace's record listed: %s", out)
	}
	if _, err := s.Call(t.Context(), toolCancelScheduled, mustJSON(t, map[string]any{argScheduleID: otherCrewScheduleID})); err == nil {
		t.Error("cross-namespace cancel should fail")
	}
	if keys, _ := s.KV.Keys(t.Context()); len(keys) != 1 {
		t.Errorf("foreign record should still exist; %d remain", len(keys))
	}
}

func TestCancelScheduled_RejectsMissingID(t *testing.T) {
	s := newSet()
	_, err := s.Call(t.Context(), toolCancelScheduled, json.RawMessage(`{}`))
	if err == nil {
		t.Error("missing scheduleId should error")
	}
}

// ─── Specifications ────────────────────────────────────────────────────

func TestSpecifications_AllFourToolsPresent(t *testing.T) {
	s := newSet()
	specs := s.Specifications()
	if len(specs) != 4 {
		t.Fatalf("expected 4 tools, got %d", len(specs))
	}
	names := make(map[string]bool)
	for _, sp := range specs {
		n, _ := sp["name"].(string)
		names[n] = true
	}
	for _, expected := range []string{toolSetReminder, toolScheduleFollowup, toolListScheduled, toolCancelScheduled} {
		if !names[expected] {
			t.Errorf("missing tool spec: %s", expected)
		}
	}
}

// ─── helpers ────────────────────────────────────────────────────────────

func extractID(s string) string {
	const marker = "id="
	i := strings.Index(s, marker)
	if i < 0 {
		return ""
	}
	rest := s[i+len(marker):]
	end := strings.IndexAny(rest, " \t).")
	if end < 0 {
		end = len(rest)
	}
	return strings.TrimSpace(rest[:end])
}
