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
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// dispatchScenario must read the judge crew's SSE stream until its synthesis
// arrives, even when the judge is slow (a GPU-bound 32b discussion, ~11min
// observed). The prior fixed 300s http.Client.Timeout cut the stream mid-judge,
// producing "no synthesis verdict received" forever. This serves a delayed
// synthesis and asserts the verdict is still read (bounded by the per-dispatch
// context, not a short fixed cap). See [[Deferred Judge Spurious Zeros and Stuck Pass]].
func TestDispatchScenario_ReadsDelayedSynthesis(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"conversationId":"conv-judge-1"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"type\":\"phase\",\"content\":\"\"}\n\n"))
		if fl != nil {
			fl.Flush()
		}
		time.Sleep(300 * time.Millisecond) // slow judge
		_, _ = w.Write([]byte("data: {\"type\":\"synthesis\",\"content\":\"{\\\"scores\\\":[{\\\"index\\\":0,\\\"score\\\":0.9}],\\\"total\\\":1}\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"done\"}\n\n"))
		if fl != nil {
			fl.Flush()
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	doc := `{"question":"q","reference":"r","answers":[{"synthesis":"a","evidence":[]}],"gated":0,"total":1}`
	quality, _, err := dispatchScenario(ctx, &http.Client{}, srv.URL, doc)
	if err != nil {
		t.Fatalf("dispatchScenario errored on a delayed-but-valid stream: %v", err)
	}
	if quality != 90 {
		t.Errorf("quality = %v, want 90 (0.9/1 * 100)", quality)
	}
}

// A dispatch whose context deadline fires before any synthesis returns an error
// (so the pass retries later) rather than hanging.
func TestDispatchScenario_ContextDeadlineReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"conversationId":"conv-judge-2"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		time.Sleep(2 * time.Second) // never synthesizes before the ctx deadline
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	doc := `{"question":"q","reference":"r","answers":[{"synthesis":"a","evidence":[]}],"gated":0,"total":1}`
	_, _, err := dispatchScenario(ctx, &http.Client{}, srv.URL, doc)
	if err == nil {
		t.Fatal("expected an error when the dispatch context deadline fires before synthesis")
	}
	// A deadline-fired (transient) error must NOT be errJudgeNoVerdict: that sentinel
	// drives the in-pass retry loop, whereas a timeout should back off to the next
	// reconcile pass. Pin this so a future refactor can't conflate the two.
	if errors.Is(err, errJudgeNoVerdict) {
		t.Errorf("deadline-fired error must not be errJudgeNoVerdict, got %v", err)
	}
}

// buildComparisonDoc assembles the judge's scoring context from a scenario's
// iteration transcripts operator-side (replacing the collect_scenario tool): it
// extracts question + reference, gates empty-synthesis/consensus-failed runs, and
// carries each judgeable answer's synthesis + evidence.
// See [[Lean Judge Direct Context No Tool Round-Trip]].
func TestBuildComparisonDoc(t *testing.T) {
	pfx := testRunPrefix
	// s0-i1: a good answer with an agree finding (evidence) + a DEFER reference.
	good := `{"question":"What can fail?","assertions":[{"raw":"DEFER synthesis REFLECTS \"the answer\"","passed":true}],` +
		`"events":[{"type":"finding","agent":"k8s","signal":"agree","content":"pod X is crashlooping"},{"type":"synthesis","content":"pod X is crashlooping"},{"type":"done"}]}`
	// s0-i2: empty synthesis -> gated.
	emptySynth := `{"question":"What can fail?","assertions":[{"raw":"DEFER synthesis REFLECTS \"the answer\"","passed":true}],"events":[{"type":"done"}]}`
	store := fakeStore{objs: map[string][]byte{
		pfx + "s0-i1.json": []byte(good),
		pfx + "s0-i2.json": []byte(emptySynth),
		pfx + "s1-i1.json": []byte(good), // different scenario, must be excluded
	}}

	doc, total, answers, err := buildComparisonDoc(store, pfx, 0, testVerdictReflects)
	if err != nil {
		t.Fatalf("buildComparisonDoc error: %v", err)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2 (only s0 iterations)", total)
	}
	if answers != 1 {
		t.Errorf("answers = %d, want 1 (empty-synthesis run gated)", answers)
	}
	var parsed judgeComparisonDoc
	if uErr := json.Unmarshal([]byte(doc), &parsed); uErr != nil {
		t.Fatalf("doc is not valid JSON: %v", uErr)
	}
	if parsed.Reference != testTheAnswer {
		t.Errorf("reference = %q, want %q", parsed.Reference, testTheAnswer)
	}
	if parsed.Gated != 1 {
		t.Errorf("gated = %d, want 1", parsed.Gated)
	}
	if len(parsed.Answers) != 1 || parsed.Answers[0].Synthesis != "pod X is crashlooping" {
		t.Fatalf("answers = %+v, want one answer with the synthesis", parsed.Answers)
	}
	if len(parsed.Answers[0].Evidence) != 1 || parsed.Answers[0].Evidence[0] != "k8s (agree): pod X is crashlooping" {
		t.Errorf("evidence = %v, want the agent-prefixed finding", parsed.Answers[0].Evidence)
	}
}

