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

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	meshcontract "github.com/kubemoot/kubemoot/operator/pkg/mesh"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The env vars that carry the model choice into a legacy-runtime pod.
const (
	envModelModel     = "KUBEMOOT_MODEL_MODEL"
	envModelEndpoint  = "KUBEMOOT_MODEL_ENDPOINT"
	envTriageModel    = "KUBEMOOT_TRIAGE_MODEL_MODEL_ID"
	envTriageEndpoint = "KUBEMOOT_TRIAGE_MODEL_ENDPOINT"
	envOllamaBaseURL  = "QUARKUS_LANGCHAIN4J_OLLAMA_BASE_URL"
)

// modelBindingEnvVars are the model and endpoint env vars of the two phases, none for a phase
// with no pick.
func modelBindingEnvVars(mulling, triage *modelPick) []corev1.EnvVar {
	var env []corev1.EnvVar
	if mulling != nil {
		env = append(env,
			corev1.EnvVar{Name: envModelModel, Value: mulling.ModelID},
			corev1.EnvVar{Name: envModelEndpoint, Value: mulling.Endpoint})
	}
	if triage != nil {
		env = append(env,
			corev1.EnvVar{Name: envTriageModel, Value: triage.ModelID},
			corev1.EnvVar{Name: envTriageEndpoint, Value: triage.Endpoint})
	}
	return env
}

// heldPicks are the models an Agent's workload last ran with, for the reconciles in which no
// model is feasible: models are chosen per call, so the pods keep what they were bound to and
// everything else about the Agent still follows its spec. Both are nil when the Agent never ran
// with a model. A held pick carries what the build reads (ids, endpoints, candidates, the
// server's name), not a live ModelServer.
func (r *AgentReconciler) heldPicks(ctx context.Context, agent *kubemootv1alpha1.Agent) (mulling, triage *modelPick) {
	if usesSidecar(agent) {
		cm := &corev1.ConfigMap{}
		if err := r.Get(ctx, types.NamespacedName{Name: contractConfigMapName(agent), Namespace: agent.Namespace}, cm); err != nil {
			return nil, nil
		}
		return picksFromContract(cm.Data[meshcontract.ContractFile])
	}
	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(agent), dep); err != nil {
		return nil, nil
	}
	return picksFromDeployment(dep)
}

// withTriageFallback gives a missing triage pick the mulling one, as a fresh pick does.
func withTriageFallback(mulling, triage *modelPick) (*modelPick, *modelPick) {
	if triage == nil {
		triage = mulling
	}
	return mulling, triage
}

// picksFromDeployment reads the picks back from the model env vars and server labels the
// build wrote.
func picksFromDeployment(dep *appsv1.Deployment) (mulling, triage *modelPick) {
	env := map[string]string{}
	for _, c := range dep.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			// The first occurrence is the operator's; spec.deployment.env follows it.
			if _, seen := env[e.Name]; !seen {
				env[e.Name] = e.Value
			}
		}
	}
	mulling = heldPick(env[envModelModel], env[envModelEndpoint], dep.Labels[labelMullingServer],
		env[envModelCandidatesMulling], env[envModelPreferFasterMulling])
	triage = heldPick(env[envTriageModel], env[envTriageEndpoint], dep.Labels[labelTriageServer],
		env[envModelCandidatesTriage], env[envModelPreferFasterTriage])
	if mulling == nil {
		return nil, nil
	}
	return withTriageFallback(mulling, triage)
}

// heldPick rebuilds one phase's pick, nil when no model id was recorded.
func heldPick(modelID, endpoint, server, candidatesJSON, preferFaster string) *modelPick {
	if modelID == "" {
		return nil
	}
	pick := &modelPick{ModelID: modelID, Endpoint: endpoint}
	if server != "" {
		pick.Server = &kubemootv1alpha1.ModelServer{ObjectMeta: metav1.ObjectMeta{Name: server}}
	}
	if candidatesJSON != "" && json.Unmarshal([]byte(candidatesJSON), &pick.Candidates) != nil {
		return nil // unreadable: hold nothing rather than rebuild a different template
	}
	if n, err := strconv.ParseInt(preferFaster, 10, 32); err == nil && n > 0 {
		pick.PreferFaster = int32(n)
	}
	return pick
}

// picksFromContract reads the picks back from a sidecar Agent's contract.yaml: each alias's
// candidates and its fallback server.
func picksFromContract(data string) (mulling, triage *modelPick) {
	c, err := meshcontract.Parse([]byte(data))
	if err != nil {
		return nil, nil
	}
	mulling = pickFromAlias(c.Models.Aliases[meshcontract.DefaultAlias])
	triage = pickFromAlias(c.Models.Aliases[meshcontract.TriageAlias])
	return withTriageFallback(mulling, triage)
}

// pickFromAlias rebuilds a pick from a contract alias, nil when it names no model.
func pickFromAlias(a meshcontract.Alias) *modelPick {
	if len(a.Candidates) == 0 {
		return nil
	}
	pick := &modelPick{ModelID: a.Candidates[0]}
	for _, name := range a.Candidates {
		pick.Candidates = append(pick.Candidates, modelCandidate{Model: name})
	}
	if f := a.Fallback; f != nil {
		pick.ModelID, pick.Endpoint = f.Model, f.Endpoint
		pick.Server = &kubemootv1alpha1.ModelServer{ObjectMeta: metav1.ObjectMeta{Name: f.Server}}
	}
	return pick
}
