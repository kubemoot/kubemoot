/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// csp_propagation.go wires CrewSchedulingPolicy changes to the agents they
// govern. Each agent's per-phase model binding (mulling / triage / synthesis)
// is derived during Agent reconcile from the crew's CrewSchedulingPolicy
// (agent_controller.findPolicyAndRule). Without a watch, editing a CSP rule —
// e.g. adding a `prefer` block to pin triage to latencyClass:low — would not
// take effect until the next Agent change or the (multi-hour) informer resync.
// This handler re-reconciles the crew's agents the moment the CSP changes.
package controller

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// enqueueAgentsOnCSPChange returns an event handler that enqueues every Agent
// governed by a changed CrewSchedulingPolicy (same namespace, matching crewRef;
// empty crewRef matches all). CSP edits are rare, so listing per event is cheap.
func enqueueAgentsOnCSPChange(cli client.Client) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return mapCSPToAgentRequests(ctx, cli, obj)
	})
}

// mapCSPToAgentRequests lists the Agents a CrewSchedulingPolicy governs and
// returns reconcile requests for them. Non-CSP objects → nil. crewRef ""
// matches all agents in the CSP's namespace.
func mapCSPToAgentRequests(ctx context.Context, cli client.Client, obj client.Object) []reconcile.Request {
	csp, ok := obj.(*kubemootv1alpha1.CrewSchedulingPolicy)
	if !ok {
		return nil
	}
	log := logf.FromContext(ctx)
	agents := &kubemootv1alpha1.AgentList{}
	if err := cli.List(ctx, agents, client.InNamespace(csp.Namespace)); err != nil {
		log.Error(err, "CSP propagation: list agents failed", "csp", csp.Name)
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(agents.Items))
	for i := range agents.Items {
		a := &agents.Items[i]
		if csp.Spec.CrewRef != "" && a.Labels["kubemoot.ai/crew"] != csp.Spec.CrewRef {
			continue
		}
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(a)})
	}
	if len(reqs) > 0 {
		log.Info("CrewSchedulingPolicy changed; enqueueing agent reconciles",
			"csp", csp.Name, "crew", csp.Spec.CrewRef, "count", len(reqs))
	}
	return reqs
}
