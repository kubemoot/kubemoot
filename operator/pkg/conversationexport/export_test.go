/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package conversationexport

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory/ompi"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

const (
	convID      = "conv-1"
	thread1     = "th-1"
	thread2     = "th-2"
	credential  = "Authorization: Bearer abcdefgh12345678"
	zoneNewYork = "America/New_York"
)

var (
	testScope = crewscope.Scope{Namespace: "ns", Crew: "c"}
	answered  = time.Date(2026, 11, 20, 19, 3, 0, 0, time.UTC) // 14:03 in New York
	exportAt  = time.Date(2026, 11, 20, 20, 0, 0, 0, time.UTC)
)

// fakeSource holds conversations by id.
type fakeSource map[string]Held

func (f fakeSource) Held(_ context.Context, _ crewscope.Scope, id string) (Held, error) {
	if id == "boom" {
		return Held{}, errors.New("store down")
	}
	h, ok := f[id]
	if !ok {
		return Held{}, ErrNotHeld
	}
	return h, nil
}

func file(thread, name, body string) File {
	return File{Thread: thread, Name: name, Open: func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(body)), nil
	}}
}

// fullHeld is a conversation with every part present.
func fullHeld() Held {
	return Held{
		Turns: []Turn{
			{ThreadID: thread1, Question: "what is failing?", Answer: "nothing", AnsweredAt: answered},
			{ThreadID: thread2, Question: "and the GPUs?", Answer: "idle", AnsweredAt: answered.Add(time.Hour)},
		},
		Receipts: map[string][]byte{thread1: []byte(`{"tokens":5}`), thread2: []byte(`{"tokens":7}`)},
		Messages: map[string][]Message{thread1: mootMessages(thread1), thread2: mootMessages(thread2)},
		Files:    []File{file(thread1, "obs/result-1", "table")},
	}
}

type memReader map[string]string

func (m memReader) ListKVKeys(string) ([]string, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys, nil
}

func (m memReader) GetKVEntry(_, key string) (crewmemory.Entry, error) {
	return crewmemory.Entry{Value: []byte(m[key]), Revision: 2, Created: time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)}, nil
}

func fact(value, thread string) string {
	b, _ := json.Marshal(map[string]string{
		"value": value, "learnedBy": "obs", "learnedAt": "2026-11-20T18:00:00Z", "learnedInThread": thread,
	})
	return string(b)
}

func crewMemory() memReader {
	return memReader{
		"ns.c.hw.gpu":     fact("RTX 5090", thread1),
		"ns.c.hw.other":   fact("a fact from another conversation", "th-99"),
		"ns.c.hw.nothrd":  fact("a fact with no thread", ""),
		"ns.c.ops.login":  fact(credential, thread2),
		"other.c.hw.leak": fact("another crew", thread1),
	}
}

func builderWith(src fakeSource, mem crewmemory.EntryReader) *Builder {
	b := &Builder{
		Source: src, Now: func() time.Time { return exportAt },
		Allow: func(context.Context, crewscope.Scope, string) error { return nil },
	}
	if mem != nil {
		b.Memory = func() (crewmemory.EntryReader, error) { return mem, nil }
	}
	return b
}

// unzip returns the files of a zip by name.
func unzip(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(rc)
		_ = rc.Close()
		out[f.Name] = body
	}
	return out
}

func exportOne(t *testing.T, b *Builder, opts Options) (map[string][]byte, Manifest) {
	t.Helper()
	var buf bytes.Buffer
	m, err := b.WriteConversation(context.Background(), &buf, testScope, convID, opts)
	if err != nil {
		t.Fatal(err)
	}
	return unzip(t, buf.Bytes()), m
}

func part(m Manifest, path string) Part {
	for _, p := range m.Parts {
		if p.Path == path {
			return p
		}
	}
	return Part{}
}

func TestConversationZipHoldsEveryPartWhenAllAreStored(t *testing.T) {
	files, m := exportOne(t, builderWith(fakeSource{convID: fullHeld()}, crewMemory()), Options{})
	for _, name := range []string{
		"conversation.md", "conversation.json", "discussion.md", "receipts/th-1.json", "receipts/th-2.json",
		"artifacts/th-1/obs/result-1", "memory/manifest.json", "manifest.json",
	} {
		if _, ok := files[name]; !ok {
			t.Errorf("zip lacks %s; has %v", name, keys(files))
		}
	}
	if string(files["receipts/th-1.json"]) != `{"tokens":5}` || string(files["artifacts/th-1/obs/result-1"]) != "table" {
		t.Error("a receipt or an artifact was changed")
	}
	for _, p := range m.Parts {
		if p.Status != StatusIncluded {
			t.Errorf("part %s is %s, want included", p.Path, p.Status)
		}
	}
	assertManifest(t, m, files["manifest.json"])
}

