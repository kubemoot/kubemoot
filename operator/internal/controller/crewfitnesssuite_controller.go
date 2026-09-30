/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package controller — crewfitnesssuite_controller.go drives the
// [[Kubemoot Fitness Suite Runner with NATS Object Store Artifacts]] card.
//
// A CrewFitnessSuite is a request to run len(scripts) × iterations
// CrewFitness CRs against a crew, aggregate the results, and persist an
// XLSX artifact for dashboard download. The reconciler:
//
//  1. On a Pending suite: stamp a runId, transition to Running.
//  2. Per reconcile tick: list the per-iteration CrewFitness CRs we own,
//     count how many have reached terminal phase, classify pass/fail/error,
//     update status counts.
//  3. Schedule the next batch of per-iteration CRs up to spec.concurrency
//     (default 1 — serial). Each CR is named
//     `run-{runId-short}-{scriptIdx}-{iter}` and labelled with the
//     fitness-kind=run-instance + suite owner labels.
//  4. When IterationsCompleted == IterationsTotal: hand off to XLSX
//     generation + NATS write (separate commit), then transition to
//     Completed or Failed.
//  5. spec.suspend holds scheduling: the running iteration finishes and the
//     suite reads Paused once nothing is in flight; clearing it resumes.
//     spec.cancel deletes the in-flight iteration and moves the suite to the
//     terminal Cancelled phase, which writes a partial XLSX without judging.
//
// XLSX writing and NATS Object Store integration ship in the next commit
// to keep this one reviewable. Until that lands, the suite still completes
// end-to-end and the status carries the counts; only the artifactRef stays
// nil.
package controller

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
)

// CrewFitnessSuiteReconciler reconciles a CrewFitnessSuite object.
type CrewFitnessSuiteReconciler struct {
	client.Client
	// APIReader reads children straight from the API server, bypassing the
	// informer cache. The pause and cancel decisions use it so a child created
	// on the previous tick is never missed. Nil falls back to Client.
	APIReader     client.Reader
	Scheme        *runtime.Scheme
	NATSPublisher *kubemootnats.Publisher
}

// FitnessArtifactsBucket is the NATS Object Store bucket where the
// reconciler writes XLSX outputs. Dashboard backend reads from the
// same bucket on download.
const FitnessArtifactsBucket = "kubemoot_fitness_artifacts"

// crewFitnessSuiteFinalizer guards deletion so the run's NATS Object Store
// artifacts (XLSX, per-iteration transcripts, deferred-score sidecars) are
// purged before the CR is removed. Without it the owner-ref cascade reclaims
// only the child CrewFitness CRs and their Jobs, orphaning the artifacts.
const crewFitnessSuiteFinalizer = "kubemoot.ai/fitness-artifacts"

const (
	// suiteOwnerLabel ties per-iteration CrewFitness CRs back to the
	// CrewFitnessSuite that created them. Reconciler uses this label
	// selector to find its child CRs without an owner-ref list query.
	suiteOwnerLabel = "kubemoot.ai/fitness-suite"

	// fitnessKindLabel distinguishes run-instance CRs (created by this
	// reconciler) from baseline-scenario CRs (created by the crew chart)
	// from anything else. Matches the labels the crew chart already uses.
	fitnessKindLabel           = "kubemoot.ai/fitness-kind"
	fitnessKindRunInstance     = "run-instance"
	suiteIterationIndexLabel   = "kubemoot.ai/fitness-iteration"
	suiteScriptIndexLabel      = "kubemoot.ai/fitness-script-index"
	suiteRunIDLabel            = "kubemoot.ai/fitness-run-id"
	defaultPerIterationTimeout = 10 * time.Minute
	defaultArtifactRetention   = 168 * time.Hour
	reconcileTickInterval      = 5 * time.Second
	// artifactRetryInterval backs off the post-completion XLSX write when
	// NATS is transiently unavailable. Longer than the tick interval since
	// a write failure usually means NATS needs a moment.
	artifactRetryInterval = 30 * time.Second
)

// +kubebuilder:rbac:groups=kubemoot.ai,resources=crewfitnesssuites,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=crewfitnesssuites/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=crewfitnesssuites/finalizers,verbs=update
// +kubebuilder:rbac:groups=kubemoot.ai,resources=crewfitnesses,verbs=get;list;watch;create;update;patch;delete;deletecollection

func (r *CrewFitnessSuiteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{}
	if err := r.Get(ctx, req.NamespacedName, suite); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion: purge the run's NATS artifacts before letting the CR go, then
	// remove the finalizer. Child CRs/Jobs are reclaimed separately by owner-ref
	// cascade.
	if !suite.DeletionTimestamp.IsZero() {
		return r.finalizeSuite(ctx, suite)
	}
	if !controllerutil.ContainsFinalizer(suite, crewFitnessSuiteFinalizer) {
		controllerutil.AddFinalizer(suite, crewFitnessSuiteFinalizer)
		// The update event re-triggers reconcile through the watch, so the
		// watch must not filter metadata-only updates (no GenerationChangedPredicate).
		return ctrl.Result{}, r.Update(ctx, suite)
	}

	return r.dispatchPhase(ctx, suite)
}

