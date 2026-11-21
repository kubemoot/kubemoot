/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package conversationexport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

const (
	theirs = "theirs"
	agedID = "aged"
)

func writeBundle(t *testing.T, b *Builder, ids ...string) (map[string][]byte, BundleManifest) {
	t.Helper()
	var buf bytes.Buffer
	bm, err := b.WriteBundle(context.Background(), &buf, testScope, ids, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return unzip(t, buf.Bytes()), bm
}

func ids(es []Entry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.ConversationID)
	}
	return out
}

func TestBundleListsIncludedRefusedExpiredAndFailed(t *testing.T) {
	b := builderWith(fakeSource{"mine": fullHeld(), theirs: fullHeld()}, nil)
	b.Allow = func(_ context.Context, _ crewscope.Scope, id string) error {
		if id == theirs {
			return ErrRefused
		}
		return nil
	}
	files, bm := writeBundle(t, b, "mine", theirs, agedID, "bad.id", "boom")
	want := map[string][]string{
		"included": {"mine"}, "refused": {theirs, "bad.id"}, agedID: {agedID}, "failed": {"boom"},
	}
	got := map[string][]string{
		"included": ids(bm.Included), "refused": ids(bm.Refused), agedID: ids(bm.Expired), "failed": ids(bm.Failed),
	}
	for list, w := range want {
		if strings.Join(got[list], ",") != strings.Join(w, ",") {
			t.Errorf("%s = %v, want %v", list, got[list], w)
		}
	}
	var onDisk BundleManifest
	if err := json.Unmarshal(files["manifest.json"], &onDisk); err != nil || len(onDisk.Included)+len(onDisk.Refused)+len(onDisk.Expired)+len(onDisk.Failed) != 5 {
		t.Errorf("manifest.json = %s, err = %v: every id asked for must be listed", files["manifest.json"], err)
	}
	for _, e := range append(bm.Refused, bm.Expired...) {
		if _, ok := files[FileName(testScope, e.ConversationID)]; ok {
			t.Errorf("a zip of %s is in the bundle", e.ConversationID)
		}
	}
}

func TestBundleHoldsEachConversationZipUnchanged(t *testing.T) {
	b := builderWith(fakeSource{"one": fullHeld(), "two": fullHeld()}, crewMemory())
	files, bm := writeBundle(t, b, "one", "two")
	if len(bm.Included) != 2 {
		t.Fatalf("included = %+v", bm.Included)
	}
	for _, e := range bm.Included {
		var alone bytes.Buffer
		if _, err := b.WriteConversation(context.Background(), &alone, testScope, e.ConversationID, Options{}); err != nil {
			t.Fatal(err)
		}
		inner := unzip(t, files[e.File])
		want := unzip(t, alone.Bytes())
		if len(inner) != len(want) {
			t.Errorf("%s: inner zip has %d files, standalone %d", e.File, len(inner), len(want))
		}
		for name, body := range want {
			if name == "manifest.json" {
				continue // names the conversation, which differs per id
			}
			if string(inner[name]) != string(body) {
				t.Errorf("%s: %s differs from the standalone zip", e.File, name)
			}
		}
		var m Manifest
		if err := json.Unmarshal(inner["manifest.json"], &m); err != nil || m.ConversationID != e.ConversationID {
			t.Errorf("%s: its manifest names %q", e.File, m.ConversationID)
		}
	}
}

func TestBundleAsksEachDuplicateOnce(t *testing.T) {
	_, bm := writeBundle(t, builderWith(fakeSource{"one": fullHeld()}, nil), "one", "one", "one")
	if len(bm.Included) != 1 {
		t.Errorf("included = %+v, want one", bm.Included)
	}
}

func TestBundleRefusesMoreThanTheLimit(t *testing.T) {
	var all []string
	for i := 0; i <= MaxBundle; i++ {
		all = append(all, "id")
	}
	var buf bytes.Buffer
	if _, err := builderWith(fakeSource{}, nil).WriteBundle(context.Background(), &buf, testScope, all, Options{}); !errors.Is(err, ErrTooMany) || buf.Len() != 0 {
		t.Errorf("err = %v, wrote %d bytes", err, buf.Len())
	}
}

func TestBundleOfOnlyRefusedIdsIsStillAZipWithItsManifest(t *testing.T) {
	files, bm := writeBundle(t, builderWith(fakeSource{}, nil), "a.b", "c.d")
	if len(bm.Refused) != 2 || len(files) != 1 || !strings.Contains(string(files["manifest.json"]), "refused") {
		t.Errorf("refused = %+v, files = %v", bm.Refused, keys(files))
	}
}

func TestBundleReadsTheCrewMemoryOnce(t *testing.T) {
	opened := 0
	b := builderWith(fakeSource{"a": fullHeld(), "b": fullHeld(), "c": fullHeld()}, nil)
	b.Memory = func() (crewmemory.EntryReader, error) { opened++; return crewMemory(), nil }
	writeBundle(t, b, "a", "b", "c")
	if opened != 1 {
		t.Errorf("the memory store was opened %d times for three conversations, want once", opened)
	}
}

func TestBundleManifestMatchesTheBundleWithoutBuildingIt(t *testing.T) {
	b := builderWith(fakeSource{"a": fullHeld()}, nil)
	report, err := b.BundleManifest(context.Background(), testScope, []string{"a", "gone"}, Options{})
	if err != nil || len(report.Included) != 1 || len(report.Expired) != 1 {
		t.Errorf("report = %+v, err = %v", report, err)
	}
}
