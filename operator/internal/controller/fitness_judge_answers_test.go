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

	"github.com/go-logr/logr"
	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	answerScenario   = "answers-scenario"
	answerTranscript = `{"question":"q","assertions":[{"raw":"DEFER synthesis REFLECTS \"ref\"","passed":true}],` +
		`"events":[{"type":"finding","agent":"a","signal":"agree","content":"%s"},{"type":"synthesis","content":"answer %d"},{"type":"done"}]}`
)

// oneDocWork is a scenario whose answers fit one document, for a test that
// hands the judge a fixed document.
func oneDocWork(doc string) judgeWork {
	return judgeWork{Batches: []judgeBatch{{Doc: doc, Iterations: []int{1}}}, Total: 1}
}

// transcriptStore holds n iterations of scenario 0, iteration gatedIter failing the agree floor.
func transcriptStore(n, gatedIter int, evidence string) fakeStore {
	objs := map[string][]byte{}
	for i := 1; i <= n; i++ {
		raw := fmt.Sprintf(answerTranscript, evidence, i)
		if i == gatedIter {
			raw = strings.Replace(raw, `"assertions":[`, `"assertions":[{"raw":">=2 toolers agree","passed":false},`, 1)
		}
		objs[fmt.Sprintf("%ss0-i%d.json", testRunPrefix, i)] = []byte(raw)
	}
	return fakeStore{objs: objs}
}

// verdictStream is the judge's event stream for one verdict.
func verdictStream(verdict string) []string {
	content, _ := json.Marshal(verdict)
	return []string{`{"type":"synthesis","content":` + string(content) + `}`, `{"type":"done"}`}
}

func ix(i int) *int { return &i }

func TestAssignAnswerScoresByEchoedIndex(t *testing.T) {
	bv := batchVerdict{Scores: []answerScore{
		{Index: ix(1), Score: 0.5, Fabrication: true, Reason: "invented a VM"},
		{Index: ix(0), Score: 1.0},
	}}
	got := assignAnswerScores(bv, []int{3, 7})
	if got[0].Iteration != 3 || got[0].Score != 100 || got[1].Iteration != 7 || got[1].Score != 50 || !got[1].Fabrication {
		t.Fatalf("scores not mapped by index to iterations: %+v", got)
	}
}

func TestAssignAnswerScoresNeverGuesses(t *testing.T) {
	t.Run("a 1-based judge that drops an answer scores every answer 0", func(t *testing.T) {
		bv := batchVerdict{Scores: []answerScore{{Index: ix(1), Score: 1}, {Index: ix(2), Score: 1}}}
		assertAllUnscored(t, assignAnswerScores(bv, []int{1, 2, 3}))
	})
	t.Run("repeated indexes with one score per answer are read by position", func(t *testing.T) {
		bv := batchVerdict{Scores: []answerScore{{Index: ix(0), Score: 0.2}, {Index: ix(2), Score: 0.8}, {Index: ix(2), Score: 0.6}}}
		got := assignAnswerScores(bv, []int{1, 2, 3})
		if got[0].Score != 20 || got[1].Score != 80 || got[2].Score != 60 {
			t.Fatalf("not read by position: %+v", got)
		}
	})
	t.Run("missing index fields are read by position when the counts match", func(t *testing.T) {
		bv := batchVerdict{Scores: []answerScore{{Score: 0.2}, {Score: 0.8}}}
		got := assignAnswerScores(bv, []int{1, 2})
		if got[0].Score != 20 || got[1].Score != 80 {
			t.Fatalf("not read by position: %+v", got)
		}
	})
	t.Run("one index field missing among several is not a permutation", func(t *testing.T) {
		bv := batchVerdict{Scores: []answerScore{{Index: ix(1), Score: 0.2}, {Score: 0.8}}}
		got := assignAnswerScores(bv, []int{1, 2})
		if got[0].Score != 20 || got[1].Score != 80 { // counts match: position
			t.Fatalf("not read by position: %+v", got)
		}
	})
	t.Run("fewer scores than answers with no indexes scores every answer 0", func(t *testing.T) {
		assertAllUnscored(t, assignAnswerScores(batchVerdict{Scores: []answerScore{{Score: 1}}}, []int{1, 2}))
	})
	t.Run("an out-of-range index among fewer scores scores every answer 0", func(t *testing.T) {
		assertAllUnscored(t, assignAnswerScores(batchVerdict{Scores: []answerScore{{Index: ix(9), Score: 1}}}, []int{1, 2}))
	})
	t.Run("the critical-flaw cap applies per answer", func(t *testing.T) {
		got := assignAnswerScores(batchVerdict{Scores: []answerScore{{Score: 1, Reason: "fabricated the id"}}}, []int{1})
		if got[0].Score != 70 {
			t.Fatalf("score = %v, want the 70 cap", got[0].Score)
		}
	})
}

