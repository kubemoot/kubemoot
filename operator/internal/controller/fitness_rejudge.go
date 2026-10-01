/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// fitness_rejudge.go runs a re-judge suite (spec.rejudge): a judge-only pass
// that scores an earlier run's saved answers against THIS suite's scenarios,
// without asking the crew anything.
//
// The suite copies the source run's transcripts into its own run prefix,
// re-indexed to its own scripts by testRef, with the assertions re-evaluated
// against its own scenario text. From then on it is an ordinary completed run:
// the deferred judge scores the copies (references come from the copied DEFER
// assertions, so they are this suite's), the checkpoint resumes after a
// restart, and the dashboard's /scores and /iterations read it unchanged.
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/fitnessscript"
)

const (
	// rejudgeConditionType reports whether the re-judge source was usable.
	rejudgeConditionType = "RejudgeSourceReady"

	// rejudgedNote marks an assertion whose text differs from the source run's,
	// so its result was computed from the stored transcript at re-judge time.
	rejudgedNote = " (re-evaluated from the source transcript)"

	reasonSourceReady        = "SourceReady"
	reasonSourceNotFound     = "SourceNotFound"
	reasonSourceNotCompleted = "SourceNotCompleted"
	reasonRunIDMismatch      = "RunIDMismatch"
	reasonInvalidRejudge     = "InvalidRejudge"
	reasonScriptUnavailable  = "ScriptUnavailable"
	reasonNoTranscripts      = "NoTranscripts"
	reasonNoArtifactStore    = "NoArtifactStore"
)

// rejudgeError is a permanent problem with the re-judge request. Its reason
// becomes the RejudgeSourceReady condition reason; the suite goes to Error.
type rejudgeError struct {
	reason string
	msg    string
}

func (e *rejudgeError) Error() string { return e.msg }

func rejudgeFailure(reason, format string, args ...any) error {
	return &rejudgeError{reason: reason, msg: fmt.Sprintf(format, args...)}
}

// rejudgedFrom is stamped on every copied transcript so a reader can trace the
// answer back to the run that produced it.
type rejudgedFrom struct {
	Suite string `json:"suite"`
	RunID string `json:"runId"`
	Key   string `json:"key"`
}

// rejudgeCopy is one source transcript to copy into this run.
type rejudgeCopy struct {
	scenario  string
	sourceKey string
	targetKey string
	test      *fitnessscript.FitnessTest
}

// rejudgePlan is the full copy list plus this suite's scenarios the source lacks.
type rejudgePlan struct {
	copies      []rejudgeCopy
	notInSource []string
}

// suiteRunPrefix is the object-store prefix of one suite run.
func suiteRunPrefix(namespace, name, runID string) string {
	return fmt.Sprintf("%s/%s/%s/", namespace, name, runID)
}

// startRejudge handles a non-terminal re-judge suite in one step: validate the
// source, copy its transcripts, and complete. A permanent problem moves the suite
// to Error with a False RejudgeSourceReady condition; an object-store error is
// returned so the reconcile retries. The copy is idempotent (same keys, same
// bytes), so a restart mid-copy simply copies again.
func (r *CrewFitnessSuiteReconciler) startRejudge(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) (ctrl.Result, error) {
	plan, err := r.prepareRejudge(ctx, suite)
	if err != nil {
		return r.failRejudge(ctx, suite, err)
	}
	runID := generateRunID(suite)
	progress, err := copyRejudgeTranscripts(r.store(), &plan, *suite.Spec.Rejudge, artifactTTL(suite))
	if err != nil {
		return r.failRejudge(ctx, suite, err)
	}
	r.completeRejudge(suite, runID, plan, progress)
	if err := r.Status().Update(ctx, suite); err != nil {
		return ctrl.Result{}, err
	}
	logf.FromContext(ctx).Info("Re-judge transcripts copied",
		"suite", suite.Name, "source", suite.Spec.Rejudge.Suite, "sourceRun", suite.Spec.Rejudge.RunID,
		"transcripts", progress.completed, "notInSource", len(plan.notInSource))
	// The deferred judge starts in terminalUpkeep on the next reconcile.
	return ctrl.Result{RequeueAfter: reconcileTickInterval}, nil
}

