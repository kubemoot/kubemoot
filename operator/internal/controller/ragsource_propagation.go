/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// ragsource_propagation.go wires RAGSource changes to the agents that reference
// them. An agent's KUBEMOOT_RAG_SOURCES_* env carries each referenced RAGSource's
// query endpoint (agent_env_rag.go), so when that endpoint appears, moves, or the
// query service is switched off, the agents naming the RAGSource re-render their
// Deployment. Only changes to what the env is built from pass the predicate; the
// RAGSource controller's frequent status updates while indexing do not.
package controller

import (
	"context"
	"reflect"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

// enqueueAgentsOnRAGSourceChange returns an event handler that enqueues every Agent
// referencing a changed RAGSource (same namespace, name in spec.ragSources).
func enqueueAgentsOnRAGSourceChange(cli client.Client) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return mapRAGSourceToAgentRequests(ctx, cli, obj)
	})
}

// ragSourceEndpointChanged passes creates and deletes, and updates that change the
// query endpoint or the query service spec.
func ragSourceEndpointChanged() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldRS, okOld := e.ObjectOld.(*kubemootv1alpha1.RAGSource)
			newRS, okNew := e.ObjectNew.(*kubemootv1alpha1.RAGSource)
			if !okOld || !okNew {
				return false
			}
			return oldRS.Status.QueryEndpoint != newRS.Status.QueryEndpoint ||
				!reflect.DeepEqual(oldRS.Spec.QueryService, newRS.Spec.QueryService)
		},
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// mapRAGSourceToAgentRequests lists the Agents in the RAGSource's namespace whose
// spec.ragSources name it and returns reconcile requests for them.
// Non-RAGSource objects -> nil.
func mapRAGSourceToAgentRequests(ctx context.Context, cli client.Client, obj client.Object) []reconcile.Request {
	rs, ok := obj.(*kubemootv1alpha1.RAGSource)
	if !ok {
		return nil
	}
	log := logf.FromContext(ctx)
	agents := &kubemootv1alpha1.AgentList{}
	if err := cli.List(ctx, agents, client.InNamespace(rs.Namespace)); err != nil {
		log.Error(err, "RAGSource propagation: list agents failed", "ragSource", rs.Name)
		return nil
	}
	reqs := make([]reconcile.Request, 0)
	for i := range agents.Items {
		a := &agents.Items[i]
		if referencesRAGSource(a, rs.Name) {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(a)})
		}
	}
	if len(reqs) > 0 {
		log.Info("RAGSource changed; enqueueing agent reconciles", "ragSource", rs.Name, "count", len(reqs))
	}
	return reqs
}

// referencesRAGSource reports whether the agent lists the named RAGSource in spec.ragSources.
func referencesRAGSource(agent *kubemootv1alpha1.Agent, name string) bool {
	for _, ref := range agent.Spec.RAGSources {
		if ref.Name == name {
			return true
		}
	}
	return false
}
