/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// model_propagation.go wires Model changes to the agents in the same namespace.
// An agent's per-phase model binding and its candidate list are derived from the
// Models it can see; without a watch, adding, removing, or relabeling Models (or a
// new Model finishing its pull) left every agent on its old binding until an
// unrelated change or the informer resync.
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

// enqueueAgentsOnModelChange enqueues every Agent in a changed Model's namespace.
func enqueueAgentsOnModelChange(cli client.Client) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return mapModelToAgentRequests(ctx, cli, obj)
	})
}

// modelBindingChanged passes the Model events that can change a binding: creation,
// deletion (including the start of a finalizer-held deletion), and updates to the
// labels the scheduler selects on, the spec, or whether the model is usable.
func modelBindingChanged() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldM, okOld := e.ObjectOld.(*kubemootv1alpha1.Model)
			newM, okNew := e.ObjectNew.(*kubemootv1alpha1.Model)
			if !okOld || !okNew {
				return false
			}
			return !reflect.DeepEqual(oldM.Labels, newM.Labels) ||
				!reflect.DeepEqual(oldM.Spec, newM.Spec) ||
				oldM.Status.Ready != newM.Status.Ready ||
				oldM.Status.State != newM.Status.State ||
				oldM.DeletionTimestamp.IsZero() != newM.DeletionTimestamp.IsZero()
		},
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// mapModelToAgentRequests returns reconcile requests for the Agents in the Model's
// namespace. Non-Model objects -> nil.
func mapModelToAgentRequests(ctx context.Context, cli client.Client, obj client.Object) []reconcile.Request {
	m, ok := obj.(*kubemootv1alpha1.Model)
	if !ok {
		return nil
	}
	log := logf.FromContext(ctx)
	agents := &kubemootv1alpha1.AgentList{}
	if err := cli.List(ctx, agents, client.InNamespace(m.Namespace)); err != nil {
		log.Error(err, "Model propagation: list agents failed", "model", m.Name)
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(agents.Items))
	for i := range agents.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&agents.Items[i])})
	}
	if len(reqs) > 0 {
		log.Info("Model changed; enqueueing agent reconciles", "model", m.Name, "count", len(reqs))
	}
	return reqs
}