func assertAllUnscored(t *testing.T, got []answerRecord) {
	t.Helper()
	for _, r := range got {
		if r.Score != 0 || r.Reason != reasonNoAnswerScore {
			t.Fatalf("an answer was scored without a trustworthy index: %+v", got)
		}
	}
}

func TestAnswersMeanReasonAndTally(t *testing.T) {
	recs := []answerRecord{
		{Iteration: 1, Score: 100, Reason: "all facts present"},
		{Iteration: 2, Score: 40, Fabrication: true, Reason: "invented"},
		{Iteration: 3, Gated: true, Reason: reasonGatedAnswer},
	}
	if got := answersMean(recs); got != 140.0/3 {
		t.Errorf("mean = %v", got)
	}
	if got := answersReason(recs); got != "invented" {
		t.Errorf("reason = %q, want the lowest-scoring judged answer's, not a gated one's", got)
	}
	if got := answersReason(recs[2:]); got != reasonGatedAnswer {
		t.Errorf("reason = %q, want the gated reason as the fallback", got)
	}
	tl := tallyAnswers(recs)
	if tl.answers != 3 || tl.gated != 1 || tl.fabrications != 1 {
		t.Errorf("tally = %+v", tl)
	}
	if answersMean(nil) != 0 {
		t.Error("mean of no answers must be 0")
	}
}

func TestBuildComparisonDocOrdersIterationsNumerically(t *testing.T) {
	store := transcriptStore(11, 0, "e")
	work, err := buildComparisonDoc(store, testRunPrefix, 0, testVerdictReflects, 0, logr.Discard())
	if err != nil || len(work.Batches) != 1 {
		t.Fatalf("err %v, batches %d", err, len(work.Batches))
	}
	its := work.Batches[0].Iterations
	if len(its) != 11 || its[1] != 2 || its[9] != 10 || its[10] != 11 {
		t.Errorf("iterations = %v, want 1..11 in numeric order", its)
	}
	var doc judgeComparisonDoc
	if json.Unmarshal([]byte(work.Batches[0].Doc), &doc) != nil || len(doc.Answers) != 11 || doc.Answers[9].Synthesis != "answer 10" {
		t.Errorf("document answers are not in iteration order: %+v", doc.Answers)
	}
}

// Answers that do not fit the judge's window together are split over several
// documents; every answer lands in exactly one, in order, and a split document
// counts only its own answers.
func TestBuildComparisonDocChunksToTheWindow(t *testing.T) {
	store := transcriptStore(6, 4, strings.Repeat("x", 3000))
	// A window of 4096 tokens holds about two answers of this size.
	work, err := buildComparisonDoc(store, testRunPrefix, 0, testVerdictReflects, 4096, logr.Discard())
	if err != nil {
		t.Fatal(err)
	}
	if len(work.Batches) < 2 {
		t.Fatalf("want the answers split over several documents, got %d", len(work.Batches))
	}
	var all []int
	for _, b := range work.Batches {
		all = append(all, b.Iterations...)
		assertSplitDocument(t, b, 4096)
	}
	if fmt.Sprint(all) != "[1 2 3 5 6]" || fmt.Sprint(work.Gated) != "[4]" || work.Total != 6 {
		t.Errorf("answers %v gated %v total %d, want [1 2 3 5 6], [4], 6", all, work.Gated, work.Total)
	}
}

