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
	"errors"
	"net/textproto"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/fitness-runner/internal/evidencenames"
)

type fakeEvidenceStore struct {
	infos   []*nats.ObjectInfo
	bodies  map[string]string
	listErr error
	getErr  error
}

func (f *fakeEvidenceStore) List(...nats.ListObjectsOpt) ([]*nats.ObjectInfo, error) {
	return f.infos, f.listErr
}

func (f *fakeEvidenceStore) GetBytes(name string, _ ...nats.GetObjectOpt) ([]byte, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return []byte(f.bodies[name]), nil
}

// obj is a stored tool call with canonical-case header names (Gen_ai.operation.name,
// Kubemoot.truncated), the form a header read through the Go client's MIME
// reader takes for the lower-case names the runtime writes. A plain map lookup of
// the runtime's names finds none of them.
func obj(name string, hdr ...string) *nats.ObjectInfo {
	return objWith(textproto.CanonicalMIMEHeaderKey, name, hdr...)
}

// objAsWritten is a stored tool call whose header names are as the runtime wrote them.
func objAsWritten(name string, hdr ...string) *nats.ObjectInfo {
	return objWith(func(k string) string { return k }, name, hdr...)
}

func objWith(key func(string) string, name string, hdr ...string) *nats.ObjectInfo {
	h := nats.Header{}
	h[key("gen_ai.operation.name")] = []string{"execute_tool"}
	for i := 0; i+1 < len(hdr); i += 2 {
		h[key(hdr[i])] = []string{hdr[i+1]}
	}
	return &nats.ObjectInfo{ObjectMeta: nats.ObjectMeta{Name: name, Headers: h}}
}

var noWait = stragglerWait{}

const (
	evCallKey = "k8s-nodes/001-resources_list"
	evFinding = "finding"
	evNS      = "ns"
	evThread  = "t1"
	evNodes   = "ns/crew/t1/k8s-nodes/001-resources_list"
	evSignal  = "ns/crew/t1/k8s-nodes/agree-uuid1"
	evOther   = "ns/crew/t2/k8s-nodes/001-resources_list"
	evOtherN  = "other/crew/t1/k8s-nodes/001-resources_list"
	evErr     = "ns/crew/t1/k8s-nodes/002-events_list"
	nodeBody  = "NAME\nhomelab-k8s-1-cp Ready\nhomelab-k8s-1-gpu-worker Ready"
)

func store(infos []*nats.ObjectInfo, bodies map[string]string) *fakeEvidenceStore {
	return &fakeEvidenceStore{infos: infos, bodies: bodies}
}

func TestReadEvidenceReadsOnlyThisThreadsCalls(t *testing.T) {
	notACall := obj("ns/crew/t1/k8s-nodes/003-x")
	delete(notACall.Headers, "Gen_ai.operation.name")
	st := store([]*nats.ObjectInfo{obj(evNodes), obj(evSignal), obj(evOther), obj(evOtherN), notACall},
		map[string]string{
			evNodes: nodeBody, evSignal: "signal-only-1", evOther: "other-thread-1",
			evOtherN: "other-ns-1", "ns/crew/t1/k8s-nodes/003-x": "not-a-call-1",
		})
	set, calls, res := readEvidence([]evidenceStore{st}, evNS, evThread, nil, inlineEvidence{}, noWait)
	if res != readComplete || calls != 1 || !set.Has("homelab-k8s-1-cp") {
		t.Fatalf("res=%v calls=%d", res, calls)
	}
	for _, bad := range []string{"signal-only-1", "other-thread-1", "other-ns-1", "not-a-call-1"} {
		if set.Has(bad) {
			t.Errorf("%q must not be evidence", bad)
		}
	}
}

