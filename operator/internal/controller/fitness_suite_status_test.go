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
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// maximalResults builds n score and scenario entries at the field maximums:
// names near the 253-byte cap made of 4-byte characters, and 200-character
// reasons of 4-byte characters.
func maximalResults(n int) (*kubemootv1alpha1.FitnessJudgeStatus, []kubemootv1alpha1.SuiteScenarioResult) {
	js := &kubemootv1alpha1.FitnessJudgeStatus{Phase: kubemootv1alpha1.FitnessJudgePhaseComplete, Judged: int32(n), Total: int32(n)}
	scenarios := make([]kubemootv1alpha1.SuiteScenarioResult, 0, n)
	reason := strings.Repeat("\U0001F600", kubemootv1alpha1.MaxJudgeReasonLength)
	for i := range n {
		name := statusName(fmt.Sprintf("%03d-%s", i, strings.Repeat("\U0001F600", kubemootv1alpha1.MaxStatusNameLength)))
		js.Scores = append(js.Scores, kubemootv1alpha1.FitnessJudgeScore{Scenario: name, Score: 100, Reason: reason})
		scenarios = append(scenarios, kubemootv1alpha1.SuiteScenarioResult{
			Name: name, Iterations: 1000, Passed: 400, Failed: 300, Errored: 300,
			MeanDurationMs: 1 << 40, AssertionsPassed: 100000, AssertionsTotal: 100000,
		})
	}
	return js, scenarios
}

// Names of the suite run the judge-pass helpers (newWorkerPass) use.
const (
	wpSuite    = "suite"
	wpPrefix   = "ns/suite/run/"
	wpFirstKey = wpPrefix + "s0-i1.json"
)

func scripts(refs ...string) []kubemootv1alpha1.SuiteScript {
	out := make([]kubemootv1alpha1.SuiteScript, 0, len(refs))
	for _, r := range refs {
		out = append(out, kubemootv1alpha1.SuiteScript{TestRef: r})
	}
	return out
}

