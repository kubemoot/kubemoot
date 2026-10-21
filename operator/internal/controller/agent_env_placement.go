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

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// envSchedulerPlacementPolicy tells the agent runtime which placement policy
// decides where its model calls run.
const envSchedulerPlacementPolicy = "KUBEMOOT_SCHEDULER_PLACEMENT_POLICY"

// envPlacementServiceURL tells the agent runtime to call the placement service
// at this URL. It is absent when agents place in-process.
const envPlacementServiceURL = "KUBEMOOT_PLACEMENT_SERVICE_URL"

// defaultPlacementPolicy applies when neither the crew nor the realm sets one.
const defaultPlacementPolicy = "algorithm"

// policyAppliesTo reports whether a CrewSchedulingPolicy governs agents of crew.
// An agent without a crew label, or a policy without a crewRef, matches any.
func policyAppliesTo(p *kubemootv1alpha1.CrewSchedulingPolicy, crew string) bool {
	return crew == "" || p.Spec.CrewRef == "" || p.Spec.CrewRef == crew
}

// resolvePlacementPolicy picks the crew's placement policy, else the realm's,
// else the default.
func resolvePlacementPolicy(crewPolicy *kubemootv1alpha1.CrewSchedulingPolicy, realm string) string {
	if crewPolicy != nil && crewPolicy.Spec.PlacementPolicy != "" {
		return crewPolicy.Spec.PlacementPolicy
	}
	if realm != "" {
		return realm
	}
	return defaultPlacementPolicy
}

// crewPlacementOverride returns the first policy, by name, for the agent's crew
// that sets spec.placementPolicy, or nil when none does.
func (r *AgentReconciler) crewPlacementOverride(ctx context.Context, agent *kubemootv1alpha1.Agent) *kubemootv1alpha1.CrewSchedulingPolicy {
	for _, p := range r.matchingPolicies(ctx, agent) {
		if p.Spec.PlacementPolicy != "" {
			return p
		}
	}
	return nil
}

// placementPolicyEnvVar returns the agent's resolved placement policy env var.
func (r *AgentReconciler) placementPolicyEnvVar(ctx context.Context, agent *kubemootv1alpha1.Agent) corev1.EnvVar {
	realm := ""
	if r.ConfigCache != nil {
		realm = r.ConfigCache.GetPlacementPolicy()
	}
	return corev1.EnvVar{
		Name:  envSchedulerPlacementPolicy,
		Value: resolvePlacementPolicy(r.crewPlacementOverride(ctx, agent), realm),
	}
}

// placementServiceEnvVars returns the placement service URL env var when the
// realm enables the service with a URL, and nothing otherwise.
func (r *AgentReconciler) placementServiceEnvVars() []corev1.EnvVar {
	if r.ConfigCache == nil {
		return nil
	}
	url := r.ConfigCache.GetPlacementServiceURL()
	if url == "" {
		return nil
	}
	return []corev1.EnvVar{{Name: envPlacementServiceURL, Value: url}}
}