// TestParseScenarioQuality covers the batched verdict → scenario quality math:
// quality = 100 × Σ(answer scores) / total, with gated runs counted as 0 in total.
func TestParseScenarioQuality(t *testing.T) {
	cases := []struct {
		name          string
		synthesis     string
		want          float64
		wantReason    string // if set, the representative reason must match (lowest-scoring answer)
		wantErr       bool
		wantNoVerdict bool // if set, the error must be errJudgeNoVerdict (retriable judge flake)
	}{
		{
			name:      "two answers, all scored, no gating",
			synthesis: `{"scores":[{"index":0,"score":1.0,"fabrication":false},{"index":1,"score":0.5,"fabrication":false}],"total":2}`,
			want:      75, // 100*(1.0+0.5)/2
		},
		{
			name:       "reason comes from the lowest-scoring answer",
			synthesis:  `{"scores":[{"index":0,"score":1.0,"reason":"all key facts present"},{"index":1,"score":0.4,"reason":"missed the storage implication"}],"total":2}`,
			want:       70, // 100*(1.0+0.4)/2
			wantReason: "missed the storage implication",
		},
		{
			name:      "gated runs pull the mean down (2 scored of 4 total)",
			synthesis: `{"scores":[{"index":0,"score":1.0},{"index":1,"score":0.5}],"total":4}`,
			want:      37.5, // 100*1.5/4
		},
		{
			name:      "tolerates 0-100 per-answer scores",
			synthesis: `{"scores":[{"index":0,"score":90}],"total":1}`,
			want:      90,
		},
		{
			name:      "fabrication flag is advisory: the analog score is honored, not zeroed",
			synthesis: `{"scores":[{"index":0,"score":1.0,"fabrication":false},{"index":1,"score":0.9,"fabrication":true}],"total":2}`,
			want:      95, // 100*(1.0+0.9)/2 - the rubric already deducts for fabrication; the operator does not re-zero
		},
		{
			name:      "a fabrication-flagged answer keeps its (already-deducted) score",
			synthesis: `{"scores":[{"index":0,"score":0.15,"fabrication":true}],"total":1}`,
			want:      15, // wholly-invented answers land low via the rubric band, not via an operator gate
		},
		{
			name:      "gated runs counted in total (empty scores, total>0) → 0",
			synthesis: `{"scores":[],"total":3}`,
			want:      0, // legitimately gated: total counts them, sum is 0 → real 0, NOT a no-verdict flake
		},
		{
			name:      "strips think block before parsing",
			synthesis: "<think>weighing the answers…</think>\n{\"scores\":[{\"index\":0,\"score\":0.8}],\"total\":1}",
			want:      80,
		},
		{
			name:          "no scores AND total<=0 → retriable no-verdict signal (judge flaked, not a real 0)",
			synthesis:     `{"scores":[],"total":0}`,
			wantErr:       true,
			wantNoVerdict: true,
		},
		{
			name:      "float total 0.0 with a score → falls back to score count",
			synthesis: `{"scores":[{"index":0,"score":1.0}],"total":0.0}`,
			want:      100, // 100*1.0/1 (bad total, 1 score → denominator 1)
		},
		{
			name:      "float total 2.0 parses (not just int)",
			synthesis: `{"scores":[{"index":0,"score":1.0},{"index":1,"score":0.0}],"total":2.0}`,
			want:      50, // 100*1.0/2
		},
		{
			name:      "no JSON is an error",
			synthesis: `the judge rambled without emitting a verdict`,
			wantErr:   true,
		},
		{
			name:       "over-credit cap: near-perfect score with a fabrication reason is clamped to 0.70",
			synthesis:  `{"scores":[{"index":0,"score":1.0,"reason":"correct shape but fabricated the VM ID 100"}],"total":1}`,
			want:       70, // 1.0 capped to 0.70 because the reason names a critical flaw
			wantReason: "correct shape but fabricated the VM ID 100",
		},
		{
			name:       "over-credit cap: unconfirmed-claim reason clamps a 0.95",
			synthesis:  `{"scores":[{"index":0,"score":0.95,"reason":"asserts an unconfirmed correlation between disk I/O and the node"}],"total":1}`,
			want:       70,
			wantReason: "asserts an unconfirmed correlation between disk I/O and the node",
		},
		{
			name:      "no cap when the reason names no critical flaw",
			synthesis: `{"scores":[{"index":0,"score":1.0,"reason":"all key facts present and correct"}],"total":1}`,
			want:      100, // benign reason: cap must not over-trigger
		},
		{
			name:      "cap only lowers: a low score with a flaw reason is unchanged",
			synthesis: `{"scores":[{"index":0,"score":0.4,"reason":"fabricated the metrics"}],"total":1}`,
			want:      40, // already below the 0.70 cap
		},
		{
			name:      "fabrication flag + an affirmative flaw reason DOES clamp (distinct from flag-only)",
			synthesis: `{"scores":[{"index":0,"score":0.9,"fabrication":true,"reason":"fabricated the central claim"}],"total":1}`,
			want:      70, // the FLAG alone is advisory, but a reason naming the fabrication is the over-credit case
		},
		{
			name:      "negated flaw reason does NOT clamp",
			synthesis: `{"scores":[{"index":0,"score":1.0,"reason":"no fabricated facts, fully grounded in evidence"}],"total":1}`,
			want:      100, // "no fabricated" is a denial, not a flaw
		},
		{
			name:      "cap is per-answer, not applied to the mean",
			synthesis: `{"scores":[{"index":0,"score":1.0,"reason":"fabricated the ID"},{"index":1,"score":0.8,"reason":"good"}],"total":2}`,
			want:      75, // (0.70 capped + 0.80)/2 * 100, NOT (1.0+0.8)/2
		},
		{
			// The fitness-coordinator's actual output for a one-answer scenario: a
			// SINGLE verdict, not the batch shape. Before tolerance this parsed to
			// empty scores + total 0 and was discarded as a no-verdict 0, zeroing a
			// real 0.95. See [[Deferred Judge Spurious Zeros and Stuck Pass]].
			name:       "single-answer verdict shape is scored, not discarded",
			synthesis:  `{"score":0.95,"fabrication":false,"reasoning":"correctly identifies no visible database deployments"}`,
			want:       95,
			wantReason: "correctly identifies no visible database deployments",
		},
		{
			name:      "single-answer verdict tolerates 0-100 score",
			synthesis: `{"score":85,"fabrication":false,"reasoning":"mostly correct"}`,
			want:      85,
		},
		{
			name:       "single-answer verdict accepts the batch key 'reason' too",
			synthesis:  `{"score":0.8,"reason":"missed one node"}`,
			want:       80,
			wantReason: "missed one node",
		},
		{
			name:      "single-answer verdict with think block strips and scores",
			synthesis: "<think>scoring…</think>\n{\"score\":1.0,\"fabrication\":false,\"reasoning\":\"all facts present\"}",
			want:      100,
		},
		{
			// A single-answer verdict still applies the over-credit cap: a near-perfect
			// score whose reason names a critical flaw is clamped, same as batch.
			name:       "single-answer verdict honors the over-credit cap",
			synthesis:  `{"score":1.0,"fabrication":false,"reasoning":"fabricated the VM ID 100"}`,
			want:       70,
			wantReason: "fabricated the VM ID 100",
		},
		{
			// A genuinely empty verdict (no "score" field) must STILL be the retriable
			// no-verdict signal - the single-shape tolerance must not swallow it.
			name:          "empty object is still a no-verdict signal, not a coerced 0",
			synthesis:     `{}`,
			wantErr:       true,
			wantNoVerdict: true,
		},
		{
			// A PRESENT "score":0 is a real content 0, NOT a no-verdict. This is the
			// exact case the *float64 pointer on singleVerdict.Score exists to
			// distinguish from an absent field; without this test a refactor dropping
			// the pointer would silently turn every score:0 verdict into a no-verdict.
			name:       "single-answer score 0 is a real 0, not a no-verdict signal",
			synthesis:  `{"score":0,"fabrication":false,"reasoning":"answer was truly wrong"}`,
			want:       0,
			wantReason: "answer was truly wrong",
		},
		{
			// Both keys present: "reasoning" (the singular key) wins over "reason".
			name:       "single-answer verdict prefers reasoning over reason when both present",
			synthesis:  `{"score":0.8,"reasoning":"primary rationale","reason":"secondary"}`,
			want:       80,
			wantReason: "primary rationale",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason, err := parseScenarioQuality(tc.synthesis)
			if tc.wantErr {
				assertParseScenarioError(t, got, err, tc.wantNoVerdict)
				return
			}
			assertParseScenarioOK(t, got, reason, err, tc.want, tc.wantReason)
		})
	}
}

