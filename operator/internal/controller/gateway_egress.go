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

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/netpol"
)

// reconcileEgress applies the gateway's egress policy and records the EgressEnforced
// condition. It does nothing when the reconciler has no NetPolicySync.
func (r *MCPGatewayReconciler) reconcileEgress(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway) error {
	if r.NetPol == nil {
		return nil
	}
	cfg := r.NetPol.Resolved()
	workload := netpol.Workload{
		Name: "gateway-" + boundedName(gateway.Name), Namespace: gateway.Namespace,
		Selector: gatewayLabels(gateway),
	}
	if cfg.EgressOn() {
		servers, err := r.selectedServers(ctx, gateway)
		if err != nil {
			return err
		}
		if err := r.NetPol.attachGatewayDestinations(ctx, gateway.Namespace, servers, &workload); err != nil {
			return err
		}
	}
	egress := netpol.Egress(cfg, workload)
	setEgressCondition(&gateway.Status.Conditions, gateway.Generation, egress.Status)
	return r.NetPol.Apply(ctx, gateway, gateway.Namespace, egress.Objects)
}

// attachGatewayDestinations derives a gateway's egress from the servers it fronts. A
// gateway that fronts an external server (spec.externalEndpoint) makes that call itself, so
// it is restricted to DNS, the host and port of each external endpoint, and the pods of the
// in-cluster servers it fronts. A gateway that fronts no external server declares nothing
// and is not restricted. An endpoint that is not a usable http or https URL gets no rule
// and is listed in the workload's Invalid.
func (s *NetPolicySync) attachGatewayDestinations(ctx context.Context, namespace string, servers []kubemootv1alpha1.MCPServer, w *netpol.Workload) error {
	if !frontsExternalServer(servers) {
		return nil
	}
	w.Rules = &kubemootv1alpha1.EgressRules{}
	for i := range servers {
		mcp := &servers[i]
		if mcp.Spec.ExternalEndpoint == "" {
			w.Defaults = append(w.Defaults, inClusterServerDests(namespace, mcp)...)
			continue
		}
		dests, reason, err := s.externalEndpointDests(ctx, mcp, namespace)
		if err != nil {
			return err
		}
		if reason != "" {
			w.Invalid = append(w.Invalid, reason)
		}
		w.Defaults = append(w.Defaults, dests...)
	}
	return nil
}

func frontsExternalServer(servers []kubemootv1alpha1.MCPServer) bool {
	for i := range servers {
		if servers[i].Spec.ExternalEndpoint != "" {
			return true
		}
	}
	return false
}

// inClusterServerDests are the pods and port the gateway calls on a server that runs in the
// cluster, the same pods and port the server's own ingress policy admits the gateway to.
func inClusterServerDests(gatewayNamespace string, mcp *kubemootv1alpha1.MCPServer) []netpol.Dest {
	if !shouldCreateMCPServerService(mcp) {
		return nil
	}
	d := netpol.Dest{PodLabels: mcpServerPodLabels(mcp), Port: mcpServerServicePort(mcp)}
	if mcp.Namespace == gatewayNamespace {
		d.SameNamespace = true
	} else {
		d.Namespace = mcp.Namespace
	}
	return []netpol.Dest{d}
}

// externalEndpointDests turns an external server's endpoint into the destinations the
// gateway may call: its host and port, or the Service it names. The reason is set, and no
// destination returned, when the endpoint cannot become a rule.
func (s *NetPolicySync) externalEndpointDests(ctx context.Context, mcp *kubemootv1alpha1.MCPServer, namespace string) ([]netpol.Dest, string, error) {
	raw := mcp.Spec.ExternalEndpoint
	if err := netpol.CheckHTTPEndpoint(raw); err != nil {
		return nil, fmt.Sprintf("MCPServer %s/%s externalEndpoint: %v", mcp.Namespace, mcp.Name, err), nil
	}
	dests, reason, err := s.serviceURLDests(ctx, raw, namespace)
	if err != nil {
		return nil, "", err
	}
	if reason != "" {
		reason = fmt.Sprintf("MCPServer %s/%s externalEndpoint: %s", mcp.Namespace, mcp.Name, reason)
	}
	return dests, reason, nil
}
