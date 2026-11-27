/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// fitness_suite_status.go puts a suite run's results on the CrewFitnessSuite
// status, so a client reads them through the Kubernetes API: the per-scenario
// rollup of finished iterations (status.scenarios) and the deferred judge's
// progress and scores (status.judge). Both are bounded (see
// kubemootv1alpha1.MaxStatusScenarios); transcripts, full judge reasons and the
// XLSX stay in the object store.
package controller

import (
	"context"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// judgeStatusWriteTimeout bounds one status write from the judge worker, which
// runs outside any reconcile context.
const judgeStatusWriteTimeout = 30 * time.Second

// scenarioRollup accumulates the finished iterations of one scenario.
type scenarioRollup struct {
	outcomes         childProgress
	durationSum      int64
	assertionsPassed int32
	assertionsTotal  int32
	inventedRuns     int32
	failureReason    string
}

func (a *scenarioRollup) add(r IterationResult) {
	a.outcomes.add(r.Phase)
	a.durationSum += r.DurationMs
	a.assertionsPassed += int32(r.AssertionsPassed)
	a.assertionsTotal += int32(r.AssertionsTotal)
	if len(r.InventedNames) > 0 {
		a.inventedRuns++
	}
	if a.failureReason == "" {
		a.failureReason = iterationFailureReason(r)
	}
}

// iterationFailureReason is the one-line reason a finished iteration did not
// pass its gates: the message of its first failed assertion, or its error when
// it could not run. Empty for a passed iteration.
func iterationFailureReason(r IterationResult) string {
	switch r.Phase {
	case kubemootv1alpha1.CrewFitnessPhasePassed:
		return ""
	case kubemootv1alpha1.CrewFitnessPhaseFailed:
		for _, a := range r.Assertions {
			if !a.Passed {
				return failureLine(a.Message, a.Raw)
			}
		}
		return ""
	default:
		return failureLine(r.Error, "")
	}
}

// failureLine folds a gate message onto one line, falling back to the
// assertion text when the message is empty, and cuts it to
// MaxFailureReasonLength characters.
func failureLine(message, fallback string) string {
	line := strings.Join(strings.Fields(message), " ")
	if line == "" {
		line = strings.Join(strings.Fields(fallback), " ")
	}
	return truncateRunes(line, kubemootv1alpha1.MaxFailureReasonLength, "...")
}

func (a *scenarioRollup) result(name string) kubemootv1alpha1.SuiteScenarioResult {
	out := kubemootv1alpha1.SuiteScenarioResult{Name: name}
	if a == nil || a.outcomes.completed == 0 {
		return out
	}
	out.Iterations = a.outcomes.completed
	out.Passed = a.outcomes.passed
	out.Failed = a.outcomes.failed
	out.Errored = a.outcomes.errored
	out.MeanDurationMs = a.durationSum / int64(a.outcomes.completed)
	out.AssertionsPassed = a.assertionsPassed
	out.AssertionsTotal = a.assertionsTotal
	out.InventedNameRuns = a.inventedRuns
	out.FailureReason = a.failureReason
	out.PassRate = ptr.To(passRatePercent(out.Passed, out.Iterations))
	out.PassAllK = ptr.To(passAllKPercent(out.Passed, out.Iterations))
	out.AllPassed = ptr.To(out.Passed == out.Iterations)
	out.CheckScenario = scenarioNeedsCheck(out.Passed, out.Iterations)
	return out
}

// scenarioResults rolls finished iterations up per scenario, one row per
// distinct script in spec order (a script with no finished iteration gets an
// empty row), capped at MaxStatusScenarios rows.
func scenarioResults(scripts []kubemootv1alpha1.SuiteScript, results []IterationResult) []kubemootv1alpha1.SuiteScenarioResult {
	acc := map[string]*scenarioRollup{}
	for _, r := range results {
		a := acc[r.Scenario]
		if a == nil {
			a = &scenarioRollup{}
			acc[r.Scenario] = a
		}
		a.add(r)
	}
	out := make([]kubemootv1alpha1.SuiteScenarioResult, 0, min(len(scripts), kubemootv1alpha1.MaxStatusScenarios))
	for _, ref := range distinctTestRefs(scripts) {
		if len(out) == kubemootv1alpha1.MaxStatusScenarios {
			break
		}
		out = append(out, acc[ref].result(statusName(ref)))
	}
	return out
}

// addInventedCounts reads the run's transcripts and sets inventedNameRuns on
// status.scenarios. The rows of a running suite come from the finished
// CrewFitness children, which carry no transcript, so the count is added once the
// run is terminal and its transcripts are final. Without a store, with a failed
// listing, or when a transcript could not be read (a transient failure must not
// lower a correct count), the rows keep what they hold.
func addInventedCounts(store objectStore, suite *kubemootv1alpha1.CrewFitnessSuite) {
	if results, ok := readRunResults(store, suite); ok {
		setInventedCounts(suite, results)
	}
}

// setInventedCounts copies the invented-name count of each scenario onto its
// status row. The rows are rolled up by scenarioResults, the one place that counts
// iterations, and only the count is copied.
func setInventedCounts(suite *kubemootv1alpha1.CrewFitnessSuite, results []IterationResult) {
	runs := map[string]int32{}
	for _, row := range scenarioResults(suite.Spec.Scripts, results) {
		runs[row.Name] = row.InventedNameRuns
	}
	for i := range suite.Status.Scenarios {
		suite.Status.Scenarios[i].InventedNameRuns = runs[suite.Status.Scenarios[i].Name]
	}
}

// countTranscriptKeys is how many of the listed keys are transcripts (the run
// prefix also holds the judge checkpoint and other non-transcript objects).
func countTranscriptKeys(keys []string) int {
	n := 0
	for _, k := range keys {
		if _, _, ok := parseTranscriptKey(k); ok && strings.HasSuffix(k, ".json") {
			n++
		}
	}
	return n
}

// distinctTestRefs returns the scripts' testRefs in spec order, each once.
func distinctTestRefs(scripts []kubemootv1alpha1.SuiteScript) []string {
	seen := make(map[string]bool, len(scripts))
	out := make([]string, 0, len(scripts))
	for _, s := range scripts {
		if seen[s.TestRef] {
			continue
		}
		seen[s.TestRef] = true
		out = append(out, s.TestRef)
	}
	return out
}

// statusName cuts a scenario name to MaxStatusNameLength bytes on a character
// boundary. Counting bytes, not characters, keeps the size bound true for
// names in any script; the schema limit counts characters, so it holds too.
func statusName(name string) string {
	if len(name) <= kubemootv1alpha1.MaxStatusNameLength {
		return name
	}
	cut := kubemootv1alpha1.MaxStatusNameLength
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return name[:cut]
}

// oneLineReason folds a judge reason onto one line (runs of whitespace,
// newlines included, become one space) and cuts it to MaxJudgeReasonLength
// characters, ending a cut reason with "...".
func oneLineReason(reason string) string {
	return truncateRunes(strings.Join(strings.Fields(reason), " "), kubemootv1alpha1.MaxJudgeReasonLength, "...")
}

// truncateRunes cuts s to at most limit characters. When it cuts, the result
// ends with suffix and is still at most limit characters long.
func truncateRunes(s string, limit int, suffix string) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	keep := limit - utf8.RuneCountInString(suffix)
	return string([]rune(s)[:keep]) + suffix
}

