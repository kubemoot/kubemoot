/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// fitness_trials.go keeps the trials of a suite independent: with the default
// PerTrial isolation the crew's working memory is cleared before every trial and
// the trials run one at a time, so a fact the crew learns in one trial cannot
// help the next. The suite's TrialsIndependent condition says whether that held.
package controller

import (
	"errors"
	"fmt"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Reasons of the TrialsIndependent condition.
const (
	reasonPerTrialPurge = "PerTrialPurge"
	reasonSharedMemory  = "SharedAcrossTrials"
	reasonPurgeDisabled = "PurgeDisabled"
	reasonPurgeFailed   = "PurgeFailed"
	reasonNoMemoryStore = "NoMemoryStore"
)

// memoryPurger clears keys of the crew-memory bucket by prefix.
type memoryPurger interface {
	PurgeKVPrefix(bucket, prefix string) (int, error)
}

// memoryPurger returns what clears crew memory: the test hook, else the NATS
// publisher; nil when the operator has no bus. Returning an untyped nil keeps a
// nil publisher from becoming a non-nil interface.
func (r *CrewFitnessSuiteReconciler) memoryPurger() memoryPurger {
	switch {
	case r.memory != nil:
		return r.memory
	case r.NATSPublisher != nil:
		return r.NATSPublisher
	default:
		return nil
	}
}

// trialIsolation is the suite's isolation with the default applied.
func trialIsolation(suite *kubemootv1alpha1.CrewFitnessSuite) kubemootv1alpha1.FitnessTrialIsolation {
	if suite.Spec.TrialIsolation == "" {
		return kubemootv1alpha1.FitnessTrialIsolationPerTrial
	}
	return suite.Spec.TrialIsolation
}

// purgesPerTrial reports whether crew memory is cleared before every trial.
func purgesPerTrial(suite *kubemootv1alpha1.CrewFitnessSuite) bool {
	return kubemootv1alpha1.BoolOrTrue(suite.Spec.PurgeMemory) &&
		trialIsolation(suite) == kubemootv1alpha1.FitnessTrialIsolationPerTrial
}

// effectiveConcurrency is how many trials run at once. Trials that must not
// share memory run one at a time: crew memory is per crew, so two trials in
// flight would share it.
func effectiveConcurrency(suite *kubemootv1alpha1.CrewFitnessSuite) int {
	if purgesPerTrial(suite) {
		return 1
	}
	return max(int(suite.Spec.Concurrency), 1)
}

// purgeBeforeTrial clears the crew's working memory before a trial starts. A
// failed purge is returned so the trial is not created and the reconcile
// retries: a trial that ran on unpurged memory would not be independent.
func (r *CrewFitnessSuiteReconciler) purgeBeforeTrial(suite *kubemootv1alpha1.CrewFitnessSuite) error {
	if !purgesPerTrial(suite) {
		return nil
	}
	purger := r.memoryPurger()
	if purger == nil {
		return nil // recorded by the TrialsIndependent condition at start
	}
	scope := crewscope.Scope{Namespace: suite.Namespace, Crew: suite.Spec.CrewRef}
	if _, err := purger.PurgeKVPrefix(crewMemoryBucket, scope.MemoryPrefix()); err != nil {
		return fmt.Errorf("%w: %w", errMemoryClearFailed, err)
	}
	if c := meta.FindStatusCondition(suite.Status.Conditions, kubemootv1alpha1.ConditionTrialsIndependent); c != nil && c.Reason == reasonPurgeFailed {
		r.recordTrialIsolation(suite) // the clear works again
	}
	return nil
}

// errMemoryClearFailed marks a failed clear of crew memory before a trial.
var errMemoryClearFailed = errors.New("clearing crew memory before a trial")

// recordPurgeFailure marks the trials as not independent while the clear fails.
func (r *CrewFitnessSuiteReconciler) recordPurgeFailure(suite *kubemootv1alpha1.CrewFitnessSuite, err error) {
	meta.SetStatusCondition(&suite.Status.Conditions, metav1.Condition{
		Type: kubemootv1alpha1.ConditionTrialsIndependent, Status: metav1.ConditionFalse,
		Reason: reasonPurgeFailed, Message: "the next trial is held until crew memory is cleared: " + err.Error(),
	})
}

// trialsIndependentCondition says whether this suite's trials share crew memory,
// and why not when they do.
func (r *CrewFitnessSuiteReconciler) trialsIndependentCondition(suite *kubemootv1alpha1.CrewFitnessSuite) metav1.Condition {
	cond := metav1.Condition{Type: kubemootv1alpha1.ConditionTrialsIndependent, Status: metav1.ConditionFalse}
	switch {
	case !kubemootv1alpha1.BoolOrTrue(suite.Spec.PurgeMemory):
		cond.Reason = reasonPurgeDisabled
		cond.Message = "spec.purgeMemory is false: crew memory is never cleared, so trials share what the crew learns"
	case trialIsolation(suite) == kubemootv1alpha1.FitnessTrialIsolationPerSuite:
		cond.Reason = reasonSharedMemory
		cond.Message = "spec.trialIsolation is PerSuite: crew memory is cleared once, so later trials can use what earlier trials taught the crew"
	case r.memoryPurger() == nil:
		cond.Reason = reasonNoMemoryStore
		cond.Message = "the operator has no message bus to clear crew memory with, so trials share what the crew learns"
	default:
		cond.Status = metav1.ConditionTrue
		cond.Reason = reasonPerTrialPurge
		cond.Message = "crew memory is cleared before every trial"
		if suite.Spec.Concurrency > 1 {
			cond.Message += fmt.Sprintf("; trials run one at a time, so spec.concurrency %d does not apply", suite.Spec.Concurrency)
		}
	}
	return cond
}

// recordTrialIsolation sets the TrialsIndependent condition on the suite.
func (r *CrewFitnessSuiteReconciler) recordTrialIsolation(suite *kubemootv1alpha1.CrewFitnessSuite) {
	meta.SetStatusCondition(&suite.Status.Conditions, r.trialsIndependentCondition(suite))
}
