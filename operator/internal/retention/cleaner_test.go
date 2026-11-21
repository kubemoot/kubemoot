/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package retention

import (
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

// fakeStore keeps KV entries and stream messages in memory.
type fakeStore struct {
	kv       map[string][]KVEntry   // bucket -> entries
	messages map[string][]time.Time // stream|subject -> message times
	failKV   map[string]error
	deleted  map[string][]string
	purged   []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{kv: map[string][]KVEntry{}, messages: map[string][]time.Time{}, failKV: map[string]error{}, deleted: map[string][]string{}}
}

func (f *fakeStore) KVEntries(bucket, prefix string) ([]KVEntry, error) {
	if err := f.failKV[bucket]; err != nil {
		return nil, err
	}
	var out []KVEntry
	for _, e := range f.kv[bucket] {
		if strings.HasPrefix(e.Key, prefix) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeStore) DeleteKVKeys(bucket string, keys []string) error {
	f.deleted[bucket] = append(f.deleted[bucket], keys...)
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	var left []KVEntry
	for _, e := range f.kv[bucket] {
		if !drop[e.Key] {
			left = append(left, e)
		}
	}
	f.kv[bucket] = left
	return nil
}

func (f *fakeStore) PurgeStreamBefore(stream, subject string, before time.Time) (int, error) {
	key := stream + "|" + subject
	f.purged = append(f.purged, key)
	var kept []time.Time
	removed := 0
	for _, m := range f.messages[key] {
		if m.Before(before) {
			removed++
		} else {
			kept = append(kept, m)
		}
	}
	f.messages[key] = kept
	return removed, nil
}

func (f *fakeStore) keys(bucket string) []string {
	out := make([]string, 0, len(f.kv[bucket]))
	for _, e := range f.kv[bucket] {
		out = append(out, e.Key)
	}
	sort.Strings(out)
	return out
}

var (
	epoch = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	scope = crewscope.Scope{Namespace: "ns", Crew: "crew"}
)

func ago(d time.Duration) time.Time { return epoch.Add(-d) }

func cleaner(s Store) *Cleaner { return &Cleaner{Store: s, Now: func() time.Time { return epoch }} }

func TestCleanRemovesExpiredConversationsAndKeepsTheRest(t *testing.T) {
	s := newFakeStore()
	s.kv[ConversationsBucket] = []KVEntry{
		{keyConvOld, ago(31 * day)},
		{"conv.ns.crew.edge", ago(30*day + time.Second)},
		{"conv.ns.crew.exactly", ago(30 * day)},
		{keyConvNew, ago(day)},
		{"conv.ns.other.old", ago(400 * day)}, // another crew
		{"conv.ns2.crew.old", ago(400 * day)}, // same crew name, other namespace
		{"conv.ns.crewx.old", ago(400 * day)}, // a crew whose name starts the same
	}
	got, err := cleaner(s).Run(scope, Policy{Conversations: 30 * day})
	if err != nil {
		t.Fatal(err)
	}
	if got.Conversations != 2 {
		t.Errorf("removed %d, want 2", got.Conversations)
	}
	want := []string{"conv.ns.crew.exactly", keyConvNew, "conv.ns.crewx.old", "conv.ns.other.old", "conv.ns2.crew.old"}
	if g := s.keys(ConversationsBucket); strings.Join(g, ",") != strings.Join(want, ",") {
		t.Errorf("left %v, want %v", g, want)
	}
}

func TestCleanMemoryUnusedGoesUsedStaysNeverKeepsAll(t *testing.T) {
	facts := func() []KVEntry {
		return []KVEntry{
			{"ns.crew.topic.unused", ago(91 * day)},
			{"ns.crew.topic.used", ago(2 * day)},
			{"ns.other.topic.unused", ago(500 * day)},
		}
	}
	s := newFakeStore()
	s.kv[MemoryBucket] = facts()
	got, err := cleaner(s).Run(scope, Policy{Memory: Of(90 * day)})
	if err != nil || got.Memory != 1 {
		t.Fatalf("removed %d %v, want 1", got.Memory, err)
	}
	if g := strings.Join(s.keys(MemoryBucket), ","); g != "ns.crew.topic.used,ns.other.topic.unused" {
		t.Errorf("left %s", g)
	}

	s = newFakeStore()
	s.kv[MemoryBucket] = facts()
	got, err = cleaner(s).Run(scope, Policy{Memory: Forever})
	if err != nil || got.Memory != 0 || len(s.kv[MemoryBucket]) != 3 {
		t.Errorf("never keeps every fact: %+v %v %v", got, err, s.keys(MemoryBucket))
	}
	if len(s.deleted) != 0 {
		t.Error("never must not delete")
	}
}

func TestCleanLeavesKindsWithoutAPolicy(t *testing.T) {
	s := newFakeStore()
	s.kv[ConversationsBucket] = []KVEntry{{keyConvOld, ago(900 * day)}}
	s.kv[MemoryBucket] = []KVEntry{{"ns.crew.t.k", ago(900 * day)}}
	got, err := cleaner(s).Run(scope, Policy{})
	if err != nil || got != (Removed{}) || len(s.purged) != 0 || len(s.deleted) != 0 {
		t.Errorf("an empty policy removes nothing: %+v %v", got, err)
	}
}

func TestCleanStreamsOfTheCrewOnly(t *testing.T) {
	s := newFakeStore()
	s.messages["KUBEMOOT_DISCUSS|kubemoot.discuss.ns.crew.>"] = []time.Time{ago(3 * day), ago(2 * day), ago(time.Hour)}
	s.messages["KUBEMOOT_COORDINATOR|kubemoot.coordinator.ns.crew.>"] = []time.Time{ago(5 * day)}
	s.messages["KUBEMOOT_REQUEST|kubemoot.request.ns.crew"] = []time.Time{ago(time.Minute)}
	s.messages["KUBEMOOT_DISCUSS|kubemoot.discuss.ns.other.>"] = []time.Time{ago(30 * day)}
	got, err := cleaner(s).Run(scope, Policy{Other: 36 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if got.Other != 3 {
		t.Errorf("removed %d, want 3", got.Other)
	}
	if len(s.messages["KUBEMOOT_DISCUSS|kubemoot.discuss.ns.other.>"]) != 1 {
		t.Error("another crew's messages were touched")
	}
	for _, k := range s.purged {
		if strings.Contains(k, "other") {
			t.Errorf("purged another crew's subject: %s", k)
		}
	}
}

func TestCleanKeepsGoingAfterAFailureAndReportsIt(t *testing.T) {
	s := newFakeStore()
	s.failKV[ConversationsBucket] = errors.New("unreachable")
	s.kv[MemoryBucket] = []KVEntry{{"ns.crew.t.old", ago(200 * day)}}
	got, err := cleaner(s).Run(scope, Policy{Conversations: 30 * day, Memory: Of(90 * day)})
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("the failure is reported: %v", err)
	}
	if got.Memory != 1 {
		t.Errorf("memory still cleaned, removed %d", got.Memory)
	}
}

// An owner record expires with the history of its conversation, by the same window, and
// only the crew's own records are touched.
func TestCleanRemovesExpiredOwnerRecordsOfTheCrewOnly(t *testing.T) {
	s := newFakeStore()
	s.kv[ConversationsBucket] = []KVEntry{
		{"own.ns.crew.old", ago(31 * day)},
		{"own.ns.crew.new", ago(day)},
		{"own.ns.other.old", ago(400 * day)},
		{"own.ns2.crew.old", ago(400 * day)},
	}
	got, err := cleaner(s).Run(scope, Policy{Conversations: 30 * day})
	if err != nil {
		t.Fatal(err)
	}
	if got.Conversations != 0 {
		t.Errorf("an owner record is not a conversation: counted %d", got.Conversations)
	}
	want := "own.ns.crew.new,own.ns.other.old,own.ns2.crew.old"
	if g := strings.Join(s.keys(ConversationsBucket), ","); g != want {
		t.Errorf("left %s, want %s", g, want)
	}
}