// roundScore rounds a 0-100 quality to an integer status score.
func roundScore(q float64) int32 {
	return int32(math.Round(math.Max(0, math.Min(100, q))))
}

// judgePhaseFor is the judge phase the suite and its checkpoint imply.
func judgePhaseFor(suite *kubemootv1alpha1.CrewFitnessSuite, checkpointComplete bool) kubemootv1alpha1.FitnessJudgePhase {
	switch {
	case judgeSkipped(suite):
		return kubemootv1alpha1.FitnessJudgePhaseSkipped
	case !isSuiteTerminal(suite.Status.Phase):
		return kubemootv1alpha1.FitnessJudgePhasePending
	case checkpointComplete:
		return kubemootv1alpha1.FitnessJudgePhaseComplete
	default:
		return kubemootv1alpha1.FitnessJudgePhaseJudging
	}
}

// judgeStatusFrozen reports whether status.judge is final: once Complete or
// Skipped it is the durable record and is not rewritten, even after the object
// store checkpoint expires.
func judgeStatusFrozen(js *kubemootv1alpha1.FitnessJudgeStatus) bool {
	return js != nil && (js.Phase == kubemootv1alpha1.FitnessJudgePhaseComplete ||
		js.Phase == kubemootv1alpha1.FitnessJudgePhaseSkipped)
}