func TestReadEvidenceCountsWhatTheRunWasToldAndAsked(t *testing.T) {
	st := store([]*nats.ObjectInfo{
		obj(evNodes, "gen_ai.tool.call.arguments", "utf8-pct:%7B%22name%22%3A%22foo-9%22%7D"),
		obj(evErr, "kubemoot.error", "true", "error.type", "tool_error"),
	}, map[string]string{evNodes: nodeBody, evErr: `pod "web-7" not found in namespace "staging-1"`})
	set, calls, res := readEvidence([]evidenceStore{st}, evNS, evThread, nil, inlineEvidence{}, noWait)
	if res != readComplete || calls != 2 {
		t.Fatalf("res=%v calls=%d", res, calls)
	}
	for _, tok := range []string{"foo-9", "web-7", "staging-1"} {
		if !set.Has(tok) {
			t.Errorf("%q was asked for or reported to the run", tok)
		}
	}
}

func TestReadEvidenceNotComplete(t *testing.T) {
	good := store([]*nats.ObjectInfo{obj(evNodes)}, map[string]string{evNodes: nodeBody})
	cases := map[string]struct {
		stores   []evidenceStore
		expected map[string]bool
	}{
		"no calls":     {[]evidenceStore{store(nil, nil)}, nil},
		"list fails":   {[]evidenceStore{&fakeEvidenceStore{listErr: errors.New("boom")}}, nil},
		"read fails":   {[]evidenceStore{&fakeEvidenceStore{infos: []*nats.ObjectInfo{obj(evNodes)}, getErr: errors.New("gone")}}, nil},
		"none found":   {[]evidenceStore{&fakeEvidenceStore{listErr: nats.ErrNoObjectsFound}}, nil},
		"current errs": {[]evidenceStore{&fakeEvidenceStore{listErr: errors.New("timeout")}, good}, nil},
		"legacy errs":  {[]evidenceStore{good, &fakeEvidenceStore{listErr: errors.New("timeout")}}, nil},
		"cut result": {[]evidenceStore{store([]*nats.ObjectInfo{obj(evNodes, "kubemoot.truncated", "true")},
			map[string]string{evNodes: nodeBody})}, nil},
		"call still missing": {[]evidenceStore{good}, map[string]bool{evCallKey: true, "k8s-nodes/002-events_list": true}},
	}
	for name, tc := range cases {
		if set, _, res := readEvidence(tc.stores, evNS, evThread, tc.expected, inlineEvidence{}, noWait); res == readComplete || set != nil {
			t.Errorf("%s: want not complete", name)
		}
	}
}

func TestReadEvidenceNamedCallsAllStored(t *testing.T) {
	st := store([]*nats.ObjectInfo{obj(evNodes)}, map[string]string{evNodes: nodeBody})
	_, _, res := readEvidence([]evidenceStore{st}, evNS, evThread, map[string]bool{evCallKey: true}, inlineEvidence{}, noWait)
	if res != readComplete {
		t.Error("every named call is stored, want complete")
	}
}

func TestReadEvidenceReadsJSONAsJSON(t *testing.T) {
	st := store([]*nats.ObjectInfo{obj(evNodes, "Content-Type", "application/json")},
		map[string]string{evNodes: `{"items":[{"name":"node-01\nnginx-1"}]}`})
	set, _, res := readEvidence([]evidenceStore{st}, evNS, evThread, nil, inlineEvidence{}, noWait)
	if res != readComplete || !set.Has("node-01") || !set.Has("nginx-1") {
		t.Errorf("JSON escapes must separate words: res=%v", res)
	}
}

func TestExpectedCallKeys(t *testing.T) {
	ev := []SignalEvent{
		{Type: evFinding, Content: "[WORKSPACE agent=a calls=2]\n[CALL key=k8s-nodes/001-resources_list tool=x args={}]\n[CALL key=k8s-nodes/002-events_list tool=y]\nread k8s-nodes/*-<tool>"},
		{Type: "activity"},
	}
	got := expectedCallKeys(ev)
	if len(got) != 2 || !got[evCallKey] || !got["k8s-nodes/002-events_list"] {
		t.Errorf("keys = %v", got)
	}
}

