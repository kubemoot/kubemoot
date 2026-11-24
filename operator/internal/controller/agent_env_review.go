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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	envReviewConvene     = "KUBEMOOT_DISCUSS_REVIEW_CONVENE"
	envReviewFallback    = "KUBEMOOT_DISCUSS_REVIEW_FALLBACK"
	envReviewGeneralists = "KUBEMOOT_DISCUSS_REVIEW_GENERALISTS"
)

// +kubebuilder:rbac:groups=kubemoot.ai,resources=mootarchetypes,verbs=get;list;watch

// coordinatorReviewEnvVars tells the coordinator which analysts to convene for
// review, as its crew's consensus archetype declares. The crew's archetype is the
// archetypeRef of the CrewSchedulingPolicy that governs the crew (consent-3 when
// none sets one). Nothing is set when the archetype is missing or declares no
// review block, and the runtime then convenes every analyst.
func (r *AgentReconciler) coordinatorReviewEnvVars(ctx context.Context, agent *kubemootv1alpha1.Agent) []corev1.EnvVar {
	archetype := r.crewArchetype(ctx, agent)
	if archetype == nil {
		return nil
	}
	return reviewEnvVars(archetype.Spec.Review)
}

// crewArchetype is the consensus archetype that governs the agent's crew, or nil when it is
// not installed. The coordinator's review and ratify settings both come from it.
func (r *AgentReconciler) crewArchetype(ctx context.Context, agent *kubemootv1alpha1.Agent) *kubemootv1alpha1.MootArchetype {
	archetype := &kubemootv1alpha1.MootArchetype{}
	name := crewArchetypeName(r.matchingPolicies(ctx, agent))
	if err := r.Get(ctx, types.NamespacedName{Name: name}, archetype); err != nil {
		return nil
	}
	return archetype
}

// crewArchetypeName is the archetypeRef of the first policy (policies are ordered
// by name, as matchingPolicies returns them) that sets one, or the default archetype.
func crewArchetypeName(policies []*kubemootv1alpha1.CrewSchedulingPolicy) string {
	ref := ""
	for _, p := range policies {
		if p.Spec.ArchetypeRef != "" {
			ref = p.Spec.ArchetypeRef
			break
		}
	}
	return archetypeRefOrDefault(ref)
}

// enqueueCoordinatorsOnArchetypeChange re-reconciles every coordinator when a
// MootArchetype changes, so an edited review block reaches the crews that use it.
// A MootArchetype is cluster-scoped and a cluster has one coordinator per crew.
func enqueueCoordinatorsOnArchetypeChange(cli client.Client) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		if _, ok := obj.(*kubemootv1alpha1.MootArchetype); !ok {
			return nil
		}
		agents := &kubemootv1alpha1.AgentList{}
		if err := cli.List(ctx, agents); err != nil {
			logf.FromContext(ctx).Error(err, "MootArchetype propagation: list agents failed", "archetype", obj.GetName())
			return nil
		}
		return coordinatorRequests(agents.Items)
	})
}

// coordinatorRequests is a reconcile request for each coordinator among agents.
func coordinatorRequests(agents []kubemootv1alpha1.Agent) []reconcile.Request {
	var reqs []reconcile.Request
	for i := range agents {
		if isCoordinator(&agents[i]) {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&agents[i])})
		}
	}
	return reqs
}

// reviewEnvVars is the coordinator environment for an archetype's review block.
func reviewEnvVars(review *kubemootv1alpha1.ArchetypeReview) []corev1.EnvVar {
	if review == nil {
		return nil
	}
	var env []corev1.EnvVar
	if review.Convene != "" {
		env = append(env, corev1.EnvVar{Name: envReviewConvene, Value: string(review.Convene)})
	}
	if review.Fallback != "" {
		env = append(env, corev1.EnvVar{Name: envReviewFallback, Value: string(review.Fallback)})
	}
	if review.Generalists != "" {
		env = append(env, corev1.EnvVar{Name: envReviewGeneralists, Value: string(review.Generalists)})
	}
	return env
}
