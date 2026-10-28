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

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/netpol"
)

// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch

// kubernetesServiceName is the Service, in the default namespace, that fronts the API server.
const kubernetesServiceName = "kubernetes"

// reconcileNetworkPolicies applies the ingress and egress policies of a managed MCPServer
// and records the EgressEnforced condition. It does nothing when the reconciler has no
// NetPolicySync.
func (r *MCPServerReconciler) reconcileNetworkPolicies(ctx context.Context, mcpServer *kubemootv1alpha1.MCPServer) error {
	if r.NetPol == nil {
		return nil
	}
	cfg := r.NetPol.Resolved()
	var objects []client.Object
	if shouldCreateMCPServerService(mcpServer) {
		server := netpol.MCPServer{
			PodName: boundedName(mcpServer.Name), Namespace: mcpServer.Namespace, Port: mcpServerServicePort(mcpServer),
		}
		if mcpServer.Namespace == cfg.OperatorNamespace {
			var err error
			if server.CrewNamespaces, err = crewNamespaces(ctx, r.Client); err != nil {
				return err
			}
		}
		objects = netpol.MCPServerIngress(cfg, server)
	}
	workload := netpol.Workload{
		Name: "mcp-" + boundedName(mcpServer.Name), Namespace: mcpServer.Namespace,
		Selector: mcpServerPodLabels(mcpServer), Rules: mcpServer.Spec.Egress,
	}
	if err := r.NetPol.attachAPIServer(ctx, cfg, &workload); err != nil {
		return err
	}
	if cfg.EgressOn() {
		if err := r.NetPol.attachDeclaredDestinations(ctx, &workload); err != nil {
			return err
		}
	}
	egress := netpol.Egress(cfg, workload)
	setEgressCondition(&mcpServer.Status.Conditions, mcpServer.Generation, egress.Status)
	return r.NetPol.Apply(ctx, mcpServer, mcpServer.Namespace, append(objects, egress.Objects...))
}

// attachAPIServer reads the API server's addresses into the workload when it asks for the
// API server and the cluster has no Cilium entity for it.
func (s *NetPolicySync) attachAPIServer(ctx context.Context, cfg netpol.Config, w *netpol.Workload) error {
	if w.Rules == nil || !w.Rules.APIServer || cfg.UseCilium || !cfg.EgressOn() {
		return nil
	}
	slices := &discoveryv1.EndpointSliceList{}
	err := s.Client.List(ctx, slices, client.InNamespace("default"),
		client.MatchingLabels{discoveryv1.LabelServiceName: kubernetesServiceName})
	if err != nil {
		return err
	}
	for i := range slices.Items {
		w.APIServer = append(w.APIServer, apiServerEndpoints(&slices.Items[i])...)
	}
	if len(w.APIServer) == 0 {
		return fmt.Errorf("egress.apiServer is set but the %s/%s Service lists no endpoints", metav1.NamespaceDefault, kubernetesServiceName)
	}
	return nil
}

// apiServerEndpoints are the addresses and ports one EndpointSlice of the kubernetes
// Service lists.
func apiServerEndpoints(slice *discoveryv1.EndpointSlice) []netpol.APIServerEndpoint {
	var out []netpol.APIServerEndpoint
	for _, ep := range slice.Endpoints {
		for _, addr := range ep.Addresses {
			for _, port := range slice.Ports {
				if port.Port == nil {
					continue
				}
				out = append(out, netpol.APIServerEndpoint{CIDR: netpol.AddrCIDR(addr), Port: *port.Port})
			}
		}
	}
	return out
}
