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
	"net/http"
	"testing"
	"time"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// A feasible iteration: a DEFER REFLECTS assertion, a passing agree floor, and a
// non-empty synthesis -> one judgeable answer.
const feasibleTranscript = `{"question":"q",` +
	`"assertions":[{"raw":"DEFER synthesis REFLECTS \"the reference\"","passed":true},` +
	`{"raw":"at least 1 tooler agree","passed":true}],` +
	`"events":[{"type":"finding","agent":"k8s","signal":"agree","content":"found it"},` +
	`{"type":"synthesis","content":"the answer"}]}`

// A gated iteration: the agree floor FAILED, so the run is consensus-gated and the
// scenario contributes 0 with no judge call.
const gatedTranscript = `{"question":"q",` +
	`"assertions":[{"raw":"DEFER synthesis REFLECTS \"ref\"","passed":true},` +
	`{"raw":"at least 2 toolers agree","passed":false}],` +
	`"events":[{"type":"synthesis","content":"answer"}]}`

func newWorkerPass(t *testing.T, store fakeStore, crews ...client.Object) *judgePass {
	t.Helper()
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crews...).Build()
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: testSuite, Namespace: "ns"},
		Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
			Scripts: []kubemootv1alpha1.SuiteScript{{TestRef: testScenarioX}},
		},
	}
	return &judgePass{
		r:         &CrewFitnessSuiteReconciler{Client: cli},
		store:     store,
		suite:     suite,
		prefix:    testRunPrefix,
		cache:     deferredScoreCache{Scores: map[string]float64{}, Reasons: map[string]string{}},
		endpoints: map[string]string{},
		hc:        &http.Client{Timeout: 500 * time.Millisecond},
		log:       logf.Log,
	}
}

func reflectsCrew() *kubemootv1alpha1.Crew {
	return &kubemootv1alpha1.Crew{
		ObjectMeta: metav1.ObjectMeta{
			Name: "judge-crew", Namespace: "ns",
			Labels: map[string]string{keywordCrewLabel: testVerdictReflects},
		},
	}
}

func TestScenarioKeyword(t *testing.T) {
	store := fakeStore{objs: map[string][]byte{testRunS0I1: []byte(feasibleTranscript)}}
	p := newWorkerPass(t, store)
	kw, ok := p.scenarioKeyword(0)
	if !ok || kw != testVerdictReflects {
		t.Errorf("scenarioKeyword(0) = %q,%v; want REFLECTS,true", kw, ok)
	}
	// No transcript for script index 1 -> not runnable.
	if _, ok := p.scenarioKeyword(1); ok {
		t.Error("scenarioKeyword on a scenario with no transcript should be false")
	}
}

func TestResolveEndpoint(t *testing.T) {
	p := newWorkerPass(t, fakeStore{objs: map[string][]byte{}}, reflectsCrew())
	ep, ok := p.resolveEndpoint(context.Background(), testVerdictReflects)
	if !ok || ep == "" {
		t.Fatalf("resolveEndpoint(REFLECTS) = %q,%v; want a non-empty endpoint", ep, ok)
	}
	// Cached + a keyword no crew declares -> not resolvable, cached as a miss.
	if _, ok := p.resolveEndpoint(context.Background(), "NOPE"); ok {
		t.Error("resolveEndpoint for an undeclared keyword should be false")
	}
	if v, cached := p.endpoints["NOPE"]; !cached || v != "" {
		t.Error("a resolve miss should be cached as empty")
	}
}

func TestProcessScenario_GapWhenNoTranscript(t *testing.T) {
	p := newWorkerPass(t, fakeStore{objs: map[string][]byte{}})
	if out := p.processScenario(context.Background(), 0); out != scenarioHandled {
		t.Errorf("no transcript should be handled (gap), got %v", out)
	}
}

func TestProcessScenario_AllGatedRecordsZero(t *testing.T) {
	store := fakeStore{objs: map[string][]byte{testRunS0I1: []byte(gatedTranscript)}}
	p := newWorkerPass(t, store, reflectsCrew())
	if out := p.processScenario(context.Background(), 0); out != scenarioHandled {
		t.Fatalf("an all-gated scenario should be handled, got %v", out)
	}
	if p.cache.Scores[testScenarioX] != 0 {
		t.Errorf("an all-gated scenario should record quality 0, got %v", p.cache.Scores[testScenarioX])
	}
}

func TestProcessScenario_UnresolvableKeywordPends(t *testing.T) {
	store := fakeStore{objs: map[string][]byte{testRunS0I1: []byte(feasibleTranscript)}}
	p := newWorkerPass(t, store) // no crew declares REFLECTS
	if out := p.processScenario(context.Background(), 0); out != scenarioPending {
		t.Errorf("an unresolved keyword should pend for a later pass, got %v", out)
	}
}

func TestProcessScenario_DispatchFailurePends(t *testing.T) {
	store := fakeStore{objs: map[string][]byte{testRunS0I1: []byte(feasibleTranscript)}}
	p := newWorkerPass(t, store, reflectsCrew())
	// The resolved endpoint is an in-cluster svc URL, unreachable from the test, so
	// the judge dispatch errors and the scenario pends (retry next pass) - it is NOT
	// recorded as a real 0.
	out := p.processScenario(context.Background(), 0)
	if out != scenarioPending {
		t.Errorf("a failed judge dispatch should pend, got %v", out)
	}
	if _, recorded := p.cache.Scores[testScenarioX]; recorded {
		t.Error("a dispatch failure must not record a score")
	}
}