func TestBucketMissing(t *testing.T) {
	for _, err := range []error{nats.ErrBucketNotFound, nats.ErrStreamNotFound, errors.Join(errors.New("x"), nats.ErrBucketNotFound)} {
		if !bucketMissing(err) {
			t.Errorf("%v is a missing bucket", err)
		}
	}
	for _, err := range []error{errors.New("timeout"), nats.ErrTimeout, nats.ErrConnectionClosed} {
		if bucketMissing(err) {
			t.Errorf("%v is not a missing bucket: the other bucket cannot stand in", err)
		}
	}
}

// TestDecodeHeaderSharedFixture runs the cases the agent runtime (Java) and
// artifact-access (Go) run, so the three decoders cannot drift apart.
func TestDecodeHeaderSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../test/fixtures/header-values.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		RoundTrip  []struct{ Name, Value, Encoded string }
		DecodeOnly []struct{ Name, Encoded, Value string }
	}
	if err := json.Unmarshal(raw, &f); err != nil || len(f.RoundTrip) == 0 || len(f.DecodeOnly) == 0 {
		t.Fatalf("fixture: %v", err)
	}
	for _, c := range f.RoundTrip {
		if got := decodeHeader(c.Encoded); got != c.Value {
			t.Errorf("%s: decodeHeader(%q) = %q, want %q", c.Name, c.Encoded, got, c.Value)
		}
	}
	for _, c := range f.DecodeOnly {
		if got := decodeHeader(c.Encoded); got != c.Value {
			t.Errorf("%s: decodeHeader(%q) = %q, want %q", c.Name, c.Encoded, got, c.Value)
		}
	}
}

// A value that is not validly encoded is returned as it is: invalid UTF-8 and
// control bytes are not decoded.
func TestDecodeHeaderRejectsInvalid(t *testing.T) {
	for _, in := range []string{"utf8-pct:%zz", "utf8-pct:%C3", "utf8-pct:%FF%FE", "utf8-pct:a%2"} {
		if got := decodeHeader(in); got != in {
			t.Errorf("decodeHeader(%q) = %q, want it returned as it is", in, got)
		}
	}
}

// The Go client canonicalizes header names on read (Gen_ai.operation.name), so a
// plain map lookup of the runtime's lower-case name finds nothing.
func TestHeadersAreFoundInCanonicalCase(t *testing.T) {
	info := obj(evNodes, "gen_ai.tool.call.arguments", `{"name":"web-7"}`,
		"Content-Type", "application/json", "kubemoot.truncated", "false")
	if _, plain := info.Headers["gen_ai.operation.name"]; plain {
		t.Fatal("the fixture must hold canonical-case names")
	}
	if !inThread(info, evNS, evThread) {
		t.Error("inThread must find the canonical-case operation header")
	}
	if !inThread(objAsWritten(evNodes), evNS, evThread) {
		t.Error("inThread must also find the names as the runtime wrote them")
	}
	st := store([]*nats.ObjectInfo{info}, map[string]string{evNodes: `{"items":[{"name":"node-01\nnginx-1"}]}`})
	set, _, res := readEvidence([]evidenceStore{st}, evNS, evThread, nil, inlineEvidence{}, noWait)
	if res != readComplete || !set.Has("web-7") || !set.Has("nginx-1") {
		t.Errorf("arguments header and JSON content type must be read: res=%v", res)
	}
	cut := obj(evNodes, "kubemoot.truncated", "true")
	if _, _, res := readEvidence([]evidenceStore{store([]*nats.ObjectInfo{cut}, map[string]string{evNodes: "x-1"})},
		evNS, evThread, nil, inlineEvidence{}, noWait); res == readComplete {
		t.Error("a result marked truncated in canonical case must skip the check")
	}
}

// lateStore lists the call only from its second list on, as a call that lands
// while the runner is already reading.
type lateStore struct {
	fakeEvidenceStore
	lists, landsAt int
}

func (l *lateStore) List(...nats.ListObjectsOpt) ([]*nats.ObjectInfo, error) {
	l.lists++
	if l.lists < l.landsAt {
		return nil, nats.ErrNoObjectsFound
	}
	return l.infos, nil
}

