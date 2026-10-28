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
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// namespaceHoldsCrew reports whether the namespace holds at least one Crew.
func namespaceHoldsCrew(ctx context.Context, cli client.Reader, namespace string) (bool, error) {
	first, err := firstCrewIn(ctx, cli, namespace)
	return first != "", err
}

// firstCrewIn returns the name of the first Crew in the namespace by name, or "" when it
// holds none.
func firstCrewIn(ctx context.Context, cli client.Reader, namespace string) (string, error) {
	crews := &kubemootv1alpha1.CrewList{}
	if err := cli.List(ctx, crews, client.InNamespace(namespace)); err != nil {
		return "", err
	}
	first := ""
	for i := range crews.Items {
		if name := crews.Items[i].Name; first == "" || name < first {
			first = name
		}
	}
	return first, nil
}

// ensureCrewLabel puts the crew label on a namespace that holds a Crew (crewName is the
// first one) and has no label, and records that the operator set it. A namespace that has
// the label, is protected, or holds no Crew is left alone. The lock turns a concurrent write
// by another reconcile into a conflict, which requeues, so the first writer's value stands
// and the label never flaps.
func ensureCrewLabel(ctx context.Context, cli client.Client, ns *corev1.Namespace, crewName string) error {
	if crewName == "" || hasCrewLabel(ns) || isProtectedNamespace(ns.Name) {
		return nil
	}
	patch := client.MergeFromWithOptions(ns.DeepCopy(), client.MergeFromWithOptimisticLock{})
	markCrewLabel(ns, crewName)
	return cli.Patch(ctx, ns, patch)
}

// crewNamespaces lists, sorted, the namespaces that hold at least one Crew.
func crewNamespaces(ctx context.Context, cli client.Reader) ([]string, error) {
	crews := &kubemootv1alpha1.CrewList{}
	if err := cli.List(ctx, crews); err != nil {
		return nil, err
	}
	var out []string
	for i := range crews.Items {
		out = append(out, crews.Items[i].Namespace)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// crewLifecyclePredicate passes a Crew being created or deleted and nothing else: the set
// of namespaces that hold a Crew changes only then.
func crewLifecyclePredicate() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// crewNamespaceRequests is the reconcile request for the namespace of a Crew, so the
// namespace reconciler re-checks whether the namespace still holds a Crew.
func crewNamespaceRequests(crew client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: crew.GetNamespace()}}}
}

func enqueueCrewNamespace() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(_ context.Context, crew client.Object) []reconcile.Request {
		return crewNamespaceRequests(crew)
	})
}

// sharedMCPServerRequests are the MCPServers in the operator namespace: those shared tool
// servers admit the gateway of every namespace that holds a Crew, so a Crew created or
// deleted changes their policy.
func sharedMCPServerRequests(ctx context.Context, cli client.Reader) []reconcile.Request {
	servers := &kubemootv1alpha1.MCPServerList{}
	if err := cli.List(ctx, servers, client.InNamespace(operatorNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list shared MCPServers for a Crew change")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(servers.Items))
	for i := range servers.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&servers.Items[i])})
	}
	return reqs
}

func enqueueSharedMCPServers(cli client.Reader) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		return sharedMCPServerRequests(ctx, cli)
	})
}
