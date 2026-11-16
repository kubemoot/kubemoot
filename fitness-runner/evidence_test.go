/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"errors"
	"slices"
	"testing"

	"github.com/nats-io/nats.go"
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

// obj is a stored tool call. The runtime writes lower-case header names and the
// store hands them back as written, so the map is filled directly.
func obj(name string, hdr ...string) *nats.ObjectInfo {
	h := nats.Header{"gen_ai.operation.name": {"execute_tool"}}
	for i := 0; i+1 < len(hdr); i += 2 {
		h[hdr[i]] = []string{hdr[i+1]}
	}
	return &nats.ObjectInfo{ObjectMeta: nats.ObjectMeta{Name: name, Headers: h}}
}

const (
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
	delete(notACall.Headers, "gen_ai.operation.name")
	st := store([]*nats.ObjectInfo{obj(evNodes), obj(evSignal), obj(evOther), obj(evOtherN), notACall},
		map[string]string{
			evNodes: nodeBody, evSignal: "signal-only-1", evOther: "other-thread-1",
			evOtherN: "other-ns-1", "ns/crew/t1/k8s-nodes/003-x": "not-a-call-1",
		})
	set, calls, ok := readEvidence([]evidenceStore{st}, evNS, evThread, nil)
	if !ok || calls != 1 || !set.Has("homelab-k8s-1-cp") {
		t.Fatalf("ok=%v calls=%d", ok, calls)
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
	set, calls, ok := readEvidence([]evidenceStore{st}, evNS, evThread, nil)
	if !ok || calls != 2 {
		t.Fatalf("ok=%v calls=%d", ok, calls)
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
		"call still missing": {[]evidenceStore{good}, map[string]bool{"k8s-nodes/001-resources_list": true, "k8s-nodes/002-events_list": true}},
	}
	for name, tc := range cases {
		if set, _, ok := readEvidence(tc.stores, evNS, evThread, tc.expected); ok || set != nil {
			t.Errorf("%s: want not complete", name)
		}
	}
}

func TestReadEvidenceNamedCallsAllStored(t *testing.T) {
	st := store([]*nats.ObjectInfo{obj(evNodes)}, map[string]string{evNodes: nodeBody})
	_, _, ok := readEvidence([]evidenceStore{st}, evNS, evThread, map[string]bool{"k8s-nodes/001-resources_list": true})
	if !ok {
		t.Error("every named call is stored, want complete")
	}
}

func TestReadEvidenceReadsJSONAsJSON(t *testing.T) {
	st := store([]*nats.ObjectInfo{obj(evNodes, "Content-Type", "application/json")},
		map[string]string{evNodes: `{"items":[{"name":"node-01\nnginx-1"}]}`})
	set, _, ok := readEvidence([]evidenceStore{st}, evNS, evThread, nil)
	if !ok || !set.Has("node-01") || !set.Has("nginx-1") {
		t.Errorf("JSON escapes must separate words: ok=%v", ok)
	}
}

func TestExpectedCallKeys(t *testing.T) {
	ev := []SignalEvent{
		{Type: evFinding, Content: "[WORKSPACE agent=a calls=2]\n[CALL key=k8s-nodes/001-resources_list tool=x args={}]\n[CALL key=k8s-nodes/002-events_list tool=y]\nread k8s-nodes/*-<tool>"},
		{Type: "activity"},
	}
	got := expectedCallKeys(ev)
	if len(got) != 2 || !got["k8s-nodes/001-resources_list"] || !got["k8s-nodes/002-events_list"] {
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

func TestDecodeHeader(t *testing.T) {
	cases := map[string]string{"plain": "plain", "utf8-pct:a%20b": "a b", "utf8-pct:%zz": "utf8-pct:%zz"}
	for in, want := range cases {
		if got := decodeHeader(in); got != want {
			t.Errorf("decodeHeader(%q) = %q, want %q", in, got, want)
		}
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
		got := checkInventedNames([]evidenceStore{st}, evNS, realRun(q, tc.answer))
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
		if got := checkInventedNames([]evidenceStore{st}, tc.ns, tc.out); got.Captured || got.InventedNames != nil {
			t.Errorf("%s: want not checked, got %+v", name, got)
		}
	}
}

func TestCheckInventedNamesNamesFromTheQuestionAndAssertions(t *testing.T) {
	st := store([]*nats.ObjectInfo{obj(evNodes)}, map[string]string{evNodes: nodeBody})
	out := realRun("Is node-7 ready?", "**node-7** and **node-9** and **vllm-rig0**")
	out.Assertions = []AssertionResult{{Raw: `DEFER synthesis REFLECTS "mentions node-9"`}}
	out.Events[0].GPU = "vllm-rig0"
	got := checkInventedNames([]evidenceStore{st}, evNS, out)
	if !got.Captured || len(got.InventedNames) != 0 {
		t.Errorf("question, reference and crew names are allowed: %+v", got)
	}
}
