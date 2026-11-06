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
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const (
	verdictEvent        = `{"type":"synthesis","content":"{\"scores\":[{\"index\":0,\"score\":1.0}],\"total\":1}"}`
	prose               = `{"type":"synthesis","content":"I could not complete the evaluation."}`
	doneEvent           = `{"type":"done"}`
	contextFailureEvent = `{"type":"finding","agent":"fitness-judge","signal":"failure","failureType":"CONTEXT_EXCEEDED"}`
	otherFailureEvent   = `{"type":"finding","agent":"fitness-judge","signal":"failure","failureType":"model_timeout"}`
	scenarioB           = "scenario-b"
)

// judgeServer answers each dispatch with the next scripted stream (the last one
// repeats) and counts the dispatches.
func judgeServer(t *testing.T, streams ...[]string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			n := int(posts.Add(1))
			_, _ = fmt.Fprintf(w, `{"conversationId":"c%d"}`, n)
			return
		}
		n := int(posts.Load())
		events := streams[min(n, len(streams))-1]
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range events {
			_, _ = w.Write([]byte("data: " + ev + "\n\n"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &posts
}

func judgeOnce(t *testing.T, p *judgePass, endpoint string) scenarioOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.judgeAndRecord(ctx, scenarioB, testVerdictReflects, endpoint, "{}")
}

func TestJudgedOnTheFirstTryCountsOneAttempt(t *testing.T) {
	srv, _ := judgeServer(t, []string{verdictEvent, doneEvent})
	p := newTestJudgePass(fakeStore{objs: map[string][]byte{}})
	if out := judgeOnce(t, p, srv.URL); out != scenarioHandled {
		t.Fatalf("outcome %v, want handled", out)
	}
	if p.cache.Scores[scenarioB] != 100 || p.cache.Attempts[scenarioB] != 1 || len(p.cache.Unjudged) != 0 {
		t.Errorf("cache after a first-try verdict: %+v", p.cache)
	}
}

func TestTransientFailureThenSuccess(t *testing.T) {
	srv, _ := judgeServer(t, []string{prose, doneEvent}, []string{verdictEvent, doneEvent})
	p := newTestJudgePass(fakeStore{objs: map[string][]byte{}})
	if out := judgeOnce(t, p, srv.URL); out != scenarioPending {
		t.Fatalf("first attempt outcome %v, want pending", out)
	}
	if out := judgeOnce(t, p, srv.URL); out != scenarioHandled {
		t.Fatalf("second attempt outcome %v, want handled", out)
	}
	if p.cache.Scores[scenarioB] != 100 || p.cache.Attempts[scenarioB] != 2 || len(p.cache.Unjudged) != 0 {
		t.Errorf("cache after retry then verdict: %+v", p.cache)
	}
}

func TestFailuresUpToTheLimitThenUnjudged(t *testing.T) {
	srv, posts := judgeServer(t, []string{prose, doneEvent})
	p := newTestJudgePass(fakeStore{objs: map[string][]byte{}})
	for attempt := 1; attempt < maxJudgeAttempts; attempt++ {
		if out := judgeOnce(t, p, srv.URL); out != scenarioPending {
			t.Fatalf("attempt %d outcome %v, want pending", attempt, out)
		}
	}
	if out := judgeOnce(t, p, srv.URL); out != scenarioHandled {
		t.Fatalf("attempt %d outcome %v, want handled (unjudged)", maxJudgeAttempts, out)
	}
	if _, scored := p.cache.Scores[scenarioB]; scored {
		t.Error("an unjudged scenario must not carry a score")
	}
	if p.cache.Unjudged[scenarioB] == "" || int(posts.Load()) != maxJudgeAttempts {
		t.Errorf("unjudged=%v dispatches=%d, want a reason and %d dispatches", p.cache.Unjudged, posts.Load(), maxJudgeAttempts)
	}
	if out := p.processScenarioSettled(scenarioB); out != scenarioHandled {
		t.Errorf("an unjudged scenario is settled, got %v", out)
	}
}

// processScenarioSettled reports what processScenario does for a scenario the
// checkpoint already settled, without any transcript or crew.
func (p *judgePass) processScenarioSettled(scenario string) scenarioOutcome {
	p.suite = &kubemootv1alpha1.CrewFitnessSuite{Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
		Scripts: []kubemootv1alpha1.SuiteScript{{TestRef: scenario}},
	}}
	return p.processScenario(context.Background(), 0)
}

