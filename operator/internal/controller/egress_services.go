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

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/netpol"
)

// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch

// componentPlacementService is the component label of the chart's placement service.
const componentPlacementService = "placement-service"

// resolvedService is the outcome of reading one Service for an egress rule: the
// destinations, or the reason it cannot become a rule. A transport error is not an
// outcome; it is returned as an error so the reconcile retries.
type resolvedService struct {
	Dests  []netpol.Dest
	Reason string
}

// resolveService reads the Service and turns it into destinations on the given Service
// ports (every TCP port when ports is empty).
func (s *NetPolicySync) resolveService(ctx context.Context, namespace, name string, ports []int32) (resolvedService, error) {
	svc := &corev1.Service{}
	if err := s.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, svc); err != nil {
		if apierrors.IsNotFound(err) {
			return resolvedService{Reason: fmt.Sprintf("service %s/%s does not exist", namespace, name)}, nil
		}
		return resolvedService{}, err
	}
	slices := &discoveryv1.EndpointSliceList{}
	if err := s.Client.List(ctx, slices, client.InNamespace(namespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: name}); err != nil {
		return resolvedService{}, err
	}
	dests, err := netpol.ServiceDests(svc, slices.Items, ports)
	if err != nil {
		return resolvedService{Reason: err.Error()}, nil
	}
	return resolvedService{Dests: dests}, nil
}

// attachDeclaredDestinations resolves what spec.egress names in the cluster: the message
// bus when bus is set, and each allowed Service. A Service that cannot be resolved gets no
// rule and is listed in the workload's Unresolved, which the EgressEnforced condition
// reports.
func (s *NetPolicySync) attachDeclaredDestinations(ctx context.Context, w *netpol.Workload) error {
	if w.Rules == nil {
		return nil
	}
	if w.Rules.Bus {
		w.Defaults = append(w.Defaults, netpol.DestsFromURL(agentNATSURL())...)
	}
	for _, svc := range w.Rules.AllowedServices {
		key := egressServiceKey(svc, w.Namespace)
		res, err := s.resolveService(ctx, key.Namespace, key.Name, svc.Ports)
		if err != nil {
			return err
		}
		if res.Reason != "" {
			w.Unresolved = append(w.Unresolved, res.Reason)
			continue
		}
		w.Services = append(w.Services, res.Dests...)
	}
	return nil
}

// serviceURLDests resolves the Service a URL names on the URL's port. A URL that does not
// name a cluster Service gives the destinations derived from the URL alone. With nothing
// resolvable it returns no destinations and, when the URL named a Service, the reason.
func (s *NetPolicySync) serviceURLDests(ctx context.Context, raw, namespace string) ([]netpol.Dest, string, error) {
	return s.resolveURL(ctx, raw, namespace, true)
}

// serviceURLDestsAnyPort is serviceURLDests for a Service whose port the operator itself
// chose, so the Service is trusted and every TCP port it lists is allowed.
func (s *NetPolicySync) serviceURLDestsAnyPort(ctx context.Context, raw, namespace string) ([]netpol.Dest, string, error) {
	return s.resolveURL(ctx, raw, namespace, false)
}

func (s *NetPolicySync) resolveURL(ctx context.Context, raw, namespace string, urlPort bool) ([]netpol.Dest, string, error) {
	ref, ok := netpol.ServiceFromURL(raw, namespace)
	if !ok {
		return netpol.DestsFromURL(raw), "", nil
	}
	var ports []int32
	if urlPort {
		ports = []int32{ref.Port}
	}
	res, err := s.resolveService(ctx, ref.Namespace, ref.Name, ports)
	return res.Dests, res.Reason, err
}

// egressServiceKey is the Service an entry names; an empty namespace is the workload's own.
func egressServiceKey(svc kubemootv1alpha1.EgressService, workloadNamespace string) types.NamespacedName {
	ns := svc.Namespace
	if ns == "" {
		ns = workloadNamespace
	}
	return types.NamespacedName{Namespace: ns, Name: svc.Name}
}

// servicesReferencing lists the Services an egress block names, as namespaced names, for
// the workload in namespace.
func servicesReferencing(rules *kubemootv1alpha1.EgressRules, namespace string) []types.NamespacedName {
	if rules == nil {
		return nil
	}
	out := make([]types.NamespacedName, 0, len(rules.AllowedServices))
	for _, svc := range rules.AllowedServices {
		out = append(out, egressServiceKey(svc, namespace))
	}
	return out
}

// requestsWhere is the reconcile requests of the items the predicate accepts.
func requestsWhere[T client.Object](items []T, match func(T) bool) []reconcile.Request {
	var out []reconcile.Request
	for _, item := range items {
		if match(item) {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(item)})
		}
	}
	return out
}

// enqueueMCPServersForService re-reconciles the MCPServers whose egress names the Service,
// so a Service that appears or changes updates the rule that depends on it. An MCPServer
// depends on a Service only by naming it.
func enqueueMCPServersForService(c client.Client) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return mcpServersForService(ctx, c, obj)
	})
}

func mcpServersForService(ctx context.Context, c client.Client, svc client.Object) []reconcile.Request {
	servers := &kubemootv1alpha1.MCPServerList{}
	if err := c.List(ctx, servers); err != nil {
		return nil
	}
	return requestsWhere(pointersTo(servers.Items), func(s *kubemootv1alpha1.MCPServer) bool {
		return namesService(servicesReferencing(s.Spec.Egress, s.Namespace), svc)
	})
}

// enqueueAgentsForService re-reconciles the Agents with egress that depend on the Service:
// those naming it, those beside a query service (in its namespace), and every one when it is
// the placement service.
func enqueueAgentsForService(c client.Client) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return agentsForService(ctx, c, obj)
	})
}

func agentsForService(ctx context.Context, c client.Client, svc client.Object) []reconcile.Request {
	agents := &kubemootv1alpha1.AgentList{}
	if err := c.List(ctx, agents); err != nil {
		return nil
	}
	component := svc.GetLabels()[labelComponent]
	return requestsWhere(pointersTo(agents.Items), func(a *kubemootv1alpha1.Agent) bool {
		if a.Spec.Egress == nil {
			return false
		}
		return component == componentPlacementService ||
			(component == componentQueryService && a.Namespace == svc.GetNamespace()) ||
			namesService(servicesReferencing(a.Spec.Egress, a.Namespace), svc)
	})
}

func pointersTo[T any](items []T) []*T {
	out := make([]*T, len(items))
	for i := range items {
		out[i] = &items[i]
	}
	return out
}

// namesService reports whether the Service is one of the referenced ones.
func namesService(refs []types.NamespacedName, svc client.Object) bool {
	for _, ref := range refs {
		if ref.Namespace == svc.GetNamespace() && ref.Name == svc.GetName() {
			return true
		}
	}
	return false
}
