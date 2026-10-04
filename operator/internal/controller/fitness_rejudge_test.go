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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/fitnessscript"
)

// lockedStore is a fakeStore safe for the deferred judge worker goroutine and
// the test reading the same objects.
type lockedStore struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func newLockedStore(objs map[string][]byte) *lockedStore {
	return &lockedStore{objs: objs}
}

func (s *lockedStore) ListObjects(_, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fakeStore{objs: s.objs}.ListObjects("", prefix)
}

func (s *lockedStore) GetObject(_, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objs[key], nil
}

func (s *lockedStore) PutObject(_, key string, data []byte, _ time.Duration) (*nats.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objs[key] = data
	return &nats.ObjectInfo{}, nil
}

func (s *lockedStore) DeleteObject(_, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objs, key)
	return nil
}

func (s *lockedStore) get(key string) []byte {
	b, _ := s.GetObject("", key)
	return b
}

const (
	rjAgree     = "agree"
	rjAlpha     = "alpha"
	rjBeta      = "beta"
	rjMissing   = "missing"
	rjAbsent    = "absent"
	rjScenarios = "scenarios"
	rjSrcKey0   = "ns/src/r1/s0-i1.json"
	rjSrcKey1   = "ns/src/r1/s1-i1.json"
	agreeFloor  = "at least 1 specialist contributes with signal=agree"
	oldDefer    = `DEFER synthesis REFLECTS "old reference"`
	newDeferRaw = `DEFER synthesis REFLECTS "new reference"`
)

// sourceTranscript is a stored answered run whose assertions were evaluated
// against the OLD scenario text.
func sourceTranscript(synthesis string) []byte {
	return []byte(`{"question":"q","conversationId":"c1","answered":true,"durationMs":42,` +
		`"assertions":[{"raw":"` + agreeFloor + `","passed":true,"message":"1 agree signal(s) received (need 1)"},` +
		`{"raw":"DEFER synthesis REFLECTS \"old reference\"","passed":true,"message":"deferred"}],` +
		`"events":[{"type":"finding","agent":"k8s","signal":"agree","content":"data"},` +
		`{"type":"synthesis","content":"` + synthesis + `"},{"type":"done"}]}`)
}

// newScenario is the CURRENT scenario text: same agree floor, a new
// deterministic assertion, and a new reference.
func newScenario(extra string) string {
	return "DEFINE CONST QUESTION AS \"q\"\nASSERT(" + agreeFloor + ")\nASSERT(" + extra + ")\nASSERT(" + newDeferRaw + ")\n"
}

func TestReassertKeepsIdenticalAndEvaluatesChanged(t *testing.T) {
	ft := fitnessscript.ParseFitnessTest(newScenario(`synthesis CONTAINS "cilium"`))
	source := []fitnessscript.AssertionResult{
		// Recorded as failed at run time: an identical assertion keeps this result
		// as-is even though the stored events would now evaluate it as passing.
		{Raw: agreeFloor, Passed: false, Message: "recorded"},
		{Raw: oldDefer, Passed: true, Message: "deferred"},
	}
	state := fitnessscript.RunState{
		PostOK:    true,
		Events:    []fitnessscript.SignalEvent{{Type: testFinding, Signal: rjAgree}, {Type: testDone}},
		Synthesis: "no gateway here",
	}
	got := reassert(ft.Assertions, source, state)
	if len(got) != 3 {
		t.Fatalf("want one result per current assertion (3), got %d", len(got))
	}
	if got[0].Passed || got[0].Message != "recorded" {
		t.Errorf("identical assertion must keep the source result, got %+v", got[0])
	}
	if got[1].Passed || !strings.HasSuffix(got[1].Message, rejudgedNote) {
		t.Errorf("new CONTAINS must be evaluated (fail) and marked, got %+v", got[1])
	}
	if got[2].Raw != newDeferRaw || !got[2].Passed {
		t.Errorf("new DEFER must carry the new reference, got %+v", got[2])
	}
}