// assertSplitDocument checks one document of a split scenario: within the window's
// budget and counting only its own answers.
func assertSplitDocument(t *testing.T, b judgeBatch, windowTokens int) {
	t.Helper()
	budget, _ := judgeDocBudgetChars(windowTokens)
	if len(b.Doc) > budget {
		t.Errorf("document of %d chars exceeds the budget %d", len(b.Doc), budget)
	}
	var doc judgeComparisonDoc
	if json.Unmarshal([]byte(b.Doc), &doc) != nil || doc.Gated != 0 || doc.Total != len(b.Iterations) || len(doc.Answers) != len(b.Iterations) {
		t.Errorf("a split document must count only its own answers: %+v", doc)
	}
}

func TestPlanBatchesOneDocumentKeepsGatedCounts(t *testing.T) {
	base := judgeComparisonDoc{Question: "q", Reference: "r", Gated: 2, Total: 3}
	items := []answerOf{{iteration: 2, answer: judgeAnswer{Synthesis: "s"}}}
	batches, err := planBatches(base, items, 0, logr.Discard())
	if err != nil || len(batches) != 1 {
		t.Fatalf("err %v batches %d", err, len(batches))
	}
	var doc judgeComparisonDoc
	_ = json.Unmarshal([]byte(batches[0].Doc), &doc)
	if doc.Gated != 2 || doc.Total != 3 {
		t.Errorf("the one document must keep the scenario's gated and total, got %+v", doc)
	}
	if b, _ := planBatches(base, nil, 0, logr.Discard()); b != nil {
		t.Error("no answers, no documents")
	}
}

func TestJudgeBatchesDispatchesEachDocumentAndZeroesANoVerdict(t *testing.T) {
	srv, _ := judgeServer(t,
		verdictStream(`{"scores":[{"index":0,"score":1.0},{"index":1,"score":0.5,"fabrication":true}],"total":2}`),
		verdictStream(`{"scores":[],"total":0}`)) // no verdict, on every retry
	p := newTestJudgePass(fakeStore{objs: map[string][]byte{}})
	p.hc = srv.Client()
	work := judgeWork{Batches: []judgeBatch{
		{Doc: "{}", Iterations: []int{1, 2}},
		{Doc: "{}", Iterations: []int{3}},
	}}
	err := p.judgeBatches(t.Context(), answerScenario, srv.URL, work)
	recs := p.cache.Parts[answerScenario]
	if err != nil || len(recs) != 3 {
		t.Fatalf("err %v recs %+v", err, recs)
	}
	if recs[0].Score != 100 || recs[1].Score != 50 || !recs[1].Fabrication || recs[2].Score != 0 || recs[2].Reason != reasonNoVerdict {
		t.Errorf("records = %+v", recs)
	}
}

func TestJudgeAndRecordKeepsEveryAnswer(t *testing.T) {
	srv, _ := judgeServer(t, verdictStream(`{"scores":[{"index":0,"score":1.0},{"index":1,"score":0.6,"fabrication":true,"reason":"invented a name"}],"total":3}`))
	p := newTestJudgePass(fakeStore{objs: map[string][]byte{}})
	p.hc = srv.Client()
	work := judgeWork{Batches: []judgeBatch{{Doc: "{}", Iterations: []int{1, 3}}}, Gated: []int{2}, Total: 3}
	if out := p.judgeAndRecord(t.Context(), answerScenario, testVerdictReflects, srv.URL, work); out != scenarioHandled {
		t.Fatalf("outcome %v", out)
	}
	recs := p.cache.Answers[answerScenario]
	if len(recs) != 3 || recs[0].Iteration != 1 || recs[1].Iteration != 2 || !recs[1].Gated || recs[2].Iteration != 3 || !recs[2].Fabrication {
		t.Fatalf("records = %+v", recs)
	}
	if want := (100.0 + 0 + 60) / 3; p.cache.Scores[answerScenario] != want {
		t.Errorf("scenario score = %v, want the mean of the three answers %v", p.cache.Scores[answerScenario], want)
	}
	if p.cache.Reasons[answerScenario] != "invented a name" {
		t.Errorf("reason = %q", p.cache.Reasons[answerScenario])
	}
}