// prepareRejudge validates the request and the source run, resolves this
// suite's scenarios, and plans the copy.
func (r *CrewFitnessSuiteReconciler) prepareRejudge(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) (rejudgePlan, error) {
	if err := validateRejudgeSpec(suite); err != nil {
		return rejudgePlan{}, err
	}
	store := r.store()
	if store == nil {
		return rejudgePlan{}, rejudgeFailure(reasonNoArtifactStore,
			"re-judge needs the NATS object store, which is not configured")
	}
	source, err := r.rejudgeSource(ctx, suite)
	if err != nil {
		return rejudgePlan{}, err
	}
	tests, err := r.suiteTests(ctx, suite)
	if err != nil {
		return rejudgePlan{}, err
	}
	src := suite.Spec.Rejudge
	keys, err := store.ListObjects(FitnessArtifactsBucket, suiteRunPrefix(suite.Namespace, src.Suite, src.RunID))
	if err != nil {
		return rejudgePlan{}, fmt.Errorf("listing source transcripts: %w", err)
	}
	runID := generateRunID(suite)
	plan := planRejudge(suiteRunPrefix(suite.Namespace, suite.Name, runID), suite.Spec.Scripts, tests,
		sourceTranscriptsByScenario(source, keys))
	if len(plan.copies) == 0 {
		return rejudgePlan{}, rejudgeFailure(reasonNoTranscripts,
			"source run %s/%s has no transcript for any scenario of this suite", src.Suite, src.RunID)
	}
	return plan, nil
}

// validateRejudgeSpec checks the parts of a re-judge request that need no lookup.
func validateRejudgeSpec(suite *kubemootv1alpha1.CrewFitnessSuite) error {
	src := suite.Spec.Rejudge
	if src.Suite == "" || src.RunID == "" {
		return rejudgeFailure(reasonInvalidRejudge, "spec.rejudge needs both suite and runId")
	}
	if src.Suite == suite.Name {
		return rejudgeFailure(reasonInvalidRejudge, "spec.rejudge.suite cannot name this suite")
	}
	if len(suite.Spec.Scripts) == 0 {
		return rejudgeFailure(reasonInvalidRejudge, "spec.scripts must list the scenarios to judge against")
	}
	return uniqueTestRefs(suite.Spec.Scripts)
}

// uniqueTestRefs rejects a script list that names a scenario twice: scenarios
// are matched to the source run by testRef, so a repeat would be ambiguous.
func uniqueTestRefs(scripts []kubemootv1alpha1.SuiteScript) error {
	seen := make(map[string]bool, len(scripts))
	for _, s := range scripts {
		if seen[s.TestRef] {
			return rejudgeFailure(reasonInvalidRejudge, "spec.scripts names %q more than once", s.TestRef)
		}
		seen[s.TestRef] = true
	}
	return nil
}

// rejudgeSource fetches the source suite and checks its run is the one named
// and is Completed.
func (r *CrewFitnessSuiteReconciler) rejudgeSource(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) (*kubemootv1alpha1.CrewFitnessSuite, error) {
	src := suite.Spec.Rejudge
	source := &kubemootv1alpha1.CrewFitnessSuite{}
	err := r.Get(ctx, client.ObjectKey{Namespace: suite.Namespace, Name: src.Suite}, source)
	if apierrors.IsNotFound(err) {
		return nil, rejudgeFailure(reasonSourceNotFound,
			"source suite %s/%s not found", suite.Namespace, src.Suite)
	}
	if err != nil {
		return nil, fmt.Errorf("getting source suite: %w", err)
	}
	if source.Status.RunID != src.RunID {
		return nil, rejudgeFailure(reasonRunIDMismatch,
			"source suite %s has run %q, not %q", src.Suite, source.Status.RunID, src.RunID)
	}
	if source.Status.Phase != kubemootv1alpha1.CrewFitnessSuitePhaseCompleted {
		return nil, rejudgeFailure(reasonSourceNotCompleted,
			"source suite %s is %s; only a Completed run can be re-judged", src.Suite, phaseOrPending(string(source.Status.Phase)))
	}
	return source, nil
}

