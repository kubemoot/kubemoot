/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// promptmodule_propagation.go wires PromptModule changes to the agents that
// reference them. Each agent's system.txt is assembled during Agent reconcile
// from the PromptModules listed in Agent.spec.promptRefs
// (agent_controller.assembleSystemPrompt). Without a watch, editing a
// PromptModule -- the canonical way agent behavior is defined in this system --
// would not re-render system.txt or roll the agent until the next unrelated
// Agent change or the (multi-hour) informer resync, so prompt edits looked like
// no-ops. This handler re-reconciles every Agent that references the changed
// PromptModule the moment it changes. See "Operator Does Not Propagate
// PromptModule Changes to Agents".
package controller

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// enqueueAgentsOnPromptModuleChange returns an event handler that enqueues every
// Agent referencing a changed PromptModule (same namespace, name in promptRefs).
// PromptModule edits are rare, so listing per event is cheap.
func enqueueAgentsOnPromptModuleChange(cli client.Client) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return mapPromptModuleToAgentRequests(ctx, cli, obj)
	})
}

// mapPromptModuleToAgentRequests lists the Agents in the PromptModule's
// namespace whose spec.promptRefs include it and returns reconcile requests for
// them. Non-PromptModule objects -> nil.
func mapPromptModuleToAgentRequests(ctx context.Context, cli client.Client, obj client.Object) []reconcile.Request {
	pm, ok := obj.(*kubemootv1alpha1.PromptModule)
	if !ok {
		return nil
	}
	log := logf.FromContext(ctx)
	agents := &kubemootv1alpha1.AgentList{}
	if err := cli.List(ctx, agents, client.InNamespace(pm.Namespace)); err != nil {
		log.Error(err, "PromptModule propagation: list agents failed", "promptModule", pm.Name)
		return nil
	}
	reqs := make([]reconcile.Request, 0)
	for i := range agents.Items {
		a := &agents.Items[i]
		if !referencesPromptModule(a, pm.Name) {
			continue
		}
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(a)})
	}
	if len(reqs) > 0 {
		log.Info("PromptModule changed; enqueueing agent reconciles",
			"promptModule", pm.Name, "count", len(reqs))
	}
	return reqs
}

// referencesPromptModule reports whether the agent lists the named PromptModule
// in spec.prompt.promptRefs.
func referencesPromptModule(agent *kubemootv1alpha1.Agent, name string) bool {
	for _, ref := range agent.Spec.PromptRefs {
		if ref == name {
			return true
		}
	}
	return false
}