// isSuiteTerminal reports whether the suite has stopped scheduling for good.
// Cancelled joins Completed/Failed/Error: its post-run upkeep (partial XLSX,
// child reap) runs through the same terminalUpkeep path.
func isSuiteTerminal(phase kubemootv1alpha1.CrewFitnessSuitePhase) bool {
	switch phase {
	case kubemootv1alpha1.CrewFitnessSuitePhaseCompleted,
		kubemootv1alpha1.CrewFitnessSuitePhaseFailed,
		kubemootv1alpha1.CrewFitnessSuitePhaseError,
		kubemootv1alpha1.CrewFitnessSuitePhaseCancelled:
		return true
	}
	return false
}

// dispatchPhase routes one reconcile by phase. A terminal suite only runs
// upkeep. spec.cancel on any non-terminal suite (Pending, Running, Paused) wins
// over everything else, including spec.suspend.
func (r *CrewFitnessSuiteReconciler) dispatchPhase(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) (ctrl.Result, error) {
	if isSuiteTerminal(suite.Status.Phase) {
		// Terminal: guarantee the XLSX artifact is written, then reap the
		// per-iteration children. Idempotent and retry-safe; converges to
		// (artifact present, zero children) with no manual cleanup.
		return r.terminalUpkeep(ctx, suite)
	}
	if suite.Spec.Cancel {
		return r.cancelSuite(ctx, suite)
	}
	switch suite.Status.Phase {
	case kubemootv1alpha1.CrewFitnessSuitePhaseRunning:
		return r.advanceRunning(ctx, suite)
	case kubemootv1alpha1.CrewFitnessSuitePhasePaused:
		return r.advancePaused(ctx, suite)
	default:
		logf.FromContext(ctx).Info("Starting fitness suite",
			"name", suite.Name, "crew", suite.Spec.CrewRef,
			"scripts", len(suite.Spec.Scripts), "iterations", suite.Spec.Iterations)
		return r.startSuite(ctx, suite)
	}
}

// purgeArtifacts deletes every object in the bucket under prefix and returns the
// count deleted. It continues past a per-object delete failure (so one bad key
// does not strand the rest) and returns the first error encountered, if any.
func purgeArtifacts(store objectStore, bucket, prefix string) (int, error) {
	keys, err := store.ListObjects(bucket, prefix)
	if err != nil {
		return 0, err
	}
	deleted := 0
	var firstErr error
	for _, key := range keys {
		if err := store.DeleteObject(bucket, key); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("deleting %s: %w", key, err)
			}
			continue
		}
		deleted++
	}
	return deleted, firstErr
}

// finalizeSuite purges this suite's NATS Object Store artifacts (the XLSX,
// per-iteration transcripts, and deferred-score sidecars, all keyed under
// "<namespace>/<name>/") and removes the finalizer so deletion can proceed. The
// prefix omits the runId, so if the suite was re-run (multiple runIds) every
// run's artifacts under the prefix are purged together. The purge is
// best-effort: a NATS error is logged but does not wedge deletion (the bucket
// TTL is the backstop), matching the crew working-memory finalizer.
func (r *CrewFitnessSuiteReconciler) finalizeSuite(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	if !controllerutil.ContainsFinalizer(suite, crewFitnessSuiteFinalizer) {
		return ctrl.Result{}, nil
	}
	if r.NATSPublisher != nil {
		prefix := fmt.Sprintf("%s/%s/", suite.Namespace, suite.Name)
		if n, err := purgeArtifacts(r.NATSPublisher, FitnessArtifactsBucket, prefix); err != nil {
			log.Error(err, "Failed to purge fitness artifacts on deletion (bucket TTL is the backstop)",
				"suite", suite.Name, "prefix", prefix)
		} else if n > 0 {
			log.Info("Purged fitness artifacts on suite deletion",
				"suite", suite.Name, "prefix", prefix, "objects", n)
		}
	}
	controllerutil.RemoveFinalizer(suite, crewFitnessSuiteFinalizer)
	return ctrl.Result{}, r.Update(ctx, suite)
}

// startSuite transitions a Pending suite into Running by stamping a runId
// and recording StartedAt + the total-iteration count.
func (r *CrewFitnessSuiteReconciler) startSuite(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) (ctrl.Result, error) {
	if err := validateSuiteSpec(suite); err != nil {
		return r.setSuiteError(ctx, suite, err.Error())
	}

	// Bring the crew to a clean, cold state once before any iteration runs
	// (purge working memory + unload models). Both default on; best-effort.
	r.coldStart(ctx, suite)

	now := metav1.Now()
	suite.Status.Phase = kubemootv1alpha1.CrewFitnessSuitePhaseRunning
	suite.Status.RunID = generateRunID(suite)
	suite.Status.StartedAt = &now
	suite.Status.IterationsTotal = int32(len(suite.Spec.Scripts)) * suite.Spec.Iterations
	suite.Status.IterationsCompleted = 0
	suite.Status.Passed = 0
	suite.Status.Failed = 0
	suite.Status.Errored = 0
	if err := r.Status().Update(ctx, suite); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: reconcileTickInterval}, nil
}

