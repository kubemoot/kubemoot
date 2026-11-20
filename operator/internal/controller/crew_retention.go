/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/retention"
)

const (
	// conditionTypeRetentionValid says whether the Crew's retention policy was read: True
	// when it was, False when a value is invalid (nothing is removed until it is fixed).
	conditionTypeRetentionValid      = "RetentionValid"
	reasonRetentionValid             = "Valid"
	reasonRetentionInvalid           = "Invalid"
	reasonRetentionFinerThanSchedule = "FinerThanSchedule"
)

// reconcileRetention records the RetentionValid condition for the Crew's declared policy
// and warns, with an event, when the policy is invalid or a duration is finer than the
// cleanup schedule. The event is sent when the condition changes, not on every reconcile.
func (r *CrewReconciler) reconcileRetention(crew *kubemootv1alpha1.Crew) {
	status, reason, message := metav1.ConditionTrue, reasonRetentionValid, "Retention is declared and read."
	policy, err := retention.Resolve(crew)
	switch {
	case err != nil:
		status, reason, message = metav1.ConditionFalse, reasonRetentionInvalid, "Nothing is removed until this is fixed: "+err.Error()
	case len(policy.Warnings) > 0:
		reason, message = reasonRetentionFinerThanSchedule, strings.Join(policy.Warnings, "; ")
	}
	prev := meta.FindStatusCondition(crew.Status.Conditions, conditionTypeRetentionValid)
	changed := prev == nil || prev.Reason != reason || prev.Message != message
	setCrewCondition(crew, conditionTypeRetentionValid, status, reason, message)
	if changed && reason != reasonRetentionValid && r.Recorder != nil {
		r.Recorder.Eventf(crew, nil, corev1.EventTypeWarning, "Retention"+reason, "Reconcile", "%s", message)
	}
}

// memoryTTLDays is the days after which an agent prunes an unused fact on read, from the
// Crew's retention policy: 0 keeps every fact (never, or a policy that could not be read).
func memoryTTLDays(crew *kubemootv1alpha1.Crew, current int32) int32 {
	policy, err := retention.Resolve(crew)
	if err != nil {
		return 0
	}
	if !policy.Enforced() || !policy.Memory.IsSet() {
		return current
	}
	if policy.Memory.IsNever() {
		return 0
	}
	return policy.Memory.Days()
}