func TestJudgeStatusCarriesAnswers(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{}
	suite.Spec.Scripts = []kubemootv1alpha1.SuiteScript{{TestRef: "a"}, {TestRef: "b"}, {TestRef: "c"}}
	cache := deferredScoreCache{
		Scores: map[string]float64{"a": 80, "b": 50},
		Answers: map[string][]answerRecord{
			"a": {{Iteration: 1, Score: 100}, {Iteration: 2, Score: 60, Fabrication: true}},
			"b": {{Iteration: 1, Score: 50}, {Iteration: 2, Gated: true}, {Iteration: 3, Score: 100}, {Iteration: 4, Score: 50}},
		},
	}
	js := judgeStatusFor(suite, cache, metav1.Now())
	if js.Judged != 2 || js.JudgedAnswers != 6 || js.Fabrications != 1 || js.Mean == nil || *js.Mean != 65 || js.AnswerMean == nil || *js.AnswerMean != 60 {
		t.Fatalf("aggregates = %+v", js)
	}
	assertScenarioEntries(t, js.Scores)
}

// assertScenarioEntries checks the two scenario entries of TestJudgeStatusCarriesAnswers.
func assertScenarioEntries(t *testing.T, scores []kubemootv1alpha1.FitnessJudgeScore) {
	t.Helper()
	a, b := scores[0], scores[1]
	if a.Answers != 2 || a.Fabrications != 1 {
		t.Errorf("scenario a = %+v", a)
	}
	if b.Answers != 4 || b.Gated != 1 || b.Fabrications != 0 {
		t.Errorf("scenario b = %+v", b)
	}
}

// A checkpoint written before answers were kept still yields the scenario
// aggregates and leaves the answer fields empty.
func TestJudgeStatusFromOlderCheckpoint(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{}
	suite.Spec.Scripts = []kubemootv1alpha1.SuiteScript{{TestRef: "a"}}
	var cache deferredScoreCache
	if err := json.Unmarshal([]byte(`{"scores":{"a":90},"complete":true}`), &cache); err != nil {
		t.Fatal(err)
	}
	js := judgeStatusFor(suite, cache, metav1.Now())
	if js.Judged != 1 || *js.Mean != 90 || js.JudgedAnswers != 0 || js.AnswerMean != nil || js.Scores[0].Answers != 0 {
		t.Errorf("status = %+v", js)
	}
}

// With one scenario judged before answers were kept, the answer mean would cover
// only some scenarios, so it is left out; the answer counts stay.
func TestJudgeStatusOmitsAnswerMeanWhenAScenarioHasNoAnswers(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{}
	suite.Spec.Scripts = []kubemootv1alpha1.SuiteScript{{TestRef: "a"}, {TestRef: "b"}}
	cache := deferredScoreCache{
		Scores:  map[string]float64{"a": 90, "b": 40},
		Answers: map[string][]answerRecord{"a": {{Iteration: 1, Score: 90}}},
	}
	js := judgeStatusFor(suite, cache, metav1.Now())
	if js.AnswerMean != nil || js.JudgedAnswers != 1 || *js.Mean != 65 {
		t.Errorf("status = %+v", js)
	}
}

// Every part of a split scenario carries the same question and reference, and each
// answer carries its index in its own document.
func TestChunksShareQuestionAndReferenceAndIndexAnswers(t *testing.T) {
	store := transcriptStore(6, 0, strings.Repeat("x", 3000))
	work, err := buildComparisonDoc(store, testRunPrefix, 0, testVerdictReflects, 4096, logr.Discard())
	if err != nil || len(work.Batches) < 2 {
		t.Fatalf("err %v batches %d", err, len(work.Batches))
	}
	for _, b := range work.Batches {
		var doc judgeComparisonDoc
		if json.Unmarshal([]byte(b.Doc), &doc) != nil || doc.Question != "q" || doc.Reference != "ref" {
			t.Errorf("part lost the question or reference: %+v", doc)
		}
		for i, a := range doc.Answers {
			if a.Index != i {
				t.Errorf("answer %d carries index %d", i, a.Index)
			}
		}
	}
}