func TestReadEvidenceWaitsForNamedCalls(t *testing.T) {
	expected := map[string]bool{evCallKey: true}
	wait := stragglerWait{deadline: 5 * time.Second, interval: time.Millisecond}
	late := &lateStore{fakeEvidenceStore: *store([]*nats.ObjectInfo{obj(evNodes)}, map[string]string{evNodes: nodeBody}), landsAt: 3}
	set, calls, res := readEvidence([]evidenceStore{late}, evNS, evThread, expected, inlineEvidence{}, wait)
	if res != readComplete || calls != 1 || !set.Has("homelab-k8s-1-cp") || late.lists != 3 {
		t.Errorf("a call that lands late is read: res=%v calls=%d lists=%d", res, calls, late.lists)
	}
	never := &lateStore{fakeEvidenceStore: *store([]*nats.ObjectInfo{obj(evNodes)}, map[string]string{evNodes: nodeBody}), landsAt: 1 << 30}
	short := stragglerWait{deadline: 20 * time.Millisecond, interval: time.Millisecond}
	if _, _, res := readEvidence([]evidenceStore{never}, evNS, evThread, expected, inlineEvidence{}, short); res != readTimedOut {
		t.Error("a call that never lands must end as not checked at the deadline")
	}
}

func TestExpectedCallKeysOnlyOfTheAnsweringThread(t *testing.T) {
	ev := []SignalEvent{
		{Type: eventThreadFound, ThreadID: "abandoned"},
		{Type: evFinding, Content: "[CALL key=old-agent/001-x tool=x]"},
		{Type: eventThreadFound, ThreadID: "answer-thread"},
		{Type: evFinding, Content: "[CALL key=k8s-nodes/001-resources_list tool=x]"},
	}
	got := expectedCallKeys(ev)
	if len(got) != 1 || !got[evCallKey] {
		t.Errorf("keys = %v, want only the answering thread's", got)
	}
	if got := expectedCallKeys([]SignalEvent{{Type: evFinding, Content: "[CALL key=a/001-x tool=x]"}}); !got["a/001-x"] {
		t.Error("a stream with no thread_found is read whole")
	}
}

// Real runs of suite gpu-check-20261115b: the answers named nodes that no tool result held.
func realRun(question, answer string) *RunOutcome {
	return &RunOutcome{
		ThreadID: evThread, Question: question,
		Events: []SignalEvent{
			{Type: evFinding, Agent: "k8s-nodes", Content: "[CALL key=k8s-nodes/001-resources_list tool=resources_list]"},
			{Type: "synthesis", Content: answer},
			{Type: "done"},
		},
	}
}

func TestCheckInventedNamesRealCases(t *testing.T) {
	st := store([]*nats.ObjectInfo{obj(evNodes)}, map[string]string{evNodes: nodeBody})
	q := "The recent pod restarts were caused by the node running out of disk, tell me which node."
	cases := []struct {
		name, answer string
		want         []string
	}{
		{"node3", "Restarts came from **node3** (see `k8s-nodes/001-resources_list`); DiskPressure True.", []string{"node3"}},
		{"node-12345", "Triggered by **node-12345** running out of disk.", []string{"node-12345"}},
		{"node-01 list", "13 nodes:\n- node-01\n- node-02\n- homelab-k8s-1-cp", []string{"node-01", "node-02"}},
		{"real names", "- homelab-k8s-1-cp\n- homelab-k8s-1-gpu-worker", nil},
	}
	for _, tc := range cases {
		got := checkInventedNames([]evidenceStore{st}, evNS, realRun(q, tc.answer), noWait)
		if !got.Captured || got.Calls != 1 || !slices.Equal(got.InventedNames, tc.want) {
			t.Errorf("%s: got %+v, want names %v", tc.name, got, tc.want)
		}
	}
}