func TestRejudgeTranscriptRewritesAssertionsAndKeepsFields(t *testing.T) {
	ft := fitnessscript.ParseFitnessTest(newScenario(`synthesis CONTAINS "cilium"`))
	from := rejudgedFrom{Suite: testSourceSuite, RunID: "r1", Key: rjSrcKey0}
	out, phase, ok := rejudgeTranscript(sourceTranscript("cilium is up"), &ft, from)
	if !ok {
		t.Fatal("a valid transcript must rewrite")
	}
	if phase != kubemootv1alpha1.CrewFitnessPhasePassed {
		t.Errorf("all current assertions pass on this answer, phase = %s", phase)
	}
	var doc struct {
		ConversationID string             `json:"conversationId"`
		DurationMs     int64              `json:"durationMs"`
		RejudgedFrom   rejudgedFrom       `json:"rejudgedFrom"`
		Events         []transcriptEvent  `json:"events"`
		Assertions     []transcriptAssert `json:"assertions"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("rewritten transcript is not JSON: %v", err)
	}
	if doc.ConversationID != "c1" || doc.DurationMs != 42 || len(doc.Events) != 3 {
		t.Errorf("source fields must be kept, got %+v", doc)
	}
	if doc.RejudgedFrom != from {
		t.Errorf("rejudgedFrom = %+v, want %+v", doc.RejudgedFrom, from)
	}
	td := transcriptDoc{Assertions: make([]transcriptAssertion, 0, len(doc.Assertions))}
	for _, a := range doc.Assertions {
		td.Assertions = append(td.Assertions, transcriptAssertion(a))
	}
	if ref := referenceForKeyword(td, deferKeyword(newDeferRaw)); ref != "new reference" {
		t.Errorf("the judge must read the current reference, got %q", ref)
	}
}

// transcriptAssert mirrors transcriptAssertion for decoding in the test.
type transcriptAssert struct {
	Raw     string `json:"raw"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
}

func TestRejudgeTranscriptRejectsNonTranscripts(t *testing.T) {
	ft := fitnessscript.ParseFitnessTest(newScenario(testSynthesisNonEmpty))
	for _, data := range [][]byte{nil, []byte("not json"), []byte(`[1,2]`)} {
		if _, _, ok := rejudgeTranscript(data, &ft, rejudgedFrom{}); ok {
			t.Errorf("%q must not be accepted as a transcript", data)
		}
	}
}

func TestSourceTranscriptsByScenarioAndPlan(t *testing.T) {
	source := &kubemootv1alpha1.CrewFitnessSuite{Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
		Scripts: []kubemootv1alpha1.SuiteScript{{TestRef: rjAlpha}, {TestRef: rjBeta}},
	}}
	keys := []string{
		rjSrcKey0, "ns/src/r1/s0-i2.json", rjSrcKey1,
		"ns/src/r1/s9-i1.json",              // index outside the source scripts
		"ns/src/r1/deferred-scores-v2.json", // checkpoint, not a transcript
	}
	by := sourceTranscriptsByScenario(source, keys)
	if len(by[rjAlpha]) != 2 || len(by[rjBeta]) != 1 || len(by) != 2 {
		t.Fatalf("grouping = %v", by)
	}
	scripts := []kubemootv1alpha1.SuiteScript{{TestRef: rjBeta}, {TestRef: rjMissing}, {TestRef: rjAlpha}}
	tests := make([]fitnessscript.FitnessTest, len(scripts))
	plan := planRejudge("ns/new/r2/", scripts, tests, by)
	if len(plan.copies) != 3 {
		t.Fatalf("want 3 copies, got %+v", plan.copies)
	}
	if plan.copies[0].targetKey != "ns/new/r2/s0-i1.json" || plan.copies[0].sourceKey != rjSrcKey1 {
		t.Errorf("beta must re-index to s0, got %+v", plan.copies[0])
	}
	if plan.copies[2].targetKey != "ns/new/r2/s2-i2.json" {
		t.Errorf("alpha keeps its iteration number, got %+v", plan.copies[2])
	}
	if len(plan.notInSource) != 1 || plan.notInSource[0] != rjMissing {
		t.Errorf("notInSource = %v", plan.notInSource)
	}
}

func TestCopyRejudgeTranscriptsSkipsUnparseable(t *testing.T) {
	ft := fitnessscript.ParseFitnessTest(newScenario(testSynthesisNonEmpty))
	store := fakeStore{objs: map[string][]byte{
		rjSrcKey0: sourceTranscript("an answer"),
		rjSrcKey1: []byte("garbage"),
	}}
	plan := rejudgePlan{copies: []rejudgeCopy{
		{scenario: "good", sourceKey: rjSrcKey0, targetKey: "ns/new/r2/s0-i1.json", test: &ft},
		{scenario: "bad", sourceKey: rjSrcKey1, targetKey: "ns/new/r2/s1-i1.json", test: &ft},
	}}
	p, err := copyRejudgeTranscripts(store, &plan, kubemootv1alpha1.RejudgeSource{Suite: testSourceSuite, RunID: "r1"}, time.Hour)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if p.completed != 1 || p.passed != 1 {
		t.Errorf("progress = %+v", p)
	}
	if _, ok := store.objs["ns/new/r2/s1-i1.json"]; ok {
		t.Error("an unparseable source must not be copied")
	}
	if len(plan.notInSource) != 1 || plan.notInSource[0] != "bad" {
		t.Errorf("a scenario left with no copy is not in source, got %v", plan.notInSource)
	}

	// Nothing parseable at all is a permanent failure.
	only := rejudgePlan{copies: plan.copies[1:]}
	_, err = copyRejudgeTranscripts(store, &only, kubemootv1alpha1.RejudgeSource{Suite: testSourceSuite, RunID: "r1"}, time.Hour)
	var rerr *rejudgeError
	if !errors.As(err, &rerr) || rerr.reason != reasonNoTranscripts {
		t.Errorf("want a NoTranscripts failure, got %v", err)
	}
}