// advanceRunning is the steady-state reconcile tick: refresh counts from
// the per-iteration CRs we own, schedule the next batch up to concurrency
// (unless spec.suspend holds scheduling), and settle the phase: Completed when
// every iteration is terminal, Paused when suspended with nothing in flight,
// otherwise Running.
func (r *CrewFitnessSuiteReconciler) advanceRunning(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) (ctrl.Result, error) {
	children, err := r.listChildren(ctx, suite)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing children: %w", err)
	}
	progress := summarizeChildren(children)

	if !suite.Spec.Suspend {
		if err := r.scheduleBatch(ctx, suite, children, progress.inFlight); err != nil {
			return ctrl.Result{}, err
		}
	}

	if suite.Spec.Suspend && progress.inFlight == 0 {
		// About to report Paused: confirm against the API server, since the
		// cache may not yet hold a child created on the previous tick.
		if progress, err = r.liveProgress(ctx, suite); err != nil {
			return ctrl.Result{}, err
		}
	}

	applyProgress(suite, progress)
	suite.Status.Phase = runningNextPhase(suite.Spec.Suspend, progress, suite.Status.IterationsTotal)
	if suite.Status.Phase == kubemootv1alpha1.CrewFitnessSuitePhaseCompleted {
		// Phase reflects EXECUTION lifecycle, not aggregate test outcome: all
		// iterations reached a terminal state, so the suite ran to completion
		// even if some iterations failed or errored their assertions. Those
		// outcomes live in passed/failed/errored. Failed/Error is reserved for
		// the suite itself failing to execute (setSuiteError). The XLSX write
		// and child reap happen in terminalUpkeep, one idempotent path.
		now := metav1.Now()
		suite.Status.CompletedAt = &now
	}
	if err := r.Status().Update(ctx, suite); err != nil {
		return ctrl.Result{}, err
	}
	if suite.Status.Phase == kubemootv1alpha1.CrewFitnessSuitePhasePaused {
		// Nothing in flight and nothing to schedule: wait for a spec edit
		// (resume or cancel), which re-triggers reconcile through the watch.
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: reconcileTickInterval}, nil
}

// runningNextPhase is the pure phase decision for a Running tick. Completion
// wins (the last in-flight iteration may finish while suspended). A suspended
// suite reads Paused only once nothing is in flight, so Paused always means
// the crew is idle.
func runningNextPhase(suspend bool, p childProgress, total int32) kubemootv1alpha1.CrewFitnessSuitePhase {
	if p.completed >= total {
		return kubemootv1alpha1.CrewFitnessSuitePhaseCompleted
	}
	if suspend && p.inFlight == 0 {
		return kubemootv1alpha1.CrewFitnessSuitePhasePaused
	}
	return kubemootv1alpha1.CrewFitnessSuitePhaseRunning
}

// liveProgress summarizes the suite's children read uncached.
func (r *CrewFitnessSuiteReconciler) liveProgress(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) (childProgress, error) {
	children, err := r.listLiveChildren(ctx, suite)
	if err != nil {
		return childProgress{}, fmt.Errorf("listing children uncached: %w", err)
	}
	return summarizeChildren(children), nil
}

// applyProgress copies the per-tick child rollup into the suite status.
func applyProgress(suite *kubemootv1alpha1.CrewFitnessSuite, p childProgress) {
	suite.Status.IterationsCompleted = p.completed
	suite.Status.Passed = p.passed
	suite.Status.Failed = p.failed
	suite.Status.Errored = p.errored
}

// scheduleBatch creates per-iteration CRs until inFlight reaches
// spec.concurrency or nothing is left to schedule. The existing children form
// the dedup set, so a resumed suite picks up at the next unscheduled iteration.
func (r *CrewFitnessSuiteReconciler) scheduleBatch(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite, children []kubemootv1alpha1.CrewFitness, inFlight int) error {
	scheduled := make(map[string]bool, len(children))
	for i := range children {
		scheduled[children[i].Name] = true
	}
	concurrency := max(int(suite.Spec.Concurrency), 1)
	for inFlight < concurrency {
		next := r.findNextIteration(suite, scheduled)
		if next == nil {
			return nil // nothing left to schedule
		}
		if err := r.createIterationCR(ctx, suite, next); err != nil {
			return fmt.Errorf("creating iteration CR: %w", err)
		}
		scheduled[next.crName] = true
		inFlight++
	}
	return nil
}

