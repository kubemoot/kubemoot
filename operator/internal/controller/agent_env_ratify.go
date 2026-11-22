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
	"strconv"

	corev1 "k8s.io/api/core/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	envRatifyEnabled   = "KUBEMOOT_DISCUSS_RATIFY_ENABLED"
	envRatifyMaxRounds = "KUBEMOOT_DISCUSS_RATIFY_MAX_ROUNDS"
	envRatifyRoster    = "KUBEMOOT_DISCUSS_RATIFY_ROSTER"
)

// coordinatorRatifyEnvVars tells the coordinator whether and how the contributors
// ratify its draft answer, as its crew's consensus archetype declares (the same
// archetype lookup as the review block). Nothing is set when the archetype is
// missing or declares no ratify block, and the runtime then ratifies with its
// defaults.
func (r *AgentReconciler) coordinatorRatifyEnvVars(ctx context.Context, agent *kubemootv1alpha1.Agent) []corev1.EnvVar {
	archetype := r.crewArchetype(ctx, agent)
	if archetype == nil {
		return nil
	}
	return ratifyEnvVars(archetype.Spec.Ratify)
}

// ratifyEnvVars is the coordinator environment for an archetype's ratify block.
func ratifyEnvVars(ratify *kubemootv1alpha1.ArchetypeRatify) []corev1.EnvVar {
	if ratify == nil {
		return nil
	}
	var env []corev1.EnvVar
	if ratify.Enabled != nil {
		env = append(env, corev1.EnvVar{Name: envRatifyEnabled, Value: strconv.FormatBool(*ratify.Enabled)})
	}
	if ratify.MaxRounds != nil {
		env = append(env, corev1.EnvVar{Name: envRatifyMaxRounds, Value: strconv.Itoa(int(*ratify.MaxRounds))})
	}
	if ratify.Roster != "" {
		env = append(env, corev1.EnvVar{Name: envRatifyRoster, Value: string(ratify.Roster)})
	}
	return env
}