// judgeStatusFor builds status.judge from the suite and the judge checkpoint.
// Scores follow spec order and are capped at MaxStatusScenarios entries; the
// counts and the mean cover every scored scenario of the spec. completedAt is
// kept from the current status when present, otherwise set to now.
func judgeStatusFor(suite *kubemootv1alpha1.CrewFitnessSuite, cache deferredScoreCache, now metav1.Time) *kubemootv1alpha1.FitnessJudgeStatus {
	js := &kubemootv1alpha1.FitnessJudgeStatus{Phase: judgePhaseFor(suite, cache.Complete)}
	if js.Phase == kubemootv1alpha1.FitnessJudgePhaseSkipped {
		return js
	}
	agg := judgeAggregate{}
	for _, ref := range distinctTestRefs(suite.Spec.Scripts) {
		q, scored := cache.Scores[ref]
		if !scored {
			continue
		}
		score := agg.add(q, cache.Answers[ref])
		if len(js.Scores) < kubemootv1alpha1.MaxStatusScenarios {
			js.Scores = append(js.Scores, judgeScoreFor(ref, score, cache))
		}
	}
	agg.applyTo(js)
	js.Unjudged = unjudgedStatus(suite.Spec.Scripts, cache)
	js.Total = max(int32(cache.Total), js.Judged+int32(len(cache.Unjudged)))
	if js.Phase == kubemootv1alpha1.FitnessJudgePhaseComplete {
		js.CompletedAt = completedAt(suite.Status.Judge, now)
	}
	return js
}

// judgeAggregate sums what the judged scenarios hold, for the aggregate fields of
// status.judge.
type judgeAggregate struct {
	scenarios, withoutAnswers, zeros, answers, fabrications int32
	scenarioSum, answerSum                                  float64
}

// add counts one judged scenario with its quality and answers and returns the
// scenario's rounded score.
func (a *judgeAggregate) add(quality float64, recs []answerRecord) int32 {
	score := roundScore(quality)
	a.scenarios++
	a.scenarioSum += quality
	if score == 0 {
		a.zeros++
	}
	if len(recs) == 0 {
		a.withoutAnswers++
	}
	for _, r := range recs {
		a.answers++
		a.answerSum += r.Score
		if r.Fabrication {
			a.fabrications++
		}
	}
	return score
}

// applyTo sets the counts and means on js. The means are absent until something
// is judged.
func (a judgeAggregate) applyTo(js *kubemootv1alpha1.FitnessJudgeStatus) {
	js.Judged, js.Zeros = a.scenarios, a.zeros
	js.JudgedAnswers, js.Fabrications = a.answers, a.fabrications
	if a.scenarios > 0 {
		js.Mean = ptr.To(roundScore(a.scenarioSum / float64(a.scenarios)))
	}
	if a.answers > 0 && a.withoutAnswers == 0 {
		js.AnswerMean = ptr.To(roundScore(a.answerSum / float64(a.answers)))
	}
}

// judgeScoreFor is the status entry of one judged scenario: its score and reason,
// and how many iterations the judge scored, gated or flagged as fabricated. Each
// iteration's own score stays in the judge checkpoint.
func judgeScoreFor(ref string, score int32, cache deferredScoreCache) kubemootv1alpha1.FitnessJudgeScore {
	out := kubemootv1alpha1.FitnessJudgeScore{
		Scenario: statusName(ref),
		Score:    score,
		Reason:   oneLineReason(cache.Reasons[ref]),
	}
	t := tallyAnswers(cache.Answers[ref])
	out.Answers, out.Gated, out.Fabrications = t.answers, t.gated, t.fabrications
	return out
}

// unjudgedStatus lists the scenarios the judge gave up on in spec order, capped at
// MaxStatusScenarios entries. nil when there are none.
func unjudgedStatus(scripts []kubemootv1alpha1.SuiteScript, cache deferredScoreCache) []kubemootv1alpha1.FitnessJudgeUnjudged {
	var out []kubemootv1alpha1.FitnessJudgeUnjudged
	for _, ref := range distinctTestRefs(scripts) {
		reason, gaveUp := cache.Unjudged[ref]
		if !gaveUp {
			continue
		}
		if len(out) == kubemootv1alpha1.MaxStatusScenarios {
			break
		}
		out = append(out, kubemootv1alpha1.FitnessJudgeUnjudged{Scenario: statusName(ref), Reason: oneLineReason(reason)})
	}
	return out
}