func TestContextFailureIsUnjudgedAtOnce(t *testing.T) {
	cases := map[string][]string{
		"failure signal and no synthesis":    {contextFailureEvent, doneEvent},
		"failure signal and prose synthesis": {contextFailureEvent, prose, doneEvent},
		"stream error":                       {`{"type":"error","error":"context_exceeded: prompt too long"}`, doneEvent},
	}
	for name, events := range cases {
		t.Run(name, func(t *testing.T) {
			srv, posts := judgeServer(t, events)
			p := newTestJudgePass(fakeStore{objs: map[string][]byte{}})
			if out := judgeOnce(t, p, srv.URL); out != scenarioHandled {
				t.Fatalf("outcome %v, want handled", out)
			}
			if p.cache.Unjudged[scenarioB] != unjudgedReasonTooLarge || posts.Load() != 1 {
				t.Errorf("unjudged=%v dispatches=%d, want the too-large reason after one dispatch", p.cache.Unjudged, posts.Load())
			}
		})
	}
}

func TestOtherFailureSignalIsRetriedNotUnjudged(t *testing.T) {
	srv, _ := judgeServer(t, []string{otherFailureEvent, doneEvent})
	p := newTestJudgePass(fakeStore{objs: map[string][]byte{}})
	if out := judgeOnce(t, p, srv.URL); out != scenarioPending {
		t.Fatalf("outcome %v, want pending", out)
	}
	if len(p.cache.Unjudged) != 0 {
		t.Errorf("a timeout failure must not be unjudged at once: %v", p.cache.Unjudged)
	}
}

func TestAVerdictBeatsAContextFailureOnTheStream(t *testing.T) {
	srv, _ := judgeServer(t, []string{contextFailureEvent, verdictEvent, doneEvent})
	p := newTestJudgePass(fakeStore{objs: map[string][]byte{}})
	if out := judgeOnce(t, p, srv.URL); out != scenarioHandled || p.cache.Scores[scenarioB] != 100 {
		t.Errorf("outcome %v cache %+v, want the verdict scored", out, p.cache)
	}
}

func TestRestartKeepsTheAttemptCount(t *testing.T) {
	store := fakeStore{objs: map[string][]byte{}}
	srv, _ := judgeServer(t, []string{prose, doneEvent})
	first := newTestJudgePass(store)
	judgeOnce(t, first, srv.URL)
	judgeOnce(t, first, srv.URL)

	// The operator restarts: a new pass resumes from the stored checkpoint.
	resumed := newTestJudgePass(store)
	resumed.cache = loadDeferredCache(store, resumed.prefix)
	if got := resumed.cache.Attempts[scenarioB]; got != 2 {
		t.Fatalf("attempts after restart = %d, want 2", got)
	}
	if out := judgeOnce(t, resumed, srv.URL); out != scenarioHandled || resumed.cache.Unjudged[scenarioB] == "" {
		t.Errorf("the third attempt overall must end unjudged; outcome %v unjudged %v", out, resumed.cache.Unjudged)
	}
	// And the unjudged mark survives another restart.
	if loadDeferredCache(store, resumed.prefix).Unjudged[scenarioB] == "" {
		t.Error("the unjudged mark must be persisted")
	}
}

func twoScenarioPass(t *testing.T, store fakeStore) *judgePass {
	t.Helper()
	store.objs[testRunS0I1] = []byte(feasibleTranscript)
	store.objs[testRunPrefix+"s1-i1.json"] = []byte(feasibleTranscript)
	p := newWorkerPass(t, store)
	p.suite.Spec.Scripts = []kubemootv1alpha1.SuiteScript{{TestRef: "a"}, {TestRef: scenarioB}}
	return p
}

func TestAllScenariosUnjudgedEndsThePassComplete(t *testing.T) {
	srv, _ := judgeServer(t, []string{contextFailureEvent, doneEvent})
	store := fakeStore{objs: map[string][]byte{}}
	p := twoScenarioPass(t, store)
	p.endpoints[testVerdictReflects] = srv.URL
	p.cache.Total = p.countJudgeable()

	if pending := p.scorePending(context.Background()); pending != 0 {
		t.Fatalf("pending = %d, want 0 once every scenario is unjudged", pending)
	}
	p.persist(true)
	p.suite.Status.Phase = kubemootv1alpha1.CrewFitnessSuitePhaseCompleted
	js := judgeStatusFor(p.suite, loadDeferredCache(store, p.prefix), metav1.Now())
	if js.Phase != kubemootv1alpha1.FitnessJudgePhaseComplete || js.Judged != 0 || js.Total != 2 || js.Mean != nil || len(js.Unjudged) != 2 {
		t.Errorf("status = %+v, want Complete, judged 0, total 2, no mean, 2 unjudged", js)
	}
	if js.Unjudged[0].Scenario != "a" || js.Unjudged[0].Reason != unjudgedReasonTooLarge {
		t.Errorf("unjudged[0] = %+v", js.Unjudged[0])
	}
}

