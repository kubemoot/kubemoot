/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// agent_resume_propagation.go keeps a crew's resumes current when its specialists
// change. Resumes are compiled during the coordinator's reconcile (resume_sync.go),
// so an agent added to, edited in, or removed from a running crew re-reconciles the
// crew's coordinator; resume sync is hash-gated, so an unchanged crew costs nothing.
// Without this, a specialist added to a live crew stays invisible to selection until
// something touches the coordinator.
package controller

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

// enqueueCoordinatorOnSpecialistChange returns an event handler that enqueues the
// coordinator of the crew a changed specialist belongs to.
func enqueueCoordinatorOnSpecialistChange(cli client.Client) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return mapSpecialistToCoordinatorRequests(ctx, cli, obj)
	})
}

// specialistResumeChanged passes creates, deletes, and updates to an agent's spec
// (a generation bump) or annotations (the triage-summary annotation); status-only
// updates, which every reconcile writes, do not re-reconcile the coordinator.
func specialistResumeChanged() predicate.Predicate {
	return predicate.Or(predicate.GenerationChangedPredicate{}, predicate.AnnotationChangedPredicate{})
}

// mapSpecialistToCoordinatorRequests returns reconcile requests for the coordinators
// in the specialist's namespace and crew. Coordinators and non-Agents map to nothing.
// On a delete, controller-runtime passes the last-known object, so its crew label
// still names the coordinator to update.
func mapSpecialistToCoordinatorRequests(ctx context.Context, cli client.Client, obj client.Object) []reconcile.Request {
	agent, ok := obj.(*kubemootv1alpha1.Agent)
	if !ok || agent.Spec.DiscussRole == roleCoordinator {
		return nil
	}
	crew := agent.Labels[crewLabelKey]
	if crew == "" {
		return nil
	}
	agents := &kubemootv1alpha1.AgentList{}
	if err := cli.List(ctx, agents, client.InNamespace(agent.Namespace), client.MatchingLabels{crewLabelKey: crew}); err != nil {
		logf.FromContext(ctx).Error(err, "resume propagation: list agents failed", "agent", agent.Name)
		return nil
	}
	reqs := make([]reconcile.Request, 0, 1)
	for i := range agents.Items {
		if agents.Items[i].Spec.DiscussRole == roleCoordinator {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&agents.Items[i])})
		}
	}
	return reqs
}
