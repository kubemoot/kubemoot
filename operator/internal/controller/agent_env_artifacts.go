/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	corev1 "k8s.io/api/core/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// envPerCallArtifacts makes a raw-output tooler store each tool call's result as its own object in
// the discussion workspace and post a manifest of those objects as its contribution. An analyst
// then records which stored calls its tool calls read, and the coordinator builds the thread's
// catalog and coverage. It is absent when the realm leaves per-call artifacts off.
const envPerCallArtifacts = "KUBEMOOT_PER_CALL_ARTIFACTS"

// roleAnalystResume is the discussRole of an analyst agent.
const roleAnalystResume = "analyst"

// perCallArtifactsEnvVars returns the per-call artifacts env var for a tooler, an analyst and the
// coordinator when the realm (KubemootConfig spec.artifacts.perCallArtifacts) turns it on, and
// nothing otherwise or for any other role: a tooler writes the per-call objects, an analyst reads
// them and the coordinator catalogs them. An Agent's own spec.deployment.env is appended after it
// and wins. A reconciler with no ConfigCache (test scaffolding only) adds nothing.
func (r *AgentReconciler) perCallArtifactsEnvVars(agent *kubemootv1alpha1.Agent) []corev1.EnvVar {
	if !usesPerCallArtifacts(agent) || r.ConfigCache == nil || !r.ConfigCache.GetPerCallArtifacts() {
		return nil
	}
	return []corev1.EnvVar{{Name: envPerCallArtifacts, Value: valueTrue}}
}

// usesPerCallArtifacts reports whether the agent's role takes part in per-call artifacts.
func usesPerCallArtifacts(agent *kubemootv1alpha1.Agent) bool {
	switch agent.Spec.DiscussRole {
	case roleToolerResume, roleAnalystResume:
		return true
	default:
		return isCoordinator(agent)
	}
}
