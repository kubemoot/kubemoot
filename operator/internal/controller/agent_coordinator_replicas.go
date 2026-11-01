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

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	// ConditionReplicasLimited is the Agent condition that says a coordinator was run with one
	// replica although its spec asks for more, because coordinator leases are off. It is absent
	// when the spec is honored.
	ConditionReplicasLimited = "ReplicasLimited"

	reasonLeasesOff = "CoordinatorLeasesOff"
)

// coordinatorReplicasLimit is the replica count a coordinator Agent is held to when leases are
// off: without leases nothing decides which replica owns a thread, so a second one would act on
// threads the first one owns. It returns 0 when there is no limit (a non-coordinator, or leases on).
func (r *AgentReconciler) coordinatorReplicasLimit(agent *kubemootv1alpha1.Agent) int32 {
	if agent.Spec.DiscussRole != roleCoordinator || r.coordinatorLeasesOn() {
		return 0
	}
	return 1
}

// setCoordinatorStrategy gives a coordinator's Deployment its rollout strategy, in either
// harness mode: Recreate, or RollingUpdate when leases are on. Other Agents keep the default.
func (r *AgentReconciler) setCoordinatorStrategy(d *appsv1.Deployment, agent *kubemootv1alpha1.Agent) {
	if agent.Spec.DiscussRole == roleCoordinator {
		d.Spec.Strategy = coordinatorStrategy(r.coordinatorLeasesOn())
	}
}

// replicasLimitedCondition builds the condition for an Agent, or nil when the spec is honored.
func (r *AgentReconciler) replicasLimitedCondition(agent *kubemootv1alpha1.Agent) *metav1.Condition {
	limit := r.coordinatorReplicasLimit(agent)
	if limit == 0 || agent.Spec.Deployment == nil || agent.Spec.Deployment.Replicas <= limit {
		return nil
	}
	return &metav1.Condition{
		Type: ConditionReplicasLimited, Status: metav1.ConditionTrue, Reason: reasonLeasesOff,
		ObservedGeneration: agent.Generation,
		Message: fmt.Sprintf("spec.deployment.replicas is %d; the coordinator runs %d replica because "+
			"coordinator leases are off (KubemootConfig spec.coordinator.lease, with resume and the log on)",
			agent.Spec.Deployment.Replicas, limit),
	}
}

// syncReplicasLimitedCondition keeps the ReplicasLimited condition true to the Agent and the
// realm, and writes the status when it changed.
func (r *AgentReconciler) syncReplicasLimitedCondition(ctx context.Context, agent *kubemootv1alpha1.Agent) error {
	var changed bool
	if cond := r.replicasLimitedCondition(agent); cond != nil {
		changed = meta.SetStatusCondition(&agent.Status.Conditions, *cond)
	} else {
		changed = meta.RemoveStatusCondition(&agent.Status.Conditions, ConditionReplicasLimited)
	}
	if !changed {
		return nil
	}
	if err := r.Status().Update(ctx, agent); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("recording the ReplicasLimited condition in the status: %w", err)
	}
	return nil
}