func TestCheckInventedNamesNotCheckedWithoutBasis(t *testing.T) {
	st := store([]*nats.ObjectInfo{obj(evNodes)}, map[string]string{evNodes: nodeBody})
	noAnswer := realRun("q", "")
	noThread := realRun("q", "**node3**")
	noThread.ThreadID = ""
	cases := map[string]struct {
		ns  string
		out *RunOutcome
	}{"no answer": {evNS, noAnswer}, "no thread": {evNS, noThread}, "no namespace": {"", realRun("q", "**node3**")}}
	for name, tc := range cases {
		if got := checkInventedNames([]evidenceStore{st}, tc.ns, tc.out, noWait); got.Captured || got.InventedNames != nil {
			t.Errorf("%s: want not checked, got %+v", name, got)
		}
	}
}

func TestCheckInventedNamesNamesFromTheQuestionAndAssertions(t *testing.T) {
	st := store([]*nats.ObjectInfo{obj(evNodes)}, map[string]string{evNodes: nodeBody})
	out := realRun("Is node-7 ready?", "**node-7** and **node-9** and **vllm-rig0**")
	out.Assertions = []AssertionResult{{Raw: `DEFER synthesis REFLECTS "mentions node-9"`}}
	out.Events[0].GPU = "vllm-rig0"
	got := checkInventedNames([]evidenceStore{st}, evNS, out, noWait)
	if !got.Captured || len(got.InventedNames) != 0 {
		t.Errorf("question, reference and crew names are allowed: %+v", got)
	}
}

