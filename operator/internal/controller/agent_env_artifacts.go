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
// the discussion workspace and post a manifest of those objects as its contribution. It is absent
// when the realm leaves per-call artifacts off.
const envPerCallArtifacts = "KUBEMOOT_PER_CALL_ARTIFACTS"

// perCallArtifactsEnvVars returns the per-call artifacts env var for a tooler Agent when the realm
// (KubemootConfig spec.artifacts.perCallArtifacts) turns it on, and nothing otherwise or for any
// other role: only a tooler posts raw tool output. An Agent's own spec.deployment.env is appended
// after it and wins. A reconciler with no ConfigCache (test scaffolding only) adds nothing.
func (r *AgentReconciler) perCallArtifactsEnvVars(agent *kubemootv1alpha1.Agent) []corev1.EnvVar {
	if agent.Spec.DiscussRole != roleToolerResume || r.ConfigCache == nil || !r.ConfigCache.GetPerCallArtifacts() {
		return nil
	}
	return []corev1.EnvVar{{Name: envPerCallArtifacts, Value: valueTrue}}
}