func TestScenarioResultsRollsUpInSpecOrder(t *testing.T) {
	results := []IterationResult{
		{Scenario: "b", Phase: kubemootv1alpha1.CrewFitnessPhasePassed, DurationMs: 100, AssertionsPassed: 2, AssertionsTotal: 2},
		{Scenario: "b", Phase: kubemootv1alpha1.CrewFitnessPhaseFailed, DurationMs: 300, AssertionsPassed: 1, AssertionsTotal: 2},
		{Scenario: "a", Phase: kubemootv1alpha1.CrewFitnessPhaseError, DurationMs: 50},
		{Scenario: "not-in-spec", Phase: kubemootv1alpha1.CrewFitnessPhasePassed},
	}
	got := scenarioResults(scripts("a", "b", "a", "c"), results)
	want := []kubemootv1alpha1.SuiteScenarioResult{
		{Name: "a", Iterations: 1, Errored: 1, MeanDurationMs: 50},
		{Name: "b", Iterations: 2, Passed: 1, Failed: 1, MeanDurationMs: 200, AssertionsPassed: 3, AssertionsTotal: 4},
		{Name: "c"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("scenarioResults = %+v\nwant %+v", got, want)
	}
}

func TestScenarioResultsCapsRowsAndNames(t *testing.T) {
	refs := make([]string, 0, kubemootv1alpha1.MaxStatusScenarios+50)
	for i := range kubemootv1alpha1.MaxStatusScenarios + 50 {
		refs = append(refs, fmt.Sprintf("s%d", i))
	}
	refs[0] = strings.Repeat("n", kubemootv1alpha1.MaxStatusNameLength+10)
	got := scenarioResults(scripts(refs...), nil)
	if len(got) != kubemootv1alpha1.MaxStatusScenarios {
		t.Fatalf("rows = %d, want the cap %d", len(got), kubemootv1alpha1.MaxStatusScenarios)
	}
	if n := len(got[0].Name); n != kubemootv1alpha1.MaxStatusNameLength {
		t.Errorf("a long name is cut to %d characters, got %d", kubemootv1alpha1.MaxStatusNameLength, n)
	}
	if got[kubemootv1alpha1.MaxStatusScenarios-1].Name != fmt.Sprintf("s%d", kubemootv1alpha1.MaxStatusScenarios-1) {
		t.Errorf("the first scripts in spec order are kept, last row %q", got[kubemootv1alpha1.MaxStatusScenarios-1].Name)
	}
	if empty := scenarioResults(nil, nil); len(empty) != 0 {
		t.Errorf("no scripts -> no rows, got %v", empty)
	}
}

func TestStatusNameCutsBytesOnACharacterBoundary(t *testing.T) {
	if got := statusName("short"); got != "short" {
		t.Errorf("a short name is kept, got %q", got)
	}
	got := statusName("a" + strings.Repeat("\U0001F600", 100)) // 401 bytes
	if len(got) > kubemootv1alpha1.MaxStatusNameLength || !utf8.ValidString(got) {
		t.Errorf("name cut to %d bytes (valid UTF-8 %v), want <= %d and valid",
			len(got), utf8.ValidString(got), kubemootv1alpha1.MaxStatusNameLength)
	}
	if len(got) != 1+4*63 {
		t.Errorf("the cut keeps every whole character that fits, got %d bytes", len(got))
	}
}

func TestJudgeStatusCurrent(t *testing.T) {
	judging := func(judged int32) *kubemootv1alpha1.FitnessJudgeStatus {
		return &kubemootv1alpha1.FitnessJudgeStatus{Phase: kubemootv1alpha1.FitnessJudgePhaseJudging, Judged: judged}
	}
	cases := []struct {
		name         string
		cur, desired *kubemootv1alpha1.FitnessJudgeStatus
		want         bool
	}{
		{rjAbsent, nil, judging(0), false},
		{"equal", judging(2), judging(2), true},
		{"progress", judging(1), judging(2), false},
		{"checkpoint expired", judging(3), judging(0), true},
		{"cancelled", judging(3), &kubemootv1alpha1.FitnessJudgeStatus{Phase: kubemootv1alpha1.FitnessJudgePhaseSkipped}, false},
		{"final", &kubemootv1alpha1.FitnessJudgeStatus{Phase: kubemootv1alpha1.FitnessJudgePhaseComplete}, judging(5), true},
	}
	for _, c := range cases {
		if got := judgeStatusCurrent(c.cur, c.desired); got != c.want {
			t.Errorf("%s: judgeStatusCurrent = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLiveReaderPrefersTheAPIReader(t *testing.T) {
	cached := fake.NewClientBuilder().Build()
	r := &CrewFitnessSuiteReconciler{Client: cached}
	if r.liveReader() != client.Reader(cached) {
		t.Error("without an APIReader the client is used")
	}
	live := fake.NewClientBuilder().Build()
	r.APIReader = live
	if r.liveReader() != client.Reader(live) {
		t.Error("the APIReader is used when set")
	}
}

func TestOneLineReason(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"short and sweet", "short and sweet"},
		{"line one\n\tline  two\r\n", "line one line two"},
	}
	for _, c := range cases {
		if got := oneLineReason(c.in); got != c.want {
			t.Errorf("oneLineReason(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	long := oneLineReason(strings.Repeat("é", 500))
	if n := utf8.RuneCountInString(long); n != kubemootv1alpha1.MaxJudgeReasonLength {
		t.Errorf("a long reason is cut to %d characters, got %d", kubemootv1alpha1.MaxJudgeReasonLength, n)
	}
	if !strings.HasSuffix(long, "...") || !utf8.ValidString(long) {
		t.Errorf("a cut reason ends with ... and stays valid UTF-8: %q", long)
	}
	exact := strings.Repeat("x", kubemootv1alpha1.MaxJudgeReasonLength)
	if got := oneLineReason(exact); got != exact {
		t.Error("a reason at the limit is kept whole")
	}
}

func TestRoundScoreClamps(t *testing.T) {
	for in, want := range map[float64]int32{-5: 0, 0: 0, 0.4: 0, 49.5: 50, 87.44: 87, 100: 100, 140: 100} {
		if got := roundScore(in); got != want {
			t.Errorf("roundScore(%v) = %d, want %d", in, got, want)
		}
	}
}

func suiteIn(phase kubemootv1alpha1.CrewFitnessSuitePhase, refs ...string) *kubemootv1alpha1.CrewFitnessSuite {
	return &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: wpSuite, Namespace: "ns"},
		Spec:       kubemootv1alpha1.CrewFitnessSuiteSpec{Scripts: scripts(refs...)},
		Status:     kubemootv1alpha1.CrewFitnessSuiteStatus{Phase: phase, RunID: "run"},
	}
}

func TestJudgePhaseFor(t *testing.T) {
	cases := []struct {
		phase    kubemootv1alpha1.CrewFitnessSuitePhase
		complete bool
		want     kubemootv1alpha1.FitnessJudgePhase
	}{
		{kubemootv1alpha1.CrewFitnessSuitePhaseRunning, false, kubemootv1alpha1.FitnessJudgePhasePending},
		{kubemootv1alpha1.CrewFitnessSuitePhasePaused, false, kubemootv1alpha1.FitnessJudgePhasePending},
		{kubemootv1alpha1.CrewFitnessSuitePhaseCompleted, false, kubemootv1alpha1.FitnessJudgePhaseJudging},
		{kubemootv1alpha1.CrewFitnessSuitePhaseCompleted, true, kubemootv1alpha1.FitnessJudgePhaseComplete},
		{kubemootv1alpha1.CrewFitnessSuitePhaseCancelled, true, kubemootv1alpha1.FitnessJudgePhaseSkipped},
	}
	for _, c := range cases {
		if got := judgePhaseFor(suiteIn(c.phase), c.complete); got != c.want {
			t.Errorf("judgePhaseFor(%s, complete=%v) = %s, want %s", c.phase, c.complete, got, c.want)
		}
	}
}

func countsSuite() (*kubemootv1alpha1.CrewFitnessSuite, deferredScoreCache) {
	suite := suiteIn(kubemootv1alpha1.CrewFitnessSuitePhaseCompleted, "z", "a", "unscored", "a")
	cache := deferredScoreCache{
		Scores:  map[string]float64{"a": 80.6, "z": 0, "stale": 50},
		Reasons: map[string]string{"z": "all gated"},
		Total:   2,
	}
	return suite, cache
}

func TestJudgeStatusForCountsAndOrder(t *testing.T) {
	suite, cache := countsSuite()
	js := judgeStatusFor(suite, cache, metav1.Now())
	got := fmt.Sprintf("%s judged=%d total=%d zeros=%d mean=%d completedAt=%v",
		js.Phase, js.Judged, js.Total, js.Zeros, ptr.Deref(js.Mean, -1), js.CompletedAt)
	if want := "Judging judged=2 total=2 zeros=1 mean=40 completedAt=<nil>"; got != want {
		t.Errorf("judge status %s, want %s", got, want)
	}
	want := []kubemootv1alpha1.FitnessJudgeScore{{Scenario: "z", Reason: "all gated"}, {Scenario: "a", Score: 81}}
	if fmt.Sprint(js.Scores) != fmt.Sprint(want) {
		t.Errorf("scores = %+v, want %+v", js.Scores, want)
	}
}

func TestJudgeStatusForTotalFallsBackToJudged(t *testing.T) {
	suite, cache := countsSuite()
	cache.Total = 0 // an older checkpoint has no total
	if js := judgeStatusFor(suite, cache, metav1.Now()); js.Total != 2 {
		t.Errorf("total falls back to the judged count, got %d", js.Total)
	}
	if js := judgeStatusFor(suite, deferredScoreCache{}, metav1.Now()); js.Judged != 0 || js.Mean != nil || js.Scores != nil {
		t.Errorf("an empty checkpoint gives an empty status, got %+v", js)
	}
}

func TestJudgeStatusForCompletedAt(t *testing.T) {
	suite, cache := countsSuite()
	cache.Complete = true
	now := metav1.NewTime(time.Unix(1000, 0))
	if js := judgeStatusFor(suite, cache, now); js.CompletedAt == nil || !js.CompletedAt.Equal(&now) {
		t.Errorf("completedAt = %v, want %v", js.CompletedAt, now)
	}
	earlier := metav1.NewTime(time.Unix(10, 0))
	suite.Status.Judge = &kubemootv1alpha1.FitnessJudgeStatus{Phase: kubemootv1alpha1.FitnessJudgePhaseComplete, CompletedAt: &earlier}
	if js := judgeStatusFor(suite, cache, now); !js.CompletedAt.Equal(&earlier) {
		t.Errorf("an existing completedAt is kept, got %v", js.CompletedAt)
	}
}

func TestJudgeStatusForSkippedAndCapped(t *testing.T) {
	cancelled := suiteIn(kubemootv1alpha1.CrewFitnessSuitePhaseCancelled, "a")
	js := judgeStatusFor(cancelled, deferredScoreCache{Scores: map[string]float64{"a": 90}}, metav1.Now())
	if js.Phase != kubemootv1alpha1.FitnessJudgePhaseSkipped || js.Judged != 0 || js.Scores != nil {
		t.Errorf("a cancelled suite reports Skipped and nothing else, got %+v", js)
	}

	n := kubemootv1alpha1.MaxStatusScenarios + 20
	refs := make([]string, 0, n)
	cache := deferredScoreCache{Scores: map[string]float64{}, Complete: true}
	for i := range n {
		ref := fmt.Sprintf("s%d", i)
		refs = append(refs, ref)
		cache.Scores[ref] = 50
	}
	js = judgeStatusFor(suiteIn(kubemootv1alpha1.CrewFitnessSuitePhaseCompleted, refs...), cache, metav1.Now())
	if len(js.Scores) != kubemootv1alpha1.MaxStatusScenarios {
		t.Errorf("scores are capped at %d, got %d", kubemootv1alpha1.MaxStatusScenarios, len(js.Scores))
	}
	if js.Judged != int32(n) || js.Total != int32(n) || ptr.Deref(js.Mean, -1) != 50 {
		t.Errorf("counts cover every scenario past the cap, got %+v", *js)
	}
}

func TestJudgeStatusFrozen(t *testing.T) {
	for phase, want := range map[kubemootv1alpha1.FitnessJudgePhase]bool{
		kubemootv1alpha1.FitnessJudgePhasePending:  false,
		kubemootv1alpha1.FitnessJudgePhaseJudging:  false,
		kubemootv1alpha1.FitnessJudgePhaseComplete: true,
		kubemootv1alpha1.FitnessJudgePhaseSkipped:  true,
	} {
		if got := judgeStatusFrozen(&kubemootv1alpha1.FitnessJudgeStatus{Phase: phase}); got != want {
			t.Errorf("judgeStatusFrozen(%s) = %v, want %v", phase, got, want)
		}
	}
	if judgeStatusFrozen(nil) {
		t.Error("an absent status.judge is not final")
	}
}

func TestMaximalStatusStaysUnderBound(t *testing.T) {
	js, scenarios := maximalResults(kubemootv1alpha1.MaxStatusScenarios)
	raw, err := json.Marshal(kubemootv1alpha1.CrewFitnessSuiteStatus{Judge: js, Scenarios: scenarios})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= 500*1024 {
		t.Errorf("worst-case status is %d bytes, want under 500 KiB", len(raw))
	}
	t.Logf("worst-case results: %d bytes for %d scenarios", len(raw), kubemootv1alpha1.MaxStatusScenarios)

	// Typical: 30-character names and full 200-character ASCII reasons.
	js.Scores, scenarios = js.Scores[:0], scenarios[:0]
	for i := range kubemootv1alpha1.MaxStatusScenarios {
		name := fmt.Sprintf("scenario-%03d-%s", i, strings.Repeat("n", 17))
		js.Scores = append(js.Scores, kubemootv1alpha1.FitnessJudgeScore{Scenario: name, Score: 87, Reason: strings.Repeat("r", 200)})
		scenarios = append(scenarios, kubemootv1alpha1.SuiteScenarioResult{Name: name, Iterations: 15, Passed: 12, Failed: 2, Errored: 1, MeanDurationMs: 95000, AssertionsPassed: 70, AssertionsTotal: 75})
	}
	typical, _ := json.Marshal(kubemootv1alpha1.CrewFitnessSuiteStatus{Judge: js, Scenarios: scenarios})
	if len(typical) >= 200*1024 {
		t.Errorf("typical status is %d bytes, want under 200 KiB", len(typical))
	}
	t.Logf("typical results: %d bytes for %d scenarios", len(typical), kubemootv1alpha1.MaxStatusScenarios)
}

func TestNewJudgeStatusNeedsAStore(t *testing.T) {
	r := &CrewFitnessSuiteReconciler{}
	if js := r.newJudgeStatus(kubemootv1alpha1.FitnessJudgePhasePending); js != nil {
		t.Errorf("no artifact store means no judge status, got %+v", js)
	}
	r.artifacts = fakeStore{objs: map[string][]byte{}}
	if js := r.newJudgeStatus(kubemootv1alpha1.FitnessJudgePhaseSkipped); js == nil || js.Phase != kubemootv1alpha1.FitnessJudgePhaseSkipped {
		t.Errorf("newJudgeStatus = %+v, want Skipped", js)
	}
}

// statusPass is a judge pass whose reconciler client holds the given suite.
func statusPass(t *testing.T, suite *kubemootv1alpha1.CrewFitnessSuite, objs ...client.Object) (*judgePass, client.Client) {
	t.Helper()
	cli := fake.NewClientBuilder().WithScheme(agentReconcileScheme(t)).
		WithObjects(objs...).WithStatusSubresource(&kubemootv1alpha1.CrewFitnessSuite{}).Build()
	return &judgePass{
		r:      &CrewFitnessSuiteReconciler{Client: cli, artifacts: fakeStore{objs: map[string][]byte{}}},
		suite:  suite,
		cache:  deferredScoreCache{Scores: map[string]float64{"a": 70}, Reasons: map[string]string{}, Total: 2},
		hc:     &http.Client{},
		log:    logf.Log,
		prefix: wpPrefix,
	}, cli
}

func TestPublishStatusWritesTheLiveSuite(t *testing.T) {
	suite := suiteIn(kubemootv1alpha1.CrewFitnessSuitePhaseCompleted, "a", "b")
	p, cli := statusPass(t, suite.DeepCopy(), suite)
	p.publishStatus()
	live := &kubemootv1alpha1.CrewFitnessSuite{}
	if err := cli.Get(context.Background(), client.ObjectKeyFromObject(suite), live); err != nil {
		t.Fatal(err)
	}
	js := live.Status.Judge
	if js == nil || js.Phase != kubemootv1alpha1.FitnessJudgePhaseJudging || js.Judged != 1 || js.Total != 2 || ptr.Deref(js.Mean, -1) != 70 {
		t.Errorf("status.judge = %+v", js)
	}
}

func TestPublishStatusLeavesAnotherRunAlone(t *testing.T) {
	suite := suiteIn(kubemootv1alpha1.CrewFitnessSuitePhaseCompleted, "a")
	worker := suite.DeepCopy()
	worker.Status.RunID = "older"
	p, cli := statusPass(t, worker, suite)
	p.publishStatus()
	live := &kubemootv1alpha1.CrewFitnessSuite{}
	if err := cli.Get(context.Background(), client.ObjectKeyFromObject(suite), live); err != nil {
		t.Fatal(err)
	}
	if live.Status.Judge != nil {
		t.Errorf("a worker for another run must not write status.judge, got %+v", live.Status.Judge)
	}
}

func TestPublishStatusToleratesAGoneSuite(t *testing.T) {
	p, _ := statusPass(t, suiteIn(kubemootv1alpha1.CrewFitnessSuitePhaseCompleted, "a"))
	p.publishStatus() // NotFound: nothing to write, no panic
	p.r = nil
	p.publishStatus() // no reconciler: a no-op
}

func TestCountJudgeable(t *testing.T) {
	store := fakeStore{objs: map[string][]byte{
		wpFirstKey:              []byte(feasibleTranscript),
		wpPrefix + "s1-i1.json": []byte(`{"assertions":[{"raw":"synthesis is non-empty","passed":true}]}`),
	}}
	p := newWorkerPass(t, store)
	p.suite.Spec.Scripts = scripts("judged", "plain", "never-ran")
	if n := p.countJudgeable(); n != 1 {
		t.Errorf("countJudgeable = %d, want 1 (only the DEFER scenario with a transcript)", n)
	}
}

func TestCountJudgeableCountsADuplicateTestRefOnce(t *testing.T) {
	store := fakeStore{objs: map[string][]byte{
		wpFirstKey:              []byte(feasibleTranscript),
		wpPrefix + "s1-i1.json": []byte(feasibleTranscript),
	}}
	p := newWorkerPass(t, store)
	p.suite.Spec.Scripts = scripts("judged", "judged")
	if n := p.countJudgeable(); n != 1 {
		t.Errorf("countJudgeable = %d, want 1 (scores are keyed by testRef)", n)
	}
}

// listFailStore fails every listing.
type listFailStore struct{ fakeStore }

func (listFailStore) ListObjects(_, _ string) ([]string, error) {
	return nil, fmt.Errorf("bucket unavailable")
}

func TestRejudgeScenariosListingFailureLeavesEmptyRows(t *testing.T) {
	suite := suiteIn(kubemootv1alpha1.CrewFitnessSuitePhaseCompleted, "a", "b")
	got := rejudgeScenarios(listFailStore{fakeStore{objs: map[string][]byte{}}}, suite)
	want := []kubemootv1alpha1.SuiteScenarioResult{{Name: "a"}, {Name: "b"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("rejudgeScenarios = %+v, want %+v", got, want)
	}
}
