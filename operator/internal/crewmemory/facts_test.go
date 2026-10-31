/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package crewmemory

import (
	"errors"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/internal/crewscope"
)

type fakeReader struct {
	values  map[string]string
	listErr error
	getErr  error
}

func (f fakeReader) ListKVKeys(string) ([]string, error) {
	keys := make([]string, 0, len(f.values))
	for k := range f.values {
		keys = append(keys, k)
	}
	return keys, f.listErr
}

func (f fakeReader) GetKVValue(_, key string) ([]byte, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return []byte(f.values[key]), nil
}

const (
	factKey   = "crew-a.pilot.t.k"
	validJSON = `{"value":"v"}`
)

var pilot = crewscope.Scope{Namespace: "crew-a", Crew: "pilot"}

func TestParseFactReadsEveryField(t *testing.T) {
	raw := `{"value":"gpu0 is a 5090","learnedBy":"gpu-analyst","learnedAt":"2026-06-13T10:00:00Z","usedAt":"2026-06-14T11:30:00Z"}`
	f, ok := ParseFact(pilot, "crew-a.pilot.hardware.gpu0", []byte(raw))
	if !ok {
		t.Fatal("ParseFact() = not ok, want ok")
	}
	if f.Topic != "hardware" || f.Key != "gpu0" || f.Value != "gpu0 is a 5090" || f.LearnedBy != "gpu-analyst" {
		t.Errorf("ParseFact() = %+v", f)
	}
	if !f.LearnedAt.Equal(time.Date(2026, 6, 13, 10, 0, 0, 0, time.UTC)) || f.UsedAt.Hour() != 11 {
		t.Errorf("times = %v / %v", f.LearnedAt, f.UsedAt)
	}
}

func TestParseFactUsedAtDefaultsToLearnedAt(t *testing.T) {
	f, ok := ParseFact(pilot, factKey, []byte(`{"value":"v","learnedAt":"2026-06-13T10:00:00Z"}`))
	if !ok || !f.UsedAt.Equal(f.LearnedAt) || f.UsedAt.IsZero() {
		t.Errorf("UsedAt = %v, LearnedAt = %v, ok = %v", f.UsedAt, f.LearnedAt, ok)
	}
}

func TestParseFactRejectsBadInput(t *testing.T) {
	cases := map[string]struct{ key, raw string }{
		"other crew":   {"crew-a.other.t.k", validJSON},
		"not json":     {factKey, `plain text`},
		"empty value":  {factKey, ``},
		"wrong prefix": {"pilot.t.k", validJSON},
		"no key":       {"crew-a.pilot.topic", validJSON},
		"empty key":    {"crew-a.pilot.topic.", validJSON},
		"empty topic":  {"crew-a.pilot..k", validJSON},
	}
	for name, c := range cases {
		if _, ok := ParseFact(pilot, c.key, []byte(c.raw)); ok {
			t.Errorf("%s: ParseFact() = ok, want rejected", name)
		}
	}
}

func TestParseFactBadTimeIsZero(t *testing.T) {
	f, ok := ParseFact(pilot, factKey, []byte(`{"value":"v","learnedAt":"yesterday"}`))
	if !ok || !f.LearnedAt.IsZero() {
		t.Errorf("LearnedAt = %v, ok = %v, want zero", f.LearnedAt, ok)
	}
}

func TestReadFactsFiltersSortsAndSkipsBadEntries(t *testing.T) {
	r := fakeReader{values: map[string]string{
		"crew-a.pilot.b.two":   `{"value":"2"}`,
		"crew-a.pilot.a.one":   `{"value":"1"}`,
		"crew-a.pilot.a.bad":   `not json`,
		"crew-a.other.a.three": `{"value":"3"}`,
		"crew-b.pilot.a.four":  `{"value":"4"}`,
	}}
	facts, err := ReadFacts(r, pilot)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 || facts[0].Key != "one" || facts[1].Key != "two" {
		t.Errorf("ReadFacts() = %+v", facts)
	}
}

func TestReadFactsPropagatesStoreErrors(t *testing.T) {
	boom := errors.New("boom")
	if _, err := ReadFacts(fakeReader{listErr: boom}, pilot); !errors.Is(err, boom) {
		t.Errorf("list error = %v", err)
	}
	r := fakeReader{values: map[string]string{factKey: `{}`}, getErr: boom}
	if _, err := ReadFacts(r, pilot); !errors.Is(err, boom) {
		t.Errorf("get error = %v", err)
	}
}

func TestReadFactsEmptyBucket(t *testing.T) {
	facts, err := ReadFacts(fakeReader{}, pilot)
	if err != nil || len(facts) != 0 {
		t.Errorf("ReadFacts() = %v, %v", facts, err)
	}
}
