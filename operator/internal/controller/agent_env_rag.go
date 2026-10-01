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
	"sort"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// defaultQueryServicePort is the RAGSource query service port when spec.queryService.port is unset.
const defaultQueryServicePort int32 = 8000

// ragSourceEnvVars wires Agent.spec.ragSources into the runtime's kubemoot.rag-sources
// list, one indexed entry per reference, highest priority first and otherwise in the
// agent's order. A RAGSource whose query service is switched off has no endpoint to
// query and is left out.
func (r *AgentReconciler) ragSourceEnvVars(ctx context.Context, agent *kubemootv1alpha1.Agent) []corev1.EnvVar {
	refs := byPriority(agent.Spec.RAGSources)
	env := make([]corev1.EnvVar, 0, 3*len(refs))
	index := 0
	for _, ref := range refs {
		endpoint, ok := r.ragQueryEndpointFor(ctx, ref.Name, agent.Namespace)
		if !ok {
			continue
		}
		env = append(env, ragSourceEnv(index, ref, endpoint)...)
		index++
	}
	return env
}

// byPriority returns the references sorted by descending priority, keeping the
// declared order among equals.
func byPriority(refs []kubemootv1alpha1.RAGSourceRef) []kubemootv1alpha1.RAGSourceRef {
	sorted := append([]kubemootv1alpha1.RAGSourceRef(nil), refs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Priority > sorted[j].Priority })
	return sorted
}

// ragSourceEnv renders one kubemoot.rag-sources[i] entry. SmallRye maps an indexed list
// from env only as KUBEMOOT_RAG_SOURCES_<i>__<FIELD>, with a double underscore after the
// index; the single-underscore form maps to nothing. TOP_K is emitted only when positive:
// the runtime skips a source whose topK is 0 and defaults an absent one.
func ragSourceEnv(i int, ref kubemootv1alpha1.RAGSourceRef, endpoint string) []corev1.EnvVar {
	prefix := fmt.Sprintf("KUBEMOOT_RAG_SOURCES_%d__", i)
	env := []corev1.EnvVar{
		{Name: prefix + "NAME", Value: ref.Name},
		{Name: prefix + "ENDPOINT", Value: endpoint},
	}
	if ref.TopK > 0 {
		env = append(env, corev1.EnvVar{Name: prefix + "TOP_K", Value: fmt.Sprintf("%d", ref.TopK)})
	}
	return env
}

// ragQueryEndpointFor is the query endpoint the RAGSource reports, or the one its
// controller will create when the RAGSource is not yet reconciled or not yet applied;
// the RAGSource watch re-renders the agent when the reported endpoint differs. It
// reports false when the RAGSource's query service is switched off.
func (r *AgentReconciler) ragQueryEndpointFor(ctx context.Context, name, namespace string) (string, bool) {
	ragSource := &kubemootv1alpha1.RAGSource{}
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, ragSource); err != nil {
		return ragQueryEndpoint(name, namespace, defaultQueryServicePort), true
	}
	if !queryServiceEnabled(ragSource) {
		return "", false
	}
	if ragSource.Status.QueryEndpoint != "" {
		return ragSource.Status.QueryEndpoint, true
	}
	return ragQueryEndpoint(name, namespace, queryServicePort(ragSource)), true
}

// queryServiceEnabled reports whether the RAGSource runs a query service; it does unless switched off.
func queryServiceEnabled(ragSource *kubemootv1alpha1.RAGSource) bool {
	qs := ragSource.Spec.QueryService
	return qs == nil || kubemootv1alpha1.BoolOrTrue(qs.Enabled)
}

// ragQueryEndpoint is the in-cluster URL of a RAGSource's query service.
func ragQueryEndpoint(name, namespace string, port int32) string {
	return fmt.Sprintf("http://%s-query.%s:%d", name, namespace, port)
}

// queryServicePort is the RAGSource's query service port, or the default.
func queryServicePort(ragSource *kubemootv1alpha1.RAGSource) int32 {
	if ragSource.Spec.QueryService != nil && ragSource.Spec.QueryService.Port > 0 {
		return ragSource.Spec.QueryService.Port
	}
	return defaultQueryServicePort
}