// advancePaused handles a Paused suite. Clearing spec.suspend flips it back to
// Running and requeues so advanceRunning schedules the next iteration at once.
// While still suspended there is nothing to do and no requeue: the next spec
// edit re-triggers reconcile. (spec.cancel is handled before this in
// dispatchPhase.)
func (r *CrewFitnessSuiteReconciler) advancePaused(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) (ctrl.Result, error) {
	if suite.Spec.Suspend {
		return ctrl.Result{}, nil
	}
	logf.FromContext(ctx).Info("Resuming fitness suite", "name", suite.Name,
		"completed", suite.Status.IterationsCompleted, "total", suite.Status.IterationsTotal)
	suite.Status.Phase = kubemootv1alpha1.CrewFitnessSuitePhaseRunning
	if err := r.Status().Update(ctx, suite); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: reconcileTickInterval}, nil
}

// cancelSuite stops a non-terminal suite. It deletes every iteration still in
// flight (its Job and pods follow through owner refs), recounts from the
// iterations that completed, and moves the suite to the terminal Cancelled
// phase. terminalUpkeep then writes the partial XLSX from the completed
// children and reaps them; the deferred judge is skipped for a cancelled run.
func (r *CrewFitnessSuiteReconciler) cancelSuite(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) (ctrl.Result, error) {
	children, err := r.listLiveChildren(ctx, suite)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing children for cancel: %w", err)
	}
	stopped, err := r.deleteInFlight(ctx, children)
	if err != nil {
		return ctrl.Result{}, err
	}
	applyProgress(suite, summarizeChildren(terminalChildren(children)))
	if suite.Status.RunID == "" {
		// Cancelled before it started: record the run identity and the planned
		// total so the status and any report read "0 of N".
		suite.Status.RunID = generateRunID(suite)
		suite.Status.IterationsTotal = int32(len(suite.Spec.Scripts)) * suite.Spec.Iterations
	}
	now := metav1.Now()
	suite.Status.Phase = kubemootv1alpha1.CrewFitnessSuitePhaseCancelled
	suite.Status.CompletedAt = &now
	if err := r.Status().Update(ctx, suite); err != nil {
		return ctrl.Result{}, err
	}
	logf.FromContext(ctx).Info("Cancelled fitness suite", "name", suite.Name,
		"completed", suite.Status.IterationsCompleted, "total", suite.Status.IterationsTotal,
		"stoppedInFlight", stopped)
	return ctrl.Result{RequeueAfter: reconcileTickInterval}, nil
}

// deleteInFlight deletes the children that have not reached a terminal phase
// and returns how many it deleted. Background propagation lets the garbage
// collector remove each child's Job and pods through owner refs. The delete is
// conditional on the resourceVersion that was read: a child that finished in
// the meantime fails with Conflict instead of losing its result, and the error
// makes the next reconcile re-list and recount.
func (r *CrewFitnessSuiteReconciler) deleteInFlight(ctx context.Context, children []kubemootv1alpha1.CrewFitness) (int, error) {
	deleted := 0
	for i := range children {
		c := &children[i]
		if isFitnessTerminal(c.Status.Phase) {
			continue
		}
		rv := c.ResourceVersion
		err := r.Delete(ctx, c,
			client.PropagationPolicy(metav1.DeletePropagationBackground),
			client.Preconditions{ResourceVersion: &rv})
		switch {
		case err == nil:
			deleted++
		case apierrors.IsNotFound(err):
			// already gone
		default:
			return deleted, fmt.Errorf("deleting in-flight iteration %s: %w", c.Name, err)
		}
	}
	return deleted, nil
}

// isFitnessTerminal reports whether a CrewFitness run has finished. It is the
// single definition of the terminal CrewFitness phases for both reconcilers.
func isFitnessTerminal(phase kubemootv1alpha1.CrewFitnessPhase) bool {
	switch phase {
	case kubemootv1alpha1.CrewFitnessPhasePassed,
		kubemootv1alpha1.CrewFitnessPhaseFailed,
		kubemootv1alpha1.CrewFitnessPhaseError:
		return true
	}
	return false
}

// terminalChildren keeps the children that finished. A cancelled suite's
// in-flight children are being deleted and must not appear in its counts or
// its XLSX; for a Completed suite every child is terminal, so this is a no-op.
func terminalChildren(children []kubemootv1alpha1.CrewFitness) []kubemootv1alpha1.CrewFitness {
	out := make([]kubemootv1alpha1.CrewFitness, 0, len(children))
	for i := range children {
		if isFitnessTerminal(children[i].Status.Phase) && children[i].DeletionTimestamp.IsZero() {
			out = append(out, children[i])
		}
	}
	return out
}

// listChildren returns all per-iteration CrewFitness CRs owned by this
// suite (matched via the suiteOwnerLabel selector).
func (r *CrewFitnessSuiteReconciler) listChildren(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) ([]kubemootv1alpha1.CrewFitness, error) {
	return listSuiteChildren(ctx, r.Client, suite)
}

