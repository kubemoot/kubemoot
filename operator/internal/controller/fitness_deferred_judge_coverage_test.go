/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

// newTestJudgePass builds a judgePass wired to a fake store (no reconciler / NATS),
// so the worker methods that persist or read the checkpoint are unit-testable.
func newTestJudgePass(store objectStore) *judgePass {
	return &judgePass{
		store:  store,
		prefix: testRunPrefix,
		cache:  deferredScoreCache{Scores: map[string]float64{}, Reasons: map[string]string{}},
		hc:     &http.Client{},
		log:    logr.Discard(),
	}
}

// newJudgeHTTPClient must carry NO total http.Client.Timeout: that cap also bounds
// the streaming SSE body read and would cut a slow (GPU-bound) judge mid-synthesis.
// Bounding is via the per-dispatch context instead.
func TestNewJudgeHTTPClientHasNoTotalTimeout(t *testing.T) {
	c := newJudgeHTTPClient()
	if c.Timeout != 0 {
		t.Errorf("judge client must have no total timeout, got %v", c.Timeout)
	}
	if c.Transport == nil {
		t.Error("expected transport-level timeouts to be configured")
	}
}

// loadTranscript reads one iteration transcript, skipping any non-.json key and any
// unreadable or unparseable object so a single bad blob never aborts a judge pass.
func TestLoadTranscriptSkipsBadObjects(t *testing.T) {
	store := fakeStore{objs: map[string][]byte{
		"p/s0-i1.json":  []byte(`{"question":"q","events":[]}`),
		"p/s0-i1.txt":   []byte(`not a json key`),
		"p/empty.json":  {},
		"p/broken.json": []byte(`{not-json`),
	}}
	if _, ok := loadTranscript(store, "p/s0-i1.json"); !ok {
		t.Error("a valid transcript should load")
	}
	if _, ok := loadTranscript(store, "p/s0-i1.txt"); ok {
		t.Error("a non-.json key must be skipped")
	}
	if _, ok := loadTranscript(store, "p/empty.json"); ok {
		t.Error("an empty object must be skipped")
	}
	if _, ok := loadTranscript(store, "p/broken.json"); ok {
		t.Error("an unparseable object must be skipped")
	}
}

// loadDeferredCache returns an empty, not-complete checkpoint when absent and the
// parsed checkpoint when present.
func TestLoadDeferredCacheAbsentAndPresent(t *testing.T) {
	prefix := testRunPrefix
	empty := loadDeferredCache(fakeStore{objs: map[string][]byte{}}, prefix)
	if empty.Complete || len(empty.Scores) != 0 {
		t.Errorf("absent checkpoint must be empty+incomplete, got %+v", empty)
	}
	blob := []byte(`{"scores":{"s-a":95},"reasons":{"s-a":"good"},"complete":true}`)
	c := loadDeferredCache(fakeStore{objs: map[string][]byte{deferredSidecarKey(prefix): blob}}, prefix)
	if !c.Complete || c.Scores["s-a"] != 95 || c.Reasons["s-a"] != "good" {
		t.Errorf("present checkpoint not parsed: %+v", c)
	}
}

// consensusOKForJudge reads the ">=N {toolers|specialists} agree" floor; an absent
// assertion is treated as passing, and the legacy "specialist" term is tolerated.
func TestConsensusOKForJudge(t *testing.T) {
	if !consensusOKForJudge(transcriptDoc{}) {
		t.Error("absent agree-floor should be treated as OK")
	}
	pass := transcriptDoc{Assertions: []transcriptAssertion{{Raw: "at least 2 toolers agree", Passed: true}}}
	if !consensusOKForJudge(pass) {
		t.Error("passed tooler-agree should be OK")
	}
	fail := transcriptDoc{Assertions: []transcriptAssertion{{Raw: "at least 2 specialists agree", Passed: false}}}
	if consensusOKForJudge(fail) {
		t.Error("failed specialist-agree (legacy term) should be not-OK")
	}
}

// evidenceFromEvents returns finding-event text (agent + signal prefixed), falling
// back to Summary, and skipping non-findings and blank entries.
func TestEvidenceFromEvents(t *testing.T) {
	events := []transcriptEvent{
		{Type: "phase"},
		{Type: testFinding, Agent: testK8s, Signal: testAgree, Content: "found 3 pods"},
		{Type: testFinding, Summary: "from summary"},
		{Type: testFinding, Content: "   "},
	}
	ev := evidenceFromEvents(events)
	if len(ev) != 2 {
		t.Fatalf("expected 2 evidence lines, got %d: %v", len(ev), ev)
	}
	if ev[0] != "k8s (agree): found 3 pods" {
		t.Errorf("agent+signal prefix wrong: %q", ev[0])
	}
	if ev[1] != "from summary" {
		t.Errorf("summary fallback wrong: %q", ev[1])
	}
}

