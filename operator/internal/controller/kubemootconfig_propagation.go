/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package controller — KubemootConfig propagation helper.
//
// Closes the [[Operator Does Not Propagate KubemootConfig Image Bumps]] gap.
// KubemootConfig is the cluster-scoped singleton holding image references for
// every kubemoot component (agentRuntime, mcpBridge, mcpGateway, indexer,
// queryService, discussionGateway, doclingServe, fitnessRunner). When the
// chart bumps an image, KubemootConfig is updated by Flux; the
// KubemootConfigReconciler refreshes the in-memory ConfigCache; but
// previously NOTHING triggered downstream controllers to re-reconcile
// their owned Deployments/Jobs against the new image. Agents could sit
// six image versions behind without any signal.
//
// This file provides the small piece each consuming controller plugs into
// its SetupWithManager:
//
//	.Watches(
//	    &kubemootv1alpha1.KubemootConfig{},
//	    enqueueAllOnKubemootConfigChange(mgr.GetClient(),
//	        func() client.ObjectList { return &kubemootv1alpha1.AgentList{} },
//	        "agent"),
//	)
//
// When the KubemootConfig singleton fires a create/update event, every
// CR returned by the list factory gets enqueued for reconciliation. The
// reconciler then reads the fresh image from ConfigCache and updates the
// Deployment/Job spec — no per-CR annotation, no operator restart needed.
package controller

import (
	"context"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// mapKubemootConfigToRequests is the pure mapping logic for
// {@link enqueueAllOnKubemootConfigChange}. Extracted as a named function
// so unit tests can exercise it directly without spinning up a
// controller-runtime queue or fake event source.
//
// Filters to the singleton (DefaultKubemootConfigName) so a stray
// non-singleton KubemootConfig can't accidentally trigger a fleet-wide
// reconcile storm. Returns nil on list failure — propagation is
// best-effort by design, with the manual annotation fallback still
// available if needed.
func mapKubemootConfigToRequests(
	ctx context.Context,
	cli client.Client,
	listFactory func() client.ObjectList,
	controllerName string,
	obj client.Object,
) []reconcile.Request {
	cfg, ok := obj.(*kubemootv1alpha1.KubemootConfig)
	if !ok || cfg.Name != DefaultKubemootConfigName {
		return nil
	}
	log := logf.FromContext(ctx)
	list := listFactory()
	if err := cli.List(ctx, list); err != nil {
		log.Error(err, "KubemootConfig propagation: list failed", "controller", controllerName)
		return nil
	}
	items, err := apimeta.ExtractList(list)
	if err != nil {
		log.Error(err, "KubemootConfig propagation: ExtractList failed", "controller", controllerName)
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(items))
	for _, o := range items {
		co, ok := o.(client.Object)
		if !ok {
			continue
		}
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(co)})
	}
	log.Info("KubemootConfig changed; enqueueing reconciles",
		"controller", controllerName, "count", len(reqs))
	return reqs
}

// enqueueAllOnKubemootConfigChange returns a handler.EventHandler that
// wraps {@link mapKubemootConfigToRequests} for use in a controller's
// SetupWithManager Watches() chain.
func enqueueAllOnKubemootConfigChange(
	cli client.Client,
	listFactory func() client.ObjectList,
	controllerName string,
) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return mapKubemootConfigToRequests(ctx, cli, listFactory, controllerName, obj)
	})
}