// listLiveChildren lists the suite's children uncached (see APIReader).
func (r *CrewFitnessSuiteReconciler) listLiveChildren(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) ([]kubemootv1alpha1.CrewFitness, error) {
	if r.APIReader == nil {
		return r.listChildren(ctx, suite)
	}
	return listSuiteChildren(ctx, r.APIReader, suite)
}

func listSuiteChildren(ctx context.Context, reader client.Reader, suite *kubemootv1alpha1.CrewFitnessSuite) ([]kubemootv1alpha1.CrewFitness, error) {
	list := &kubemootv1alpha1.CrewFitnessList{}
	if err := reader.List(ctx, list,
		client.InNamespace(suite.Namespace),
		client.MatchingLabels{suiteOwnerLabel: suite.Name}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// terminalUpkeep runs on every reconcile while the suite is in a terminal
// phase. It enforces two post-run invariants WITHOUT any manual cleanup:
//
//  1. The XLSX artifact exists in NATS. If it's not yet written (or a prior
//     write failed transiently), retry the write while the children are
//     still present to harvest from.
//  2. Once the artifact is safely written, the per-iteration children are
//     reaped — their data now lives in the XLSX, so the 390-CR clutter is
//     redundant. Children's owned Jobs (and their pods) cascade via
//     owner-ref.
//
// The function is idempotent and retry-safe: each invocation makes one unit
// of progress (write the artifact, or reap a batch of children) and requeues
// until the steady state — artifact present, zero children — at which point
// it returns with no requeue and the suite is genuinely done with no garbage.
//
// Children must NOT be reaped before the artifact write succeeds: a permanent
// write failure (NATS misconfigured) leaves the children intact for
// debugging and logs the error each tick, rather than silently destroying
// the only copy of the results.
func (r *CrewFitnessSuiteReconciler) terminalUpkeep(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) (ctrl.Result, error) {
	// Post-suite DEFER judging: score deferred assertions via their keyword's crew
	// once the run is terminal. Idempotent (sidecar-guarded), deduped, and runs the
	// slow crew calls off-reconcile — never inline during the run.
	if !judgeSkipped(suite) {
		r.runDeferredJudgePass(suite)
	}

	children, err := r.listChildren(ctx, suite)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing children for terminal upkeep: %w", err)
	}

	// The deferred judge scores the run's frozen transcripts ASYNCHRONOUSLY
	// after the suite goes terminal, so quality is 0-until-judged at the moment
	// of completion. judgeScores feeds the XLSX; judgeComplete gates the final
	// rewrite + reap so the children (the IterationResults source) survive long
	// enough to rebuild the artifact with real scores.
	judgeComplete, judgeScores := r.deferredJudgeState(suite)

	switch terminalUpkeepAction(suite.Status.ArtifactRef != nil, judgeComplete, len(children)) {
	case actionGiveUp:
		// No artifact and nothing left to harvest. Only reachable if a human
		// force-deleted the children before the harvest ran; give up cleanly
		// rather than spin.
		return ctrl.Result{}, nil

	case actionWriteProvisional:
		// Write an artifact even before the judge completes so a judge that
		// never finishes (a regression, a hard crash) still yields an artifact
		// (0/partial quality) rather than none and an unbounded pile of children.
		return r.writeProvisionalArtifact(ctx, suite, children, judgeScores)

	case actionWaitForJudge:
		// Provisional artifact exists, but DON'T reap while the deferred judge
		// is still scoring: the children are the only source of IterationResults
		// for the final, score-embedded rebuild. State-driven — we wait on judge
		// completion, never a clock.
		return ctrl.Result{RequeueAfter: artifactRetryInterval}, nil

	case actionFinalizeAndReap:
		// Judge complete. Regenerate the artifact WITH the final quality scores
		// (children still present), then reap. One authoritative rewrite replaces
		// the 0-until-judged provisional; closes [[Fitness XLSX Not Regenerated
		// After Deferred Judge]].
		return r.finalizeArtifactAndReap(ctx, suite, children, judgeScores)

	default: // actionDone
		return ctrl.Result{}, nil // steady state: artifact final, no leftover garbage
	}
}

// persistArtifact writes the suite's XLSX and stores the resulting ref into
// status. It returns ok=false (with a possible requeue Result) when the write
// failed or NATS returned nil — the caller keeps the children (the only copy)
// and retries later. failLabel distinguishes the provisional vs final rewrite
// in the retry log. On success the status is updated and ok=true is returned.
func (r *CrewFitnessSuiteReconciler) persistArtifact(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite, children []kubemootv1alpha1.CrewFitness, judgeScores map[string]float64, failLabel string) (ctrl.Result, bool, error) {
	ref, werr := r.writeArtifact(ctx, suite, children, judgeScores)
	if werr != nil || ref == nil {
		if werr != nil {
			logf.FromContext(ctx).Error(werr, failLabel,
				"suite", suite.Name, "namespace", suite.Namespace)
		}
		return ctrl.Result{RequeueAfter: artifactRetryInterval}, false, nil
	}
	suite.Status.ArtifactRef = ref
	if err := r.Status().Update(ctx, suite); err != nil {
		return ctrl.Result{}, false, err
	}
	return ctrl.Result{}, true, nil
}

// writeProvisionalArtifact handles actionWriteProvisional: write the artifact
// (keeping children on failure) and requeue.
func (r *CrewFitnessSuiteReconciler) writeProvisionalArtifact(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite, children []kubemootv1alpha1.CrewFitness, judgeScores map[string]float64) (ctrl.Result, error) {
	res, ok, err := r.persistArtifact(ctx, suite, children, judgeScores,
		"fitness suite artifact write failed; will retry")
	if !ok {
		return res, err
	}
	return ctrl.Result{RequeueAfter: reconcileTickInterval}, nil
}

// finalizeArtifactAndReap handles actionFinalizeAndReap: rewrite the artifact
// with the final judge scores (children still present), then reap the children.
func (r *CrewFitnessSuiteReconciler) finalizeArtifactAndReap(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite, children []kubemootv1alpha1.CrewFitness, judgeScores map[string]float64) (ctrl.Result, error) {
	res, ok, err := r.persistArtifact(ctx, suite, children, judgeScores,
		"fitness suite final artifact rewrite failed; will retry")
	if !ok {
		return res, err
	}
	if err := r.reapChildren(ctx, suite); err != nil {
		return ctrl.Result{}, fmt.Errorf("reaping children: %w", err)
	}
	logf.FromContext(ctx).Info("Rewrote artifact with deferred-judge scores and reaped children",
		"suite", suite.Name, "namespace", suite.Namespace, "reaped", len(children),
		"scored", len(judgeScores))
	// Requeue to confirm convergence to zero (DeleteAllOf is async).
	return ctrl.Result{RequeueAfter: reconcileTickInterval}, nil
}

// terminalAction is the decision terminalUpkeep takes on one tick.
type terminalAction int

const (
	actionDone             terminalAction = iota // artifact final + no children: nothing to do
	actionGiveUp                                 // no artifact, no children to harvest from
	actionWriteProvisional                       // no artifact yet: write one (children present)
	actionWaitForJudge                           // artifact exists, deferred judge still scoring: keep children
	actionFinalizeAndReap                        // judge done, children present: rewrite with scores then reap
)

// terminalUpkeepAction is the pure reap-gate decision. Critical invariant:
// children are NEVER reaped while the deferred judge is incomplete (they are the
// only source of IterationResults for the score-embedded rebuild), and a
// provisional artifact is always written so a stuck judge cannot strand the run
// without any artifact.
func terminalUpkeepAction(hasArtifact, judgeComplete bool, childCount int) terminalAction {
	if !hasArtifact {
		if childCount == 0 {
			return actionGiveUp
		}
		return actionWriteProvisional
	}
	if !judgeComplete {
		return actionWaitForJudge
	}
	if childCount == 0 {
		return actionDone
	}
	return actionFinalizeAndReap
}

// deferredJudgeState reads the deferred-judge checkpoint for the suite's run.
// With no NATS there is no deferred judge → report complete with no scores so
// terminalUpkeep keeps the original write-then-reap flow.
func (r *CrewFitnessSuiteReconciler) deferredJudgeState(suite *kubemootv1alpha1.CrewFitnessSuite) (complete bool, scores map[string]float64) {
	if r.NATSPublisher == nil || suite.Status.RunID == "" || judgeSkipped(suite) {
		return true, nil
	}
	prefix := fmt.Sprintf("%s/%s/%s/", suite.Namespace, suite.Name, suite.Status.RunID)
	cache := loadDeferredCache(r.NATSPublisher, prefix)
	return cache.Complete, cache.Scores
}

// judgeSkipped reports whether the deferred judge pass is skipped for this
// suite: a cancelled suite is not judged, and its partial XLSX keeps quality
// unjudged.
func judgeSkipped(suite *kubemootv1alpha1.CrewFitnessSuite) bool {
	return suite.Status.Phase == kubemootv1alpha1.CrewFitnessSuitePhaseCancelled
}

// reapChildren deletes all per-iteration CrewFitness children owned by the
// suite via label selector. Their owned Jobs + pods cascade via owner-ref.
// Idempotent — deleting already-gone children is a no-op.
func (r *CrewFitnessSuiteReconciler) reapChildren(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) error {
	return r.DeleteAllOf(ctx, &kubemootv1alpha1.CrewFitness{},
		client.InNamespace(suite.Namespace),
		client.MatchingLabels{suiteOwnerLabel: suite.Name})
}

// childProgress is the per-tick rollup of child-CR state.
type childProgress struct {
	completed int32 // any terminal phase
	inFlight  int   // Pending OR Running
	passed    int32
	failed    int32
	errored   int32
}

func summarizeChildren(children []kubemootv1alpha1.CrewFitness) childProgress {
	var p childProgress
	for i := range children {
		phase := children[i].Status.Phase
		if !isFitnessTerminal(phase) {
			p.inFlight++ // Pending / Running / empty: still in flight
			continue
		}
		p.completed++
		switch phase {
		case kubemootv1alpha1.CrewFitnessPhasePassed:
			p.passed++
		case kubemootv1alpha1.CrewFitnessPhaseFailed:
			p.failed++
		default:
			p.errored++
		}
	}
	return p
}

// nextIteration identifies the next (scriptIdx, iter) pair the reconciler
// should schedule. nil if all iterations have already been scheduled.
type nextIteration struct {
	scriptIdx int
	iter      int32
	crName    string
	script    kubemootv1alpha1.SuiteScript
}

func (r *CrewFitnessSuiteReconciler) findNextIteration(suite *kubemootv1alpha1.CrewFitnessSuite, scheduled map[string]bool) *nextIteration {
	for iter := int32(1); iter <= suite.Spec.Iterations; iter++ {
		for scriptIdx, script := range suite.Spec.Scripts {
			name := iterationCRName(suite.Status.RunID, scriptIdx, iter)
			if scheduled[name] {
				continue
			}
			return &nextIteration{
				scriptIdx: scriptIdx,
				iter:      iter,
				crName:    name,
				script:    script,
			}
		}
	}
	return nil
}

// createIterationCR creates one per-iteration CrewFitness CR with the
// suite ownership labels and the script's content/configMapRef threaded
// through.
//
// CRITICAL: per-iteration children MUST NOT carry a TTL. The suite's
// progress accounting (iterationsCompleted, pass/fail counts) and the
// end-of-run XLSX harvest both derive from the LIVE set of owned child
// CRs. If children self-deleted via TTL, the count would plateau around
// "one TTL window of throughput", never reach IterationsTotal (the suite
// would run forever), and the final XLSX would contain only the handful
// of children alive at write time — losing every result older than the
// TTL. Observed 2026-06-01 on a 390-run baseline: a 1h child TTL made
// iterationsCompleted oscillate ~50 and decrease over time. Children are
// cleaned up by the suite's owner-ref cascade when the suite is deleted.
func (r *CrewFitnessSuiteReconciler) createIterationCR(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite, next *nextIteration) error {
	cf := buildIterationCR(suite, next)
	if err := controllerSetOwnerReference(suite, cf, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference: %w", err)
	}
	if err := r.Create(ctx, cf); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	return nil
}

// buildIterationCR constructs (but does not create) the per-iteration
// CrewFitness CR. Pure function — no client, no owner-ref — so the
// "children carry NO TTL" invariant is unit-testable. See createIterationCR
// for why TTL must stay unset.
func buildIterationCR(suite *kubemootv1alpha1.CrewFitnessSuite, next *nextIteration) *kubemootv1alpha1.CrewFitness {
	return &kubemootv1alpha1.CrewFitness{
		ObjectMeta: metav1.ObjectMeta{
			Name:      next.crName,
			Namespace: suite.Namespace,
			Labels: map[string]string{
				suiteOwnerLabel:          suite.Name,
				fitnessKindLabel:         fitnessKindRunInstance,
				suiteRunIDLabel:          suite.Status.RunID,
				suiteScriptIndexLabel:    fmt.Sprintf("%d", next.scriptIdx),
				suiteIterationIndexLabel: fmt.Sprintf("%d", next.iter),
			},
		},
		Spec: kubemootv1alpha1.CrewFitnessSpec{
			CrewRef:      suite.Spec.CrewRef,
			TestRef:      next.script.TestRef,
			TestContent:  next.script.TestContent,
			ConfigMapRef: next.script.ConfigMapRef,
			// TTL deliberately unset — owner-ref cascade handles cleanup.
		},
	}
}

// writeArtifact harvests per-iteration results from the owned child CRs,
// builds the XLSX, and writes it to the NATS Object Store under the
// suite-namespaced object key. Returns the populated artifactRef on
// success, nil + error otherwise. Caller treats errors as non-fatal —
// the suite still reaches terminal phase; only the download link is
// missing.
// writeArtifact builds and stores the suite's XLSX. judgeQuality carries the
// deferred judge's per-scenario quality (0-100); pass nil/partial before the
// judge completes and the full map once it does, so the final artifact embeds
// real scores instead of the 0-until-judged placeholders. See terminalUpkeep
// for the regenerate-then-reap ordering.
func (r *CrewFitnessSuiteReconciler) writeArtifact(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite, children []kubemootv1alpha1.CrewFitness, judgeQuality map[string]float64) (*kubemootv1alpha1.SuiteArtifactRef, error) {
	if r.NATSPublisher == nil {
		// NATS not configured (dev-without-NATS path). Skip silently —
		// the suite still completes, status counts are accurate.
		return nil, nil
	}
	results := HarvestIterationResults(terminalChildren(children))
	// Provenance: enrich the in-memory suite with the crew chart version so the
	// Overview tab attributes this run to a specific crew version.
	stampCrewVersion(ctx, r, suite)
	// consistency is computed elsewhere (currently unused → nil); judgeQuality
	// is threaded from the deferred-judge checkpoint so the Quality/Scenarios
	// tabs reflect the reference-grounded scores.
	xlsxBytes, err := BuildFitnessSuiteXLSXWithMeasures(suite, results, nil, judgeQuality)
	if err != nil {
		return nil, fmt.Errorf("building XLSX: %w", err)
	}

	ttl := defaultArtifactRetention
	if suite.Spec.ArtifactRetention != nil && suite.Spec.ArtifactRetention.Duration > 0 {
		ttl = suite.Spec.ArtifactRetention.Duration
	}
	objectKey := fmt.Sprintf("%s/%s/%s.xlsx", suite.Namespace, suite.Name, suite.Status.RunID)
	info, err := r.NATSPublisher.PutObject(FitnessArtifactsBucket, objectKey, xlsxBytes, ttl)
	if err != nil {
		return nil, fmt.Errorf("writing artifact to NATS: %w", err)
	}
	ref := &kubemootv1alpha1.SuiteArtifactRef{
		Bucket:    FitnessArtifactsBucket,
		ObjectKey: objectKey,
	}
	if info != nil {
		// info.Size is the canonical byte count from the object store.
		ref.SizeBytes = int64(info.Size)
	} else {
		// Best-effort fallback when NATS returned nil info (NATS unset).
		ref.SizeBytes = int64(len(xlsxBytes))
	}
	_ = ctx // ctx kept in signature for future cancel propagation
	return ref, nil
}

func (r *CrewFitnessSuiteReconciler) setSuiteError(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite, msg string) (ctrl.Result, error) {
	suite.Status.Phase = kubemootv1alpha1.CrewFitnessSuitePhaseError
	suite.Status.Error = msg
	now := metav1.Now()
	suite.Status.CompletedAt = &now
	if err := r.Status().Update(ctx, suite); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// validateSuiteSpec rejects suites whose scripts are missing both
// TestContent and ConfigMapRef. Each script needs one source so the
// per-iteration CrewFitness CR can be created without the operator
// guessing.
func validateSuiteSpec(suite *kubemootv1alpha1.CrewFitnessSuite) error {
	if suite.Spec.CrewRef == "" {
		return fmt.Errorf("spec.crewRef is required")
	}
	if suite.Spec.Iterations < 1 {
		return fmt.Errorf("spec.iterations must be >= 1")
	}
	if len(suite.Spec.Scripts) == 0 {
		return fmt.Errorf("spec.scripts must list at least one script")
	}
	for i, s := range suite.Spec.Scripts {
		if s.TestRef == "" {
			return fmt.Errorf("spec.scripts[%d].testRef is required", i)
		}
		hasContent := s.TestContent != ""
		hasConfigMap := s.ConfigMapRef != ""
		if hasContent == hasConfigMap {
			return fmt.Errorf("spec.scripts[%d]: exactly one of testContent or configMapRef must be set", i)
		}
	}
	return nil
}

// generateRunID returns a short stable identifier for this suite execution.
// Format: UID prefix + creation timestamp — both are derived from existing
// metadata so the same Reconcile transition produces the same runId on
// retry.
func generateRunID(suite *kubemootv1alpha1.CrewFitnessSuite) string {
	uidPrefix := string(suite.UID)
	if len(uidPrefix) > 8 {
		uidPrefix = uidPrefix[:8]
	}
	return uidPrefix
}

// iterationCRName builds the per-iteration CrewFitness CR name from the
// suite runId + script index + iteration number. Stable across reconciles
// for a given (runId, scriptIdx, iter) — lets findNextIteration use
// "is this name in the scheduled set?" as the dedup primitive.
func iterationCRName(runID string, scriptIdx int, iter int32) string {
	return fmt.Sprintf("run-%s-s%d-i%d", runID, scriptIdx, iter)
}

// controllerSetOwnerReference wraps controllerutil.SetControllerReference
// so the import path is consolidated in this package (avoids drift if the
// upstream helper relocates).
func controllerSetOwnerReference(owner, obj client.Object, scheme *runtime.Scheme) error {
	return controllerutil.SetControllerReference(owner, obj, scheme)
}

func (r *CrewFitnessSuiteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubemootv1alpha1.CrewFitnessSuite{}).
		Owns(&kubemootv1alpha1.CrewFitness{}).
		Named("crewfitnesssuite").
		Complete(r)
}