// suiteTests parses every script of this suite, in spec order. A script that
// cannot resolve is a permanent failure; an API error reading its ConfigMap is
// returned as-is so the reconcile retries.
func (r *CrewFitnessSuiteReconciler) suiteTests(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) ([]fitnessscript.FitnessTest, error) {
	tests := make([]fitnessscript.FitnessTest, 0, len(suite.Spec.Scripts))
	for _, s := range suite.Spec.Scripts {
		content, err := r.scriptContent(ctx, suite.Namespace, s)
		if err != nil {
			return nil, err
		}
		tests = append(tests, fitnessscript.ParseFitnessTest(content))
	}
	return tests, nil
}

// scriptContent returns a script's scenario text: inline testContent, or the
// "<testRef>.adl" key of its ConfigMap (the key a CrewFitness run reads).
func (r *CrewFitnessSuiteReconciler) scriptContent(ctx context.Context, namespace string, s kubemootv1alpha1.SuiteScript) (string, error) {
	if s.TestRef == "" || (s.TestContent == "") == (s.ConfigMapRef == "") {
		return "", rejudgeFailure(reasonScriptUnavailable,
			"script %q needs a testRef and exactly one of testContent or configMapRef", s.TestRef)
	}
	if s.TestContent != "" {
		return s.TestContent, nil
	}
	cm := &corev1.ConfigMap{}
	err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: s.ConfigMapRef}, cm)
	if apierrors.IsNotFound(err) {
		return "", rejudgeFailure(reasonScriptUnavailable, "script %q: ConfigMap %q not found", s.TestRef, s.ConfigMapRef)
	}
	if err != nil {
		return "", fmt.Errorf("reading ConfigMap %q: %w", s.ConfigMapRef, err)
	}
	content, ok := cm.Data[s.TestRef+".adl"]
	if !ok {
		return "", rejudgeFailure(reasonScriptUnavailable,
			"script %q: ConfigMap %q has no key %q", s.TestRef, s.ConfigMapRef, s.TestRef+".adl")
	}
	return content, nil
}

// sourceTranscriptsByScenario groups the source run's transcript keys by the
// testRef of the source script each key belongs to. Keys that are not
// transcripts (the checkpoint, sidecars) or whose index is outside the source
// scripts are dropped.
func sourceTranscriptsByScenario(source *kubemootv1alpha1.CrewFitnessSuite, keys []string) map[string][]string {
	out := map[string][]string{}
	for _, key := range keys {
		idx, _, ok := parseTranscriptKey(key)
		if !ok || idx < 0 || idx >= len(source.Spec.Scripts) {
			continue
		}
		ref := source.Spec.Scripts[idx].TestRef
		out[ref] = append(out[ref], key)
	}
	return out
}

// planRejudge maps every source transcript of a scenario this suite also has
// onto this suite's script index, keeping the iteration number. Scenarios with
// no source transcript are reported, not copied.
func planRejudge(targetPrefix string, scripts []kubemootv1alpha1.SuiteScript, tests []fitnessscript.FitnessTest, bySource map[string][]string) rejudgePlan {
	var plan rejudgePlan
	for i, s := range scripts {
		keys := bySource[s.TestRef]
		if len(keys) == 0 {
			plan.notInSource = append(plan.notInSource, s.TestRef)
			continue
		}
		for _, key := range keys {
			_, iter, _ := parseTranscriptKey(key)
			plan.copies = append(plan.copies, rejudgeCopy{
				scenario:  s.TestRef,
				sourceKey: key,
				targetKey: fmt.Sprintf("%ss%d-i%d.json", targetPrefix, i, iter),
				test:      &tests[i],
			})
		}
	}
	return plan
}

