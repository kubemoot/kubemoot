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
	"encoding/json"
	"strconv"

	corev1 "k8s.io/api/core/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	envDiscussSignals    = "KUBEMOOT_DISCUSS_SIGNALS"
	envRecheckMaxRounds  = "KUBEMOOT_DISCUSS_RECHECK_MAX_ROUNDS"
	signalFieldName      = "name"
	signalFieldMeaning   = "meaning"
	signalFieldTerminal  = "terminal"
	signalFieldAddressed = "addressed"
)

// agentSignalEnvVars gives every agent of a crew the kinds of signal its archetype
// declares, as KUBEMOOT_DISCUSS_SIGNALS (a JSON list). The runtime builds the signal
// tool from it. Nothing is set when the archetype is missing or declares no signal
// with a meaning, and the agent then records its verdict as before the tool existed.
func (r *AgentReconciler) agentSignalEnvVars(ctx context.Context, agent *kubemootv1alpha1.Agent) []corev1.EnvVar {
	archetype := r.crewArchetype(ctx, agent)
	if archetype == nil {
		return nil
	}
	return signalEnvVars(archetype.Spec.Signals)
}

// coordinatorRecheckEnvVars tells the coordinator how many clarifying-question rounds a
// thread may have, as its crew's archetype declares. Nothing is set when the archetype
// is missing or declares no recheck block, and the runtime then allows one round.
func (r *AgentReconciler) coordinatorRecheckEnvVars(ctx context.Context, agent *kubemootv1alpha1.Agent) []corev1.EnvVar {
	archetype := r.crewArchetype(ctx, agent)
	if archetype == nil {
		return nil
	}
	return recheckEnvVars(archetype.Spec.Recheck)
}

// signalEnvVars is the environment for an archetype's signals: the declared kinds that
// carry a meaning, in declaration order. A kind stored in the earlier bare-name form has
// no meaning and is left out, since a model cannot choose a kind it is not told about.
func signalEnvVars(signals []kubemootv1alpha1.ArchetypeSignal) []corev1.EnvVar {
	declared := make([]map[string]any, 0, len(signals))
	for _, s := range signals {
		if s.Name == "" || s.Meaning == "" {
			continue
		}
		declared = append(declared, map[string]any{
			signalFieldName:      s.Name,
			signalFieldMeaning:   s.Meaning,
			signalFieldTerminal:  s.IsTerminal(),
			signalFieldAddressed: s.Addressed,
		})
	}
	if len(declared) == 0 {
		return nil
	}
	value, err := json.Marshal(declared)
	if err != nil {
		return nil
	}
	return []corev1.EnvVar{{Name: envDiscussSignals, Value: string(value)}}
}

// recheckEnvVars is the coordinator environment for an archetype's recheck block.
func recheckEnvVars(recheck *kubemootv1alpha1.ArchetypeRecheck) []corev1.EnvVar {
	if recheck == nil || recheck.MaxRounds == nil {
		return nil
	}
	return []corev1.EnvVar{{Name: envRecheckMaxRounds, Value: strconv.Itoa(int(*recheck.MaxRounds))}}
}