func TestValidateRejudgeSpec(t *testing.T) {
	cases := map[string]kubemootv1alpha1.CrewFitnessSuiteSpec{
		"no runId":   {Rejudge: &kubemootv1alpha1.RejudgeSource{Suite: testSourceSuite}, Scripts: []kubemootv1alpha1.SuiteScript{{}}},
		"self":       {Rejudge: &kubemootv1alpha1.RejudgeSource{Suite: "me", RunID: "r"}, Scripts: []kubemootv1alpha1.SuiteScript{{}}},
		"no scripts": {Rejudge: &kubemootv1alpha1.RejudgeSource{Suite: testSourceSuite, RunID: "r"}},
		"duplicate testRef": {Rejudge: &kubemootv1alpha1.RejudgeSource{Suite: testSourceSuite, RunID: "r"},
			Scripts: []kubemootv1alpha1.SuiteScript{{TestRef: rjAlpha}, {TestRef: rjAlpha}}},
	}
	for name, spec := range cases {
		suite := &kubemootv1alpha1.CrewFitnessSuite{ObjectMeta: metav1.ObjectMeta{Name: "me"}, Spec: spec}
		var rerr *rejudgeError
		if err := validateRejudgeSpec(suite); !errors.As(err, &rerr) || rerr.reason != reasonInvalidRejudge {
			t.Errorf("%s: want InvalidRejudge, got %v", name, err)
		}
	}
	ok := &kubemootv1alpha1.CrewFitnessSuite{ObjectMeta: metav1.ObjectMeta{Name: "me"},
		Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
			Rejudge: &kubemootv1alpha1.RejudgeSource{Suite: testSourceSuite, RunID: "r"},
			Scripts: []kubemootv1alpha1.SuiteScript{{TestRef: "a"}},
		}}
	if err := validateRejudgeSpec(ok); err != nil {
		t.Errorf("valid spec rejected: %v", err)
	}
}

func TestScriptContentReadsConfigMap(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: rjScenarios, Namespace: "ns"},
		Data:       map[string]string{"alpha.adl": "ASSERT(synthesis is non-empty)"},
	}
	cli := fake.NewClientBuilder().WithScheme(resumeScheme(t)).WithObjects(cm).Build()
	r := &CrewFitnessSuiteReconciler{Client: cli}
	ctx := context.Background()

	got, err := r.scriptContent(ctx, "ns", kubemootv1alpha1.SuiteScript{TestRef: rjAlpha, ConfigMapRef: rjScenarios})
	if err != nil || got != cm.Data["alpha.adl"] {
		t.Errorf("configMap script = %q, %v", got, err)
	}
	bad := []kubemootv1alpha1.SuiteScript{
		{TestRef: rjBeta, ConfigMapRef: rjScenarios},            // key missing
		{TestRef: rjAlpha, ConfigMapRef: rjAbsent},              // ConfigMap missing
		{TestRef: rjAlpha},                                      // no source
		{TestRef: rjAlpha, TestContent: "x", ConfigMapRef: "y"}, // two sources
		{TestContent: "x"},                                      // no testRef
	}
	for _, s := range bad {
		var rerr *rejudgeError
		if _, err := r.scriptContent(ctx, "ns", s); !errors.As(err, &rerr) || rerr.reason != reasonScriptUnavailable {
			t.Errorf("script %+v must be a permanent ScriptUnavailable, got %v", s, err)
		}
	}
}

// An API error reading the ConfigMap is transient: it must not become a
// permanent failure that moves the suite to Error.
func TestScriptContentTransientErrorRetries(t *testing.T) {
	cli := fake.NewClientBuilder().WithScheme(resumeScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errors.New("api server timeout")
		},
	}).Build()
	r := &CrewFitnessSuiteReconciler{Client: cli}
	_, err := r.scriptContent(context.Background(), "ns",
		kubemootv1alpha1.SuiteScript{TestRef: rjAlpha, ConfigMapRef: rjScenarios})
	var rerr *rejudgeError
	if err == nil || errors.As(err, &rerr) {
		t.Errorf("want a retryable error, got %v", err)
	}
}

// deferKeyword reads the keyword of a DEFER assertion, as the judge pass does.
func deferKeyword(raw string) string {
	kw, _, _ := parseDeferredAssertion(raw)
	return kw
}