// copyRejudgeTranscripts copies every planned transcript with its assertions
// re-evaluated, and returns the pass/fail rollup of the copies. A source object
// that cannot be read is an error (retried); one that is not a transcript is
// skipped, and a scenario left with no copy joins plan.notInSource.
func copyRejudgeTranscripts(store objectStore, plan *rejudgePlan, src kubemootv1alpha1.RejudgeSource, ttl time.Duration) (childProgress, error) {
	var p childProgress
	copied := map[string]bool{}
	for _, c := range plan.copies {
		data, err := store.GetObject(FitnessArtifactsBucket, c.sourceKey)
		if err != nil {
			return p, fmt.Errorf("reading source transcript %s: %w", c.sourceKey, err)
		}
		out, phase, ok := rejudgeTranscript(data, c.test, rejudgedFrom{Suite: src.Suite, RunID: src.RunID, Key: c.sourceKey})
		if !ok {
			continue
		}
		if _, err := store.PutObject(FitnessArtifactsBucket, c.targetKey, out, ttl); err != nil {
			return p, fmt.Errorf("writing transcript %s: %w", c.targetKey, err)
		}
		copied[c.scenario] = true
		p.add(phase)
	}
	if p.completed == 0 {
		return p, rejudgeFailure(reasonNoTranscripts,
			"no source transcript of run %s/%s could be parsed", src.Suite, src.RunID)
	}
	plan.notInSource = appendUncopied(plan.notInSource, plan.copies, copied)
	return p, nil
}

// appendUncopied adds each planned scenario that ended with no copy, once.
func appendUncopied(notInSource []string, copies []rejudgeCopy, copied map[string]bool) []string {
	for _, c := range copies {
		if !copied[c.scenario] {
			notInSource = append(notInSource, c.scenario)
			copied[c.scenario] = true // report once
		}
	}
	return notInSource
}

// rejudgeTranscript rewrites one source transcript for this suite: every field
// is kept, the assertions become this scenario's assertions evaluated against
// the stored answer, and rejudgedFrom records the source. ok is false when the
// data is not a transcript.
func rejudgeTranscript(data []byte, test *fitnessscript.FitnessTest, from rejudgedFrom) ([]byte, kubemootv1alpha1.CrewFitnessPhase, bool) {
	var doc map[string]json.RawMessage
	if len(data) == 0 || json.Unmarshal(data, &doc) != nil {
		return nil, "", false
	}
	var stored struct {
		Assertions     []fitnessscript.AssertionResult `json:"assertions"`
		Events         []fitnessscript.SignalEvent     `json:"events"`
		ConversationID string                          `json:"conversationId"`
		Answered       bool                            `json:"answered"`
	}
	if json.Unmarshal(data, &stored) != nil {
		return nil, "", false
	}
	state := fitnessscript.RunState{
		// A transcript is written only for an answered run (POST 200 and a 'done'
		// event), so the run state the runner had is recoverable from it.
		PostOK:    stored.Answered || stored.ConversationID != "",
		Events:    stored.Events,
		Synthesis: fitnessscript.FindSynthesis(stored.Events),
		TimedOut:  !fitnessscript.HasDone(stored.Events),
	}
	results := reassert(test.Assertions, stored.Assertions, state)
	doc["assertions"], _ = json.Marshal(results)
	doc["rejudgedFrom"], _ = json.Marshal(from)
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, "", false
	}
	return out, resultsPhase(results), true
}

// reassert returns this scenario's assertion results for a stored run. An
// assertion whose text is identical to one the source run evaluated keeps the
// source result as recorded; any other is evaluated now by the shared assertion
// engine against the stored events and synthesis.
func reassert(assertions []fitnessscript.Assertion, source []fitnessscript.AssertionResult, state fitnessscript.RunState) []fitnessscript.AssertionResult {
	recorded := make(map[string]fitnessscript.AssertionResult, len(source))
	for _, a := range source {
		recorded[a.Raw] = a
	}
	out := make([]fitnessscript.AssertionResult, 0, len(assertions))
	for _, a := range assertions {
		if prev, ok := recorded[a.Raw]; ok {
			out = append(out, prev)
			continue
		}
		res := fitnessscript.EvaluateAssertion(a, state)
		res.Message += rejudgedNote
		out = append(out, res)
	}
	return out
}

