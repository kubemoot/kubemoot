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
)

// envCoordinatorEventLog turns on the coordinator's decision log in the agent
// runtime: each decision is written to the KUBEMOOT_COORDINATOR stream before it
// is acted on. It is absent when the realm leaves the log off.
const envCoordinatorEventLog = "KUBEMOOT_COORDINATOR_EVENT_LOG"

// coordinatorEventLogEnvVars returns the decision-log env var when the realm
// (KubemootConfig spec.coordinator.eventLog) turns the log on, and nothing otherwise.
// An Agent's own spec.deployment.env is appended after it and wins.
func (r *AgentReconciler) coordinatorEventLogEnvVars() []corev1.EnvVar {
	if r.ConfigCache == nil || !r.ConfigCache.GetCoordinatorEventLog() {
		return nil
	}
	return []corev1.EnvVar{{Name: envCoordinatorEventLog, Value: valueTrue}}
}