func TestStragglerTimeoutIsRecordedInTheTranscript(t *testing.T) {
	st := store([]*nats.ObjectInfo{obj(evNodes)}, map[string]string{evNodes: nodeBody})
	out := realRun("q", "**node3**")
	out.Events[0].Content += "\n[CALL key=k8s-nodes/009-missing tool=x]"
	got := checkInventedNames([]evidenceStore{st}, evNS, out, stragglerWait{deadline: 10 * time.Millisecond, interval: time.Millisecond})
	if got.Captured || !got.StragglerTimeout || got.InventedNames != nil {
		t.Errorf("a call that never lands: want not checked and StragglerTimeout, got %+v", got)
	}
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"evidenceStragglerTimeout":true`) {
		t.Errorf("the transcript must carry the flag: %s", b)
	}
	none := realRun("q", "**node3**")
	none.Events[0].Content = ""
	if skipped := checkInventedNames(nil, evNS, none, noWait); skipped.StragglerTimeout {
		t.Error("a run that is not checked for another reason is not a timeout")
	}
}

const (
	evInvented = "node-12345"
	evDone     = "done"
)

// unstoredFixture is the shared fixture of the manifests an agent that stores no results posts;
// agent-runtime's ToolCallLedgerUnstoredTest builds the same manifests from the calls.
type unstoredFixture struct {
	Cases []struct {
		Name        string   `json:"name"`
		Manifest    string   `json:"manifest"`
		ListedCalls int      `json:"listedCalls"`
		Incomplete  bool     `json:"incomplete"`
		Evidence    []string `json:"evidence"`
	} `json:"cases"`
}

func loadUnstoredFixture(t *testing.T) unstoredFixture {
	t.Helper()
	raw, err := os.ReadFile("../test/fixtures/unstored-manifests.json")
	if err != nil {
		t.Fatal(err)
	}
	var f unstoredFixture
	if err := json.Unmarshal(raw, &f); err != nil || len(f.Cases) == 0 {
		t.Fatalf("fixture: %v (%d cases)", err, len(f.Cases))
	}
	return f
}

// advisorFinding is a read-only advisor's finding: its own words and the manifest of its calls.
func advisorFinding(t *testing.T, name string) string {
	t.Helper()
	for _, c := range loadUnstoredFixture(t).Cases {
		if c.Name == name {
			return "Checked the nodes.\n" + c.Manifest
		}
	}
	t.Fatalf("no fixture case %q", name)
	return ""
}

func TestUnstoredCallLinesAreNotExpectedStoredCalls(t *testing.T) {
	for _, c := range loadUnstoredFixture(t).Cases {
		ev := []SignalEvent{{Type: evFinding, Content: c.Manifest}}
		if got := expectedCallKeys(ev); len(got) != 0 {
			t.Errorf("%s: a call line with no key names no stored object, got %v", c.Name, got)
		}
	}
}

func TestInlineEvidenceOfTheSharedManifests(t *testing.T) {
	for _, c := range loadUnstoredFixture(t).Cases {
		got := inlineEvidenceOf([]SignalEvent{{Type: evFinding, Content: "Own words.\n" + c.Manifest}})

		if got.incomplete != c.Incomplete {
			t.Errorf("%s: incomplete = %v, want %v", c.Name, got.incomplete, c.Incomplete)
		}
		if c.ListedCalls >= 0 && got.calls != c.ListedCalls || c.ListedCalls < 0 && got.calls == 0 {
			t.Errorf("%s: calls = %d, want %d", c.Name, got.calls, c.ListedCalls)
		}
		checkInlineTexts(t, c.Name, got, c.Evidence)
	}
}

// checkInlineTexts asserts the named tokens are in the inline evidence and the agent's own words are not.
func checkInlineTexts(t *testing.T, name string, got inlineEvidence, want []string) {
	t.Helper()
	set := evidencenames.NewEvidence()
	for _, text := range got.texts {
		set.Add(text)
	}
	for _, tok := range want {
		if !set.Has(tok) {
			t.Errorf("%s: %q should be evidence", name, tok)
		}
	}
	if set.Has("own") {
		t.Errorf("%s: the agent's own words are not evidence", name)
	}
}

func TestNotStoredLineOfAWorkspaceReadToolIsNotALostResult(t *testing.T) {
	line := func(tool string) string {
		return "[NOT STORED tool=" + tool + " args={} error=false - the agent does not store results and this one is not shown here, so it is not available]"
	}
	for tool, lost := range map[string]bool{
		"workspace_grep": false, "workspace_query": false, "artifact_select": false, "workspace_list": false,
		"pods_list": true, "events_list": true,
	} {
		if got := resultLost(line(tool)); got != lost {
			t.Errorf("%s: lost = %v, want %v", tool, got, lost)
		}
	}
	writeFailed := "[NOT STORED tool=pods_list args={} error=false - the result could not be written to the workspace and is not available]"
	if !resultLost(writeFailed) {
		t.Error("a write that failed leaves the result nowhere")
	}
	if resultLost("[CALL tool=x args={} stored=no]\n[RESULT]\na\n[END RESULT]") {
		t.Error("a posted result is not lost")
	}
}

func TestCheckInventedNamesFromAReadOnlyAdvisorOnly(t *testing.T) {
	out := &RunOutcome{ThreadID: evThread, Question: "which node is down?", Events: []SignalEvent{
		{Type: evFinding, Agent: "obs", Content: advisorFinding(t, "a result that fits")},
		{Type: sseSynthesis, Content: "**node-a** is NotReady and **" + evInvented + "** too."},
		{Type: evDone},
	}}

	got := checkInventedNames([]evidenceStore{store(nil, nil)}, evNS, out, noWait)

	if !got.Captured || got.Calls != 1 || !slices.Equal(got.InventedNames, []string{evInvented}) {
		t.Errorf("got %+v, want checked over the 1 posted call with %s invented", got, evInvented)
	}
}

func TestCheckInventedNamesNotCheckedWhenAResultIsNowhere(t *testing.T) {
	for _, name := range []string{"a result too large to show from a raw tool", "many calls leave some out"} {
		out := &RunOutcome{ThreadID: evThread, Question: "q", Events: []SignalEvent{
			{Type: evFinding, Agent: "obs", Content: advisorFinding(t, name)},
			{Type: sseSynthesis, Content: "**" + evInvented + "**"},
			{Type: evDone},
		}}

		if got := checkInventedNames([]evidenceStore{store(nil, nil)}, evNS, out, noWait); got.Captured {
			t.Errorf("%s: want not checked, got %+v", name, got)
		}
	}
}