func TestPassStaysPendingUntilTheLimitThenEnds(t *testing.T) {
	srv, _ := judgeServer(t, []string{prose, doneEvent})
	p := twoScenarioPass(t, fakeStore{objs: map[string][]byte{}})
	p.endpoints[testVerdictReflects] = srv.URL
	for pass := 1; pass < maxJudgeAttempts; pass++ {
		if pending := p.scorePending(context.Background()); pending != 2 {
			t.Fatalf("pass %d pending = %d, want 2", pass, pending)
		}
	}
	if pending := p.scorePending(context.Background()); pending != 0 {
		t.Fatalf("pass %d pending = %d, want 0", maxJudgeAttempts, pending)
	}
}

func TestUnresolvedKeywordEndsUnjudgedAfterTheLimit(t *testing.T) {
	store := fakeStore{objs: map[string][]byte{testRunS0I1: []byte(feasibleTranscript)}}
	p := newWorkerPass(t, store) // no crew declares REFLECTS
	for pass := 1; pass < maxJudgeAttempts; pass++ {
		if out := p.processScenario(context.Background(), 0); out != scenarioPending {
			t.Fatalf("pass %d outcome %v, want pending", pass, out)
		}
	}
	if out := p.processScenario(context.Background(), 0); out != scenarioHandled || p.cache.Unjudged[testScenarioX] == "" {
		t.Errorf("outcome %v unjudged %v, want unjudged after %d passes", out, p.cache.Unjudged, maxJudgeAttempts)
	}
}

func TestInterruptedDispatchIsNotChargedToTheScenario(t *testing.T) {
	srv, _ := judgeServer(t, []string{verdictEvent, doneEvent})
	p := newTestJudgePass(fakeStore{objs: map[string][]byte{}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the pass is cut off before the dispatch can finish
	if out := p.judgeAndRecord(ctx, scenarioB, testVerdictReflects, srv.URL, "{}"); out != scenarioBudgetExhausted {
		t.Fatalf("outcome %v, want budget exhausted", out)
	}
	if p.cache.Attempts[scenarioB] != 0 || len(p.cache.Unjudged) != 0 {
		t.Errorf("attempts=%v unjudged=%v, want the interrupted dispatch refunded", p.cache.Attempts, p.cache.Unjudged)
	}
}

func TestMeanCoversJudgedScenariosOnly(t *testing.T) {
	suite := suiteIn(kubemootv1alpha1.CrewFitnessSuitePhaseCompleted, "a", "b", "c", "d")
	cache := deferredScoreCache{
		Scores:   map[string]float64{"a": 90, "b": 70},
		Unjudged: map[string]string{"c": unjudgedReasonTooLarge, "d": "judge failed on all 3 attempts: boom"},
		Total:    4,
		Complete: true,
	}
	js := judgeStatusFor(suite, cache, metav1.Now())
	if js.Phase != kubemootv1alpha1.FitnessJudgePhaseComplete || js.Judged != 2 || js.Total != 4 || ptr.Deref(js.Mean, -1) != 80 {
		t.Errorf("status = %+v, want Complete judged 2 total 4 mean 80", js)
	}
	if len(js.Unjudged) != 2 || js.Unjudged[1].Scenario != "d" {
		t.Errorf("unjudged = %+v", js.Unjudged)
	}
}

func TestOlderCheckpointLoadsWithEmptyAttemptMaps(t *testing.T) {
	blob := []byte(`{"scores":{"s-a":88},"complete":false}`)
	c := loadDeferredCache(fakeStore{objs: map[string][]byte{deferredSidecarKey(testRunPrefix): blob}}, testRunPrefix)
	if c.Attempts == nil || c.Unjudged == nil || c.Scores["s-a"] != 88 {
		t.Errorf("older checkpoint loaded as %+v", c)
	}
}