// completedAt keeps the completion time status already holds, or uses now.
func completedAt(cur *kubemootv1alpha1.FitnessJudgeStatus, now metav1.Time) *metav1.Time {
	if cur != nil && cur.CompletedAt != nil {
		return cur.CompletedAt
	}
	return &now
}

// newJudgeStatus is status.judge for a suite that is starting (Pending) or was
// cancelled (Skipped); nil when there is no artifact store, so no judge.
func (r *CrewFitnessSuiteReconciler) newJudgeStatus(phase kubemootv1alpha1.FitnessJudgePhase) *kubemootv1alpha1.FitnessJudgeStatus {
	if r.store() == nil {
		return nil
	}
	return &kubemootv1alpha1.FitnessJudgeStatus{Phase: phase}
}

// judgeStatusCurrent reports whether status.judge needs no write: it is final,
// already matches, or holds more judged scenarios than the checkpoint. Scores
// only accumulate within a run, so fewer means the checkpoint passed its object
// store retention, and status keeps what it recorded.
func judgeStatusCurrent(cur, desired *kubemootv1alpha1.FitnessJudgeStatus) bool {
	if judgeStatusFrozen(cur) || equality.Semantic.DeepEqual(desired, cur) {
		return true
	}
	return cur != nil && desired.Phase != kubemootv1alpha1.FitnessJudgePhaseSkipped && desired.Judged < cur.Judged
}

// applyJudgeStatus writes status.judge from the checkpoint when it differs from
// what the suite holds (see judgeStatusCurrent). It patches the status
// subresource only, with an optimistic lock so it never overwrites a newer
// status.
func (r *CrewFitnessSuiteReconciler) applyJudgeStatus(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite, cache deferredScoreCache) error {
	desired := judgeStatusFor(suite, cache, metav1.Now())
	if judgeStatusCurrent(suite.Status.Judge, desired) {
		return nil
	}
	base := suite.DeepCopy()
	suite.Status.Judge = desired
	return r.Status().Patch(ctx, suite, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

// syncJudgeStatus brings status.judge in line with the judge checkpoint on a
// terminal reconcile when no judge worker is running for the run: it catches a
// write the worker missed and fills status.judge for a suite that finished
// before the field existed. While a worker runs, the worker owns the field.
// The status patch replaces suite with the server's copy, so callers must not
// hold an unsaved status change when they call it.
func (r *CrewFitnessSuiteReconciler) syncJudgeStatus(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) error {
	store := r.store()
	if store == nil || suite.Status.RunID == "" {
		return nil
	}
	if judgeSkipped(suite) {
		return r.applyJudgeStatus(ctx, suite, deferredScoreCache{})
	}
	prefix := suiteRunPrefix(suite.Namespace, suite.Name, suite.Status.RunID)
	if _, running := deferredInFlight.Load(prefix); running {
		return nil
	}
	return r.applyJudgeStatus(ctx, suite, loadDeferredCache(store, prefix))
}

// liveReader reads straight from the API server (APIReader), so a read right
// after a write sees it; nil APIReader falls back to Client.
func (r *CrewFitnessSuiteReconciler) liveReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// publishStatus writes the pass's checkpoint to status.judge from the judge
// worker. It reads the live suite (retrying on a write conflict) and leaves a
// suite that is gone, or that now holds a different run, untouched. A failed
// write is logged: the next terminal reconcile syncs status from the checkpoint.
func (p *judgePass) publishStatus() {
	if p.r == nil || p.r.Client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), judgeStatusWriteTimeout)
	defer cancel()
	key := client.ObjectKeyFromObject(p.suite)
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		live := &kubemootv1alpha1.CrewFitnessSuite{}
		if err := p.r.liveReader().Get(ctx, key, live); err != nil {
			return err
		}
		if live.Status.RunID != p.suite.Status.RunID {
			return nil
		}
		return p.r.applyJudgeStatus(ctx, live, p.cache)
	})
	if err != nil && !apierrors.IsNotFound(err) {
		p.log.Info("deferred judge: status write failed; the next reconcile syncs it", "suite", key.String(), "err", err.Error())
	}
}
