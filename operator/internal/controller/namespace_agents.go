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

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// enqueueNamespaceAgents returns an event handler that enqueues every Agent in
// the namespace of a changed T (a crew maps to a namespace). kind names T in the
// log lines.
func enqueueNamespaceAgents[T client.Object](cli client.Client, kind string) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return namespaceAgentRequests[T](ctx, cli, kind, obj)
	})
}

// namespaceAgentRequests returns reconcile requests for the Agents in the
// namespace of a changed T. Objects that are not a T -> nil.
func namespaceAgentRequests[T client.Object](ctx context.Context, cli client.Client, kind string, obj client.Object) []reconcile.Request {
	if _, ok := obj.(T); !ok {
		return nil
	}
	log := logf.FromContext(ctx)
	agents := &kubemootv1alpha1.AgentList{}
	if err := cli.List(ctx, agents, client.InNamespace(obj.GetNamespace())); err != nil {
		log.Error(err, kind+" propagation: list agents failed", "name", obj.GetName())
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(agents.Items))
	for i := range agents.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&agents.Items[i])})
	}
	if len(reqs) > 0 {
		log.Info(kind+" changed; enqueueing agent reconciles", "name", obj.GetName(), "count", len(reqs))
	}
	return reqs
}
