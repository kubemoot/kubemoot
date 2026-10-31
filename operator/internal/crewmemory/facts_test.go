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

func (f fakeReader) GetKVEntry(_, key string) (Entry, error) {
	if f.getErr != nil {
		return Entry{}, f.getErr
	}
	return Entry{Value: []byte(f.values[key]), Revision: 7, Created: time.Date(2026, 6, 15, 8, 0, 0, 0, time.UTC)}, nil
}

const (
	factKey   = "crew-a.pilot.t.k"
	validJSON = `{"value":"v"}`
)

var pilot = crewscope.Scope{Namespace: "crew-a", Crew: "pilot"}

func entry(raw string) Entry { return Entry{Value: []byte(raw)} }

func TestParseFactReadsEveryField(t *testing.T) {
	raw := `{"value":"gpu0 is a 5090","learnedBy":"gpu-analyst","learnedAt":"2026-06-13T10:00:00Z","usedAt":"2026-06-14T11:30:00Z"}`
	e := Entry{Value: []byte(raw), Revision: 42, Created: time.Date(2026, 6, 14, 11, 30, 0, 0, time.UTC)}
	f, err := ParseFact(pilot, "crew-a.pilot.hardware.gpu0", e)
	if err != nil {
		t.Fatal(err)
	}
	if f.Topic != "hardware" || f.Key != "gpu0" || f.Value != "gpu0 is a 5090" || f.LearnedBy != "gpu-analyst" {
		t.Errorf("ParseFact() = %+v", f)
	}
	if !f.LearnedAt.Equal(time.Date(2026, 6, 13, 10, 0, 0, 0, time.UTC)) || f.UsedAt.Hour() != 11 {
		t.Errorf("times = %v / %v", f.LearnedAt, f.UsedAt)
	}
	if f.Revision != 42 || !f.Recorded.Equal(e.Created) {
		t.Errorf("revision = %d, recorded = %v", f.Revision, f.Recorded)
	}
}

func TestParseFactUsedAtDefaultsToLearnedAt(t *testing.T) {
	f, err := ParseFact(pilot, factKey, entry(`{"value":"v","learnedAt":"2026-06-13T10:00:00Z"}`))
	if err != nil || !f.UsedAt.Equal(f.LearnedAt) || f.UsedAt.IsZero() {
		t.Errorf("UsedAt = %v, LearnedAt = %v, err = %v", f.UsedAt, f.LearnedAt, err)
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
		if _, err := ParseFact(pilot, c.key, entry(c.raw)); err == nil {
			t.Errorf("%s: ParseFact() = nil error, want rejected", name)
		}
	}
}

func TestParseFactOtherCrewIsOutOfScope(t *testing.T) {
	if _, err := ParseFact(pilot, "crew-a.other.t.k", entry(validJSON)); !errors.Is(err, ErrOutOfScope) {
		t.Errorf("err = %v, want ErrOutOfScope", err)
	}
}

func TestParseFactBadTimeIsZero(t *testing.T) {
	f, err := ParseFact(pilot, factKey, entry(`{"value":"v","learnedAt":"yesterday"}`))
	if err != nil || !f.LearnedAt.IsZero() {
		t.Errorf("LearnedAt = %v, err = %v, want zero", f.LearnedAt, err)
	}
}

func TestReadFactsFiltersSortsAndReportsBadEntries(t *testing.T) {
	r := fakeReader{values: map[string]string{
		"crew-a.pilot.b.two":   `{"value":"2"}`,
		"crew-a.pilot.a.one":   `{"value":"1"}`,
		"crew-a.pilot.a.bad":   `not json`,
		"crew-a.other.a.three": `{"value":"3"}`,
		"crew-b.pilot.a.four":  `{"value":"4"}`,
	}}
	res, err := ReadFacts(r, pilot)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Facts) != 2 || res.Facts[0].Key != "one" || res.Facts[1].Key != "two" {
		t.Errorf("Facts = %+v", res.Facts)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Key != "crew-a.pilot.a.bad" {
		t.Errorf("Skipped = %+v, want only the unparseable entry", res.Skipped)
	}
	if res.Facts[0].Revision != 7 {
		t.Errorf("revision not carried: %+v", res.Facts[0])
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
	res, err := ReadFacts(fakeReader{}, pilot)
	if err != nil || len(res.Facts) != 0 || len(res.Skipped) != 0 {
		t.Errorf("ReadFacts() = %v, %v", res, err)
	}
}