// assertParseScenarioError checks the error path of parseScenarioQuality: an error
// must be present, and the errJudgeNoVerdict sentinel must match wantNoVerdict.
func assertParseScenarioError(t *testing.T, got float64, err error, wantNoVerdict bool) {
	t.Helper()
	if err == nil {
		t.Errorf("expected error, got quality=%v", got)
	}
	if wantNoVerdict && !errors.Is(err, errJudgeNoVerdict) {
		t.Errorf("expected errJudgeNoVerdict, got %v", err)
	}
	if !wantNoVerdict && errors.Is(err, errJudgeNoVerdict) {
		t.Errorf("did not expect errJudgeNoVerdict, got it")
	}
}

// assertParseScenarioOK checks the success path of parseScenarioQuality: no error,
// the quality matches want, and (when set) the representative reason matches.
func assertParseScenarioOK(t *testing.T, got float64, reason string, err error, want float64, wantReason string) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(got-want) > 0.01 {
		t.Errorf("quality = %v, want %v", got, want)
	}
	if wantReason != "" && reason != wantReason {
		t.Errorf("reason = %q, want %q", reason, wantReason)
	}
}

func TestHasCriticalFlawReason(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		want   bool
	}{
		{testEmpty, "", false},
		{"uppercase is matched", "FABRICATED the VM ID", true},
		{"affirmative fabricated", "correct core but fabricated the node name", true},
		{"negated: no fabricated", "no fabricated facts; all grounded", false},
		{"negated: free of unsupported claims", "complete and free of unsupported claims", false},
		{"bare unconfirmed is a hedge, not a flaw", "data is unconfirmed by secondary sources", false},
		{"unconfirmed claim is a flaw", "asserts an unconfirmed claim about the disk", true},
		{"unconfirmed correlation is a flaw", "states an unconfirmed correlation as fact", true},
		{"unconfigured must not match unconfirmed", "the cluster is unconfigured", false},
		{"missing required element", "missing required element from the table", true},
		{"constraint violation", "a constraint violation in the output format", true},
		{"critical flaw", "a critical flaw in the core analysis", true},
		{"benign reason", "all key facts present and correct", false},
		{"bare unsupported is not enough", "one mildly unsupported detail", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasCriticalFlawReason(tc.reason); got != tc.want {
				t.Errorf("hasCriticalFlawReason(%q) = %v, want %v", tc.reason, got, tc.want)
			}
		})
	}
}