func assertManifest(t *testing.T, m Manifest, onDisk []byte) {
	t.Helper()
	if m.Turns != 2 || m.ConversationID != convID || m.Crew != "c" || m.Namespace != "ns" || m.ExportedAt != "2026-11-20T20:00:00Z" {
		t.Errorf("manifest = %+v", m)
	}
	var fromFile Manifest
	if err := json.Unmarshal(onDisk, &fromFile); err != nil || fromFile.ConversationID != convID {
		t.Errorf("manifest.json = %s, err = %v", onDisk, err)
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestExpiredAndUnkeptPartsAreListedInTheManifest(t *testing.T) {
	held := Held{Turns: fullHeld().Turns} // no receipts, no files
	files, m := exportOne(t, builderWith(fakeSource{convID: held}, nil), Options{})
	if p := part(m, "receipts/"); p.Status != StatusNotKept || p.Reason == "" {
		t.Errorf("receipts part = %+v, want not-kept with a reason", p)
	}
	if p := part(m, "artifacts/"); p.Status != StatusExpired || !strings.Contains(p.Reason, "deleted when the thread closes") {
		t.Errorf("artifacts part = %+v, want expired", p)
	}
	if p := part(m, "memory/"); p.Status != StatusNotKept {
		t.Errorf("memory part = %+v, want not-kept when the installation offers none", p)
	}
	for name := range files {
		if strings.HasPrefix(name, "receipts/") || strings.HasPrefix(name, "artifacts/") || strings.HasPrefix(name, "memory/") {
			t.Errorf("zip holds %s for a part that is not kept", name)
		}
	}
}

func TestSomeReceiptsStoredAreIncludedAndTheGapIsSaid(t *testing.T) {
	held := fullHeld()
	delete(held.Receipts, thread2)
	_, m := exportOne(t, builderWith(fakeSource{convID: held}, nil), Options{})
	if p := part(m, "receipts/"); p.Status != StatusIncluded || p.Count != 1 || !strings.Contains(p.Reason, "1 of 2") {
		t.Errorf("receipts part = %+v", p)
	}
}

func TestMemoryHoldsOnlyFactsLearnedInThisConversation(t *testing.T) {
	files, m := exportOne(t, builderWith(fakeSource{convID: fullHeld()}, crewMemory()), Options{})
	var all strings.Builder
	for name, body := range files {
		if strings.HasPrefix(name, "memory/memories/") {
			all.Write(body)
		}
	}
	got := all.String()
	if !strings.Contains(got, "RTX 5090") {
		t.Fatalf("the fact learned in this conversation is missing: %s", got)
	}
	for _, leak := range []string{"another conversation", "no thread", "another crew"} {
		if strings.Contains(got, leak) {
			t.Errorf("memory holds %q, which this conversation did not teach the crew", leak)
		}
	}
	if p := part(m, "memory/"); p.Status != StatusIncluded || p.Count != 1 {
		t.Errorf("memory part = %+v", p)
	}
}

func TestMemoryPartPassesTheOMPIValidator(t *testing.T) {
	files, _ := exportOne(t, builderWith(fakeSource{convID: fullHeld()}, crewMemory()), Options{})
	if err := ompi.ValidateManifest(files["memory/manifest.json"]); err != nil {
		t.Errorf("memory/manifest.json: %v", err)
	}
	checked := 0
	for name, body := range files {
		if strings.HasPrefix(name, "memory/memories/") {
			checked++
			if err := ompi.ValidateMemory(body); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	}
	if checked == 0 {
		t.Error("no memory object was checked")
	}
}

func TestMemoryPartIsTheFilesTheMemoryExportWrites(t *testing.T) {
	// The same screened export the memory endpoint serves, for the one fact.
	scope := testScope
	res, _ := crewmemory.ReadFacts(crewMemory(), scope)
	res = crewmemory.LearnedInThreads(res, map[string]struct{}{thread1: {}, thread2: {}})
	exp, _ := ompi.Build(scope, res, ompi.Options{ExportedAt: exportAt})
	want, err := ompi.Files(exp, false)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := exportOne(t, builderWith(fakeSource{convID: fullHeld()}, crewMemory()), Options{})
	threaded := 0
	for name, body := range want {
		if !bytes.Equal(files["memory/"+name], body) {
			t.Errorf("memory/%s differs from the memory export's file", name)
		}
		if strings.HasPrefix(name, "memories/") && strings.Contains(string(body), `"learnedInThread": "`+thread1+`"`) {
			threaded++
		}
	}
	if threaded != 1 {
		t.Errorf("%d memory files name the thread %s, want 1", threaded, thread1)
	}
}

func TestACredentialShapedFactIsScreenedOutAndListedWithoutItsText(t *testing.T) {
	files, m := exportOne(t, builderWith(fakeSource{convID: fullHeld()}, crewMemory()), Options{})
	for name, body := range files {
		if strings.Contains(string(body), "abcdefgh12345678") {
			t.Errorf("%s holds the credential", name)
		}
	}
	if len(m.LeftOut) != 1 || !strings.Contains(m.LeftOut[0].Reason, "credential") {
		t.Fatalf("leftOut = %+v, want the credential fact listed with its reason", m.LeftOut)
	}
}

func TestAConversationWithNoFactOfItsOwnSaysMemoryIsEmpty(t *testing.T) {
	mem := memReader{"ns.c.hw.other": fact("elsewhere", "th-99")}
	_, m := exportOne(t, builderWith(fakeSource{convID: fullHeld()}, mem), Options{})
	if p := part(m, "memory/"); p.Status != StatusEmpty {
		t.Errorf("memory part = %+v, want empty", p)
	}
}

func TestAnUnreadableMemoryStoreIsListedNotMistakenForEmpty(t *testing.T) {
	b := builderWith(fakeSource{convID: fullHeld()}, nil)
	b.Memory = func() (crewmemory.EntryReader, error) { return nil, errors.New("down") }
	_, m := exportOne(t, b, Options{})
	if p := part(m, "memory/"); p.Status != StatusUnavailable {
		t.Errorf("memory part = %+v, want unavailable", p)
	}
}

func TestTimesAreInTheAskersZoneWhenKnown(t *testing.T) {
	files, m := exportOne(t, builderWith(fakeSource{convID: fullHeld()}, nil), Options{TimeZone: zoneNewYork})
	if m.TimeZone != "America/New_York" {
		t.Errorf("zone = %q", m.TimeZone)
	}
	if md := string(files["conversation.md"]); !strings.Contains(md, "answered 2026-11-20 14:03 EST") {
		t.Errorf("conversation.md has no time in the asker's zone:\n%s", md)
	}
	var js struct {
		Turns []struct{ AnsweredAt, AnsweredAtUTC string }
	}
	if err := json.Unmarshal(files["conversation.json"], &js); err != nil {
		t.Fatal(err)
	}
	if js.Turns[0].AnsweredAt != "2026-11-20T14:03:00-05:00" || js.Turns[0].AnsweredAtUTC != "2026-11-20T19:03:00Z" {
		t.Errorf("turn times = %+v", js.Turns[0])
	}
}

func TestAnUnknownZoneFallsBackToUTC(t *testing.T) {
	for _, zone := range []string{"", "Mars/Olympus", "../../etc/passwd"} {
		files, m := exportOne(t, builderWith(fakeSource{convID: fullHeld()}, nil), Options{TimeZone: zone})
		if m.TimeZone != "UTC" || !strings.Contains(string(files["conversation.md"]), "19:03 UTC") {
			t.Errorf("zone %q: manifest zone = %q", zone, m.TimeZone)
		}
	}
}

func TestConversationMarkdownReadsAsTurns(t *testing.T) {
	files, _ := exportOne(t, builderWith(fakeSource{convID: fullHeld()}, nil), Options{})
	md := string(files["conversation.md"])
	for _, want := range []string{"# Conversation conv-1", "## Turn 1", "what is failing?", "## Turn 2", "idle"} {
		if !strings.Contains(md, want) {
			t.Errorf("conversation.md lacks %q:\n%s", want, md)
		}
	}
}

func TestArtifactNamesCannotEscapeTheirFolder(t *testing.T) {
	held := fullHeld()
	held.Files = []File{file("../th", "../../evil/..", "x")}
	files, _ := exportOne(t, builderWith(fakeSource{convID: held}, nil), Options{})
	for name := range files {
		for _, element := range strings.Split(name, "/") {
			if element == ".." || element == "." || element == "" {
				t.Errorf("path %q has the element %q", name, element)
			}
		}
	}
}

func TestAnArtifactThatCannotBeOpenedIsLeftOutAndListed(t *testing.T) {
	held := fullHeld()
	held.Files = append(held.Files, File{Thread: thread1, Name: "obs/gone", Open: func(context.Context) (io.ReadCloser, error) {
		return nil, errors.New("object deleted")
	}})
	files, m := exportOne(t, builderWith(fakeSource{convID: held}, nil), Options{})
	if _, ok := files["artifacts/th-1/obs/result-1"]; !ok {
		t.Error("the readable artifact is missing")
	}
	found := false
	for _, l := range m.LeftOut {
		found = found || strings.HasSuffix(l.Key, "obs/gone")
	}
	if !found {
		t.Errorf("leftOut = %+v, want the unreadable artifact", m.LeftOut)
	}
}

func TestInvalidIDsAreRefusedBeforeAnyRead(t *testing.T) {
	b := builderWith(fakeSource{}, nil)
	for _, id := range []string{"", "a.b", "a/b", "a*", strings.Repeat("a", 129), "ns.c.x"} {
		var buf bytes.Buffer
		_, err := b.WriteConversation(context.Background(), &buf, testScope, id, Options{})
		if !errors.Is(err, ErrInvalidID) || buf.Len() != 0 {
			t.Errorf("id %.20q: err = %v, wrote %d bytes", id, err, buf.Len())
		}
	}
}

func TestARefusalWritesNothingAndReadsNothing(t *testing.T) {
	read := false
	b := builderWith(fakeSource{convID: fullHeld()}, nil)
	b.Source = sourceFunc(func() { read = true })
	b.Allow = func(context.Context, crewscope.Scope, string) error { return ErrRefused }
	var buf bytes.Buffer
	_, err := b.WriteConversation(context.Background(), &buf, testScope, convID, Options{})
	if !errors.Is(err, ErrRefused) || buf.Len() != 0 || read {
		t.Errorf("err = %v, wrote %d bytes, read the store = %v", err, buf.Len(), read)
	}
}

type sourceFunc func()

func (f sourceFunc) Held(context.Context, crewscope.Scope, string) (Held, error) {
	f()
	return Held{}, nil
}

func TestAConversationThatIsNotHeldIsNotHeldNotEmpty(t *testing.T) {
	var buf bytes.Buffer
	_, err := builderWith(fakeSource{}, nil).WriteConversation(context.Background(), &buf, testScope, "gone", Options{})
	if !errors.Is(err, ErrNotHeld) || buf.Len() != 0 {
		t.Errorf("err = %v, wrote %d bytes", err, buf.Len())
	}
}

func TestManifestReportMatchesTheZipsManifest(t *testing.T) {
	b := builderWith(fakeSource{convID: fullHeld()}, crewMemory())
	report, err := b.ConversationManifest(context.Background(), testScope, convID, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, m := exportOne(t, b, Options{})
	a, _ := json.Marshal(report)
	c, _ := json.Marshal(m)
	if !bytes.Equal(a, c) {
		t.Errorf("report %s\n zip   %s", a, c)
	}
}

func TestFileNames(t *testing.T) {
	if got := FileName(testScope, "abc"); got != "c-conversation-abc.zip" {
		t.Errorf("FileName = %q", got)
	}
	if got := BundleFileName(testScope); got != "c-conversations.zip" {
		t.Errorf("BundleFileName = %q", got)
	}
}

func TestAMemoryOpenerThatReturnsNoReaderIsUnavailable(t *testing.T) {
	b := builderWith(fakeSource{convID: fullHeld()}, nil)
	b.Memory = func() (crewmemory.EntryReader, error) { return nil, nil }
	_, m := exportOne(t, b, Options{})
	if p := part(m, "memory/"); p.Status != StatusUnavailable {
		t.Errorf("memory part = %+v, want unavailable", p)
	}
}

func TestABuilderWithNoAllowRefusesEveryConversation(t *testing.T) {
	b := builderWith(fakeSource{convID: fullHeld()}, nil)
	b.Allow = nil
	var buf bytes.Buffer
	if _, err := b.WriteConversation(context.Background(), &buf, testScope, convID, Options{}); !errors.Is(err, ErrRefused) || buf.Len() != 0 {
		t.Errorf("err = %v, wrote %d bytes, want a refusal", err, buf.Len())
	}
}

func TestTheReportCountsArtifactsWithoutOpeningThem(t *testing.T) {
	opened := 0
	held := fullHeld()
	held.Files = []File{{Thread: thread1, Name: "obs/x", Open: func(context.Context) (io.ReadCloser, error) {
		opened++
		return io.NopCloser(strings.NewReader("x")), nil
	}}}
	m, err := builderWith(fakeSource{convID: held}, nil).ConversationManifest(context.Background(), testScope, convID, Options{})
	if err != nil || opened != 0 || part(m, "artifacts/").Count != 1 {
		t.Errorf("err = %v, opened %d times, artifacts part = %+v", err, opened, part(m, "artifacts/"))
	}
}

func TestNamesThatCleanToTheSameEntryGetDistinctNames(t *testing.T) {
	held := fullHeld()
	held.Files = []File{file(thread1, "obs/a b", "one"), file(thread1, "obs/a_b", "two")}
	files, _ := exportOne(t, builderWith(fakeSource{convID: held}, nil), Options{})
	if string(files["artifacts/th-1/obs/a_b"]) == "" || string(files["artifacts/th-1/obs/a_b-2"]) == "" {
		t.Errorf("files = %v, want both artifacts under distinct names", keys(files))
	}
}