// referenceForKeyword pulls the quoted reference from the matching DEFER assertion.
func TestReferenceForKeyword(t *testing.T) {
	td := transcriptDoc{Assertions: []transcriptAssertion{
		{Raw: `DEFER synthesis REFLECTS "the expected answer"`},
		{Raw: `HTTP 200`},
	}}
	if got := referenceForKeyword(td, testVerdictReflects); got != "the expected answer" {
		t.Errorf("reference = %q, want 'the expected answer'", got)
	}
	if got := referenceForKeyword(td, "OTHER"); got != "" {
		t.Errorf("unknown keyword should yield empty, got %q", got)
	}
}

// parseDeferredAssertion rejects non-DEFER lines, lines with no quoted reference,
// and lines with no keyword token; and parses a well-formed assertion.
func TestParseDeferredAssertionEdges(t *testing.T) {
	rejects := []string{
		"HTTP 200 with conversationId",
		`DEFER synthesis REFLECTS no-quote-here`,
		`DEFER "only a quote"`,
	}
	for _, raw := range rejects {
		if _, _, ok := parseDeferredAssertion(raw); ok {
			t.Errorf("expected reject for %q", raw)
		}
	}
	kw, ref, ok := parseDeferredAssertion(`DEFER synthesis REFLECTS "ref text"`)
	if !ok || kw != testVerdictReflects || ref != "ref text" {
		t.Errorf("parse: ok=%v kw=%q ref=%q", ok, kw, ref)
	}
}

// postJudgeDiscussion errors when the discussion POST returns no conversationId.
func TestPostJudgeDiscussionNoConversationID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	if _, err := postJudgeDiscussion(context.Background(), &http.Client{}, srv.URL, "doc"); err == nil {
		t.Fatal("expected an error when POST returns no conversationId")
	}
}

// A stream that ends with no synthesis is a transient error, NOT errJudgeNoVerdict
// (which is reserved for a COMPLETED verdict with nothing to score).
func TestDispatchScenarioNoSynthesisIsTransientError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"conversationId":"c1"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"done\"}\n\n"))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := dispatchScenario(ctx, &http.Client{}, srv.URL, "doc")
	if err == nil {
		t.Fatal("expected a 'no synthesis verdict received' error")
	}
	if errors.Is(err, errJudgeNoVerdict) {
		t.Error("a no-synthesis (transient) error must not be errJudgeNoVerdict")
	}
}

// recordScore updates the in-memory checkpoint AND persists it to the store after
// every scenario, so an operator restart resumes instead of re-judging from zero.
func TestRecordScorePersistsToStore(t *testing.T) {
	store := fakeStore{objs: map[string][]byte{}}
	p := newTestJudgePass(store)
	p.recordScore("scenario-a", 95, "all key facts present")
	if p.cache.Scores["scenario-a"] != 95 || p.cache.Reasons["scenario-a"] != "all key facts present" {
		t.Fatalf("recordScore did not update the cache: %+v", p.cache)
	}
	blob := store.objs[deferredSidecarKey(p.prefix)]
	if blob == nil {
		t.Fatal("recordScore must persist the checkpoint to the store")
	}
	var c deferredScoreCache
	if err := json.Unmarshal(blob, &c); err != nil || c.Scores["scenario-a"] != 95 {
		t.Errorf("persisted checkpoint missing the score: %+v (err %v)", c, err)
	}
}

// judgeAndRecord dispatches the comparison doc to the judge, then records the
// resulting quality and persists the checkpoint. Success path: one verdict, no retry.
func TestJudgeAndRecordSuccessRecordsQuality(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"conversationId":"c1"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"synthesis\",\"content\":\"{\\\"scores\\\":[{\\\"index\\\":0,\\\"score\\\":1.0}],\\\"total\\\":1}\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"done\"}\n\n"))
	}))
	defer srv.Close()
	store := fakeStore{objs: map[string][]byte{}}
	p := newTestJudgePass(store)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out := p.judgeAndRecord(ctx, "scenario-b", testVerdictReflects, srv.URL, "{}"); out != scenarioHandled {
		t.Fatalf("expected scenarioHandled, got %v", out)
	}
	if p.cache.Scores["scenario-b"] != 100 {
		t.Errorf("expected quality 100 recorded, got %v", p.cache.Scores["scenario-b"])
	}
	if store.objs[deferredSidecarKey(p.prefix)] == nil {
		t.Error("judgeAndRecord must persist the checkpoint")
	}
}

// readDeferredScores returns the cached per-scenario scores (the dashboard /scores view).
func TestReadDeferredScores(t *testing.T) {
	prefix := testRunPrefix
	blob := []byte(`{"scores":{"s-a":88,"s-b":0},"complete":false}`)
	scores := readDeferredScores(fakeStore{objs: map[string][]byte{deferredSidecarKey(prefix): blob}}, prefix)
	if scores["s-a"] != 88 || scores["s-b"] != 0 {
		t.Errorf("readDeferredScores returned %+v", scores)
	}
}