// resultsPhase applies the iteration phase rule (all pass, any fail, none).
func resultsPhase(results []fitnessscript.AssertionResult) kubemootv1alpha1.CrewFitnessPhase {
	passed := 0
	for _, r := range results {
		if r.Passed {
			passed++
		}
	}
	return phaseFromTally(passed, len(results))
}

// completeRejudge records the copied run in status and marks the suite
// Completed; the deferred judge then scores it.
func (r *CrewFitnessSuiteReconciler) completeRejudge(suite *kubemootv1alpha1.CrewFitnessSuite, runID string, plan rejudgePlan, p childProgress) {
	now := metav1.Now()
	suite.Status.RunID = runID
	suite.Status.StartedAt = &now
	suite.Status.CompletedAt = &now
	suite.Status.IterationsTotal = p.completed
	applyProgress(suite, p)
	suite.Status.Error = ""
	suite.Status.Rejudge = &kubemootv1alpha1.RejudgeStatus{
		Source:      *suite.Spec.Rejudge,
		Transcripts: p.completed,
		NotInSource: plan.notInSource,
	}
	meta.SetStatusCondition(&suite.Status.Conditions, metav1.Condition{
		Type:    rejudgeConditionType,
		Status:  metav1.ConditionTrue,
		Reason:  reasonSourceReady,
		Message: rejudgeSummary(suite.Spec.Rejudge, p.completed, plan.notInSource),
	})
	suite.Status.Phase = kubemootv1alpha1.CrewFitnessSuitePhaseCompleted
}

func rejudgeSummary(src *kubemootv1alpha1.RejudgeSource, copied int32, notInSource []string) string {
	msg := fmt.Sprintf("copied %d transcript(s) from %s run %s", copied, src.Suite, src.RunID)
	if len(notInSource) > 0 {
		msg += fmt.Sprintf("; not in source: %s", strings.Join(notInSource, ", "))
	}
	return msg
}

// failRejudge moves the suite to Error with a False condition for a permanent
// problem, and returns any other error so the reconcile retries.
func (r *CrewFitnessSuiteReconciler) failRejudge(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite, err error) (ctrl.Result, error) {
	var rerr *rejudgeError
	if !errors.As(err, &rerr) {
		return ctrl.Result{}, err
	}
	meta.SetStatusCondition(&suite.Status.Conditions, metav1.Condition{
		Type:    rejudgeConditionType,
		Status:  metav1.ConditionFalse,
		Reason:  rerr.reason,
		Message: rerr.msg,
	})
	return r.setSuiteError(ctx, suite, rerr.msg)
}

// rejudgeUpkeep is terminal upkeep for a re-judge suite. There are no children
// to harvest: once the deferred judge has finished, the XLSX is built from the
// copied transcripts and the checkpoint, the same way the on-demand report is.
func (r *CrewFitnessSuiteReconciler) rejudgeUpkeep(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) (ctrl.Result, error) {
	store := r.store()
	if store == nil || suite.Status.Phase != kubemootv1alpha1.CrewFitnessSuitePhaseCompleted || suite.Status.ArtifactRef != nil {
		return ctrl.Result{}, nil
	}
	if complete, _ := r.deferredJudgeState(suite); !complete {
		// State-driven: wait on judge completion, never a clock.
		return ctrl.Result{RequeueAfter: artifactRetryInterval}, nil
	}
	xlsx, err := buildSuiteReport(ctx, r.Client, store, suite.DeepCopy())
	if err != nil {
		logf.FromContext(ctx).Error(err, "re-judge artifact build failed; will retry", "suite", suite.Name)
		return ctrl.Result{RequeueAfter: artifactRetryInterval}, nil
	}
	ref, err := putSuiteArtifact(store, suite, xlsx)
	if err != nil {
		logf.FromContext(ctx).Error(err, "re-judge artifact write failed; will retry", "suite", suite.Name)
		return ctrl.Result{RequeueAfter: artifactRetryInterval}, nil
	}
	suite.Status.ArtifactRef = ref
	return ctrl.Result{}, r.Status().Update(ctx, suite)
}