// A key that is not an iteration transcript is not an answer.
func TestBuildComparisonDocSkipsKeysWithoutAnIteration(t *testing.T) {
	store := transcriptStore(2, 0, "e")
	store.objs[testRunPrefix+"s0-ix.json"] = store.objs[testRunPrefix+"s0-i1.json"]
	work, err := buildComparisonDoc(store, testRunPrefix, 0, testVerdictReflects, 0, logr.Discard())
	if err != nil || work.Total != 2 || fmt.Sprint(work.Batches[0].Iterations) != "[1 2]" {
		t.Fatalf("err %v work %+v", err, work)
	}
}

// A failure in a later part keeps the earlier part, and the next attempt
// dispatches only what is missing.
func TestJudgeBatchesKeepsFinishedPartsWhenALaterPartFails(t *testing.T) {
	srv, posts := judgeServer(t,
		verdictStream(`{"scores":[{"index":0,"score":1.0},{"index":1,"score":0.5}],"total":2}`),
		[]string{`{"type":"done"}`}) // part 2: the stream ends with no synthesis
	p := newTestJudgePass(fakeStore{objs: map[string][]byte{}})
	p.hc = srv.Client()
	work := judgeWork{Batches: []judgeBatch{
		{Doc: "{}", Iterations: []int{1, 2}},
		{Doc: "{}", Iterations: []int{3}},
	}}
	if err := p.judgeBatches(t.Context(), answerScenario, srv.URL, work); err == nil {
		t.Fatal("part 2 must fail")
	}
	if got := len(p.cache.Parts[answerScenario]); got != 2 {
		t.Fatalf("part 1 not kept: %+v", p.cache.Parts)
	}
	if blob := loadDeferredCache(p.store, p.prefix); len(blob.Parts[answerScenario]) != 2 {
		t.Fatalf("part 1 not in the stored checkpoint: %+v", blob.Parts)
	}
	before := posts.Load()
	_ = p.judgeBatches(t.Context(), answerScenario, srv.URL, work)
	if got := posts.Load() - before; got != 1 {
		t.Errorf("the retry dispatched %d documents, want only part 2", got)
	}
}

// A pass cut off during part 2 leaves part 1 scored and checkpointed.
func TestJudgeAndRecordKeepsFinishedPartsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	srv, _ := judgeServer(t, verdictStream(`{"scores":[{"index":0,"score":1.0}],"total":1}`))
	p := newTestJudgePass(fakeStore{objs: map[string][]byte{}})
	p.hc = &http.Client{Transport: cancelAfterFirst{cancel: cancel, rt: srv.Client().Transport, posts: new(int)}}
	work := judgeWork{Batches: []judgeBatch{
		{Doc: "{}", Iterations: []int{1}},
		{Doc: "{}", Iterations: []int{2}},
	}, Total: 2}
	if out := p.judgeAndRecord(ctx, answerScenario, testVerdictReflects, srv.URL, work); out != scenarioBudgetExhausted {
		t.Fatalf("outcome %v, want the budget-exhausted stop", out)
	}
	if got := loadDeferredCache(p.store, p.prefix).Parts[answerScenario]; len(got) != 1 || got[0].Iteration != 1 {
		t.Errorf("finished part lost: %+v", got)
	}
	if p.cache.Attempts[answerScenario] != 0 {
		t.Error("a cut-off dispatch must not count as an attempt")
	}
}

// cancelAfterFirst cancels the pass when the second judge discussion is opened.
type cancelAfterFirst struct {
	cancel func()
	rt     http.RoundTripper
	posts  *int
}

func (c cancelAfterFirst) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPost {
		*c.posts++
		if *c.posts == 2 {
			c.cancel()
		}
	}
	return c.rt.RoundTrip(r)
}
