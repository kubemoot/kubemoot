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

// envCoordinatorResume lets a restarted coordinator resume the threads it had in flight,
// rebuilding each from its decision log. The runtime ignores it unless the log is on.
// It is absent when the realm leaves resume off.
const envCoordinatorResume = "KUBEMOOT_COORDINATOR_RESUME"

// envCoordinatorResumeMaxAge is how old a thread's last logged event may be for the coordinator
// to resume it, as a Go duration; "0" turns the limit off. Set with envCoordinatorResume.
const envCoordinatorResumeMaxAge = "KUBEMOOT_COORDINATOR_RESUME_MAX_AGE"

// coordinatorResumeEnvVars returns the resume env var when the realm
// (KubemootConfig spec.coordinator.resume) turns resume on, and nothing otherwise.
// An Agent's own spec.deployment.env is appended after it and wins.
func (r *AgentReconciler) coordinatorResumeEnvVars() []corev1.EnvVar {
	if r.ConfigCache == nil || !r.ConfigCache.GetCoordinatorResume() {
		return nil
	}
	env := []corev1.EnvVar{{Name: envCoordinatorResume, Value: valueTrue}}
	if maxAge := r.ConfigCache.GetCoordinatorResumeMaxAge(); maxAge != "" {
		env = append(env, corev1.EnvVar{Name: envCoordinatorResumeMaxAge, Value: maxAge})
	}
	return env
}

// coordinatorEventLogEnvVars returns the decision-log env var when the realm
// (KubemootConfig spec.coordinator.eventLog) turns the log on, and nothing otherwise.
// An Agent's own spec.deployment.env is appended after it and wins.
func (r *AgentReconciler) coordinatorEventLogEnvVars() []corev1.EnvVar {
	if r.ConfigCache == nil || !r.ConfigCache.GetCoordinatorEventLog() {
		return nil
	}
	return []corev1.EnvVar{{Name: envCoordinatorEventLog, Value: valueTrue}}
}
