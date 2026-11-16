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

func obj(name string, hdr ...string) *nats.ObjectInfo {
	h := nats.Header{}
	for i := 0; i+1 < len(hdr); i += 2 {
		h.Set(hdr[i], hdr[i+1])
	}
	return &nats.ObjectInfo{ObjectMeta: nats.ObjectMeta{Name: name, Headers: h}}
}

const (
	evNodes  = "ns/crew/t1/k8s-nodes/001-resources_list"
	evSignal = "ns/crew/t1/k8s-nodes/agree-uuid1"
	evOther  = "ns/crew/t2/k8s-nodes/001-resources_list"
	evErr    = "ns/crew/t1/k8s-nodes/002-events_list"
)

func TestCollectEvidenceReadsOnlyThisThreadsSuccessfulCalls(t *testing.T) {
	st := &fakeEvidenceStore{
		infos: []*nats.ObjectInfo{
			obj(evNodes), obj(evSignal), obj(evOther), obj(evErr, "kubemoot.error", "true"),
		},
		bodies: map[string]string{
			evNodes:  "NAME\nhomelab-k8s-1-cp Ready",
			evSignal: "signal-only-name-1",
			evOther:  "other-thread-name-1",
			evErr:    `node "node3" not found`,
		},
	}
	got := collectEvidence([]evidenceStore{st}, "t1")
	if !got.Captured || got.Calls != 1 {
		t.Fatalf("want captured with 1 call, got %+v", got)
	}
	if !slices.Contains(got.Names, "homelab-k8s-1-cp") {
		t.Errorf("result tokens missing: %v", got.Names)
	}
	for _, bad := range []string{"signal-only-name-1", "other-thread-name-1", "node3"} {
		if slices.Contains(got.Names, bad) {
			t.Errorf("%q must not be evidence", bad)
		}
	}
}

func TestCollectEvidenceNotCaptured(t *testing.T) {
	empty := &fakeEvidenceStore{}
	cases := map[string][]evidenceStore{
		"no thread id": {&fakeEvidenceStore{infos: []*nats.ObjectInfo{obj(evNodes)}}},
		"no calls":     {empty},
		"list fails":   {&fakeEvidenceStore{listErr: errors.New("boom")}},
		"read fails":   {&fakeEvidenceStore{infos: []*nats.ObjectInfo{obj(evNodes)}, getErr: errors.New("gone")}},
		"none found":   {&fakeEvidenceStore{listErr: nats.ErrNoObjectsFound}},
	}
	for name, stores := range cases {
		thread := "t1"
		if name == "no thread id" {
			thread = ""
		}
		if got := collectEvidence(stores, thread); got.Captured || len(got.Names) != 0 {
			t.Errorf("%s: want not captured, got %+v", name, got)
		}
	}
}

func TestCollectEvidenceReadsBothBuckets(t *testing.T) {
	a := &fakeEvidenceStore{infos: []*nats.ObjectInfo{obj(evNodes)}, bodies: map[string]string{evNodes: "node-a-1"}}
	b := &fakeEvidenceStore{infos: []*nats.ObjectInfo{obj(evErr)}, bodies: map[string]string{evErr: "node-b-1"}}
	got := collectEvidence([]evidenceStore{a, b}, "t1")
	if got.Calls != 2 || !slices.Contains(got.Names, "node-a-1") || !slices.Contains(got.Names, "node-b-1") {
		t.Errorf("both buckets are read: %+v", got)
	}
}

func TestWorkspaceBuckets(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := workspaceBuckets(env(nil)); !slices.Equal(got, []string{workspaceBucket, legacyWorkspaceBucket}) {
		t.Errorf("defaults: %v", got)
	}
	if got := workspaceBuckets(env(map[string]string{"WORKSPACE_BUCKET": "x", "ARTIFACT_BUCKET": "x"})); !slices.Equal(got, []string{"x"}) {
		t.Errorf("same bucket twice is read once: %v", got)
	}
}

func TestIsCallObjectRejectsDeleted(t *testing.T) {
	o := obj(evNodes)
	o.Deleted = true
	if isCallObject(o, "t1") || isCallObject(nil, "t1") {
		t.Error("deleted and nil objects are not calls")
	}
}
