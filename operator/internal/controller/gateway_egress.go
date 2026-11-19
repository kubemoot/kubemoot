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
	"net/url"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/netpol"
)

// reconcileGatewayNetwork creates the admin token and the ingress policy, then the egress
// policy, before the gateway that reads them starts.
func (r *MCPGatewayReconciler) reconcileGatewayNetwork(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway) error {
	if err := r.reconcileAdminAccess(ctx, gateway); err != nil {
		return fmt.Errorf("admin access: %w", err)
	}
	if err := r.reconcileEgress(ctx, gateway); err != nil {
		return fmt.Errorf("egress policy: %w", err)
	}
	return nil
}

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
		if err := r.NetPol.attachGatewayDestinations(ctx, gateway, servers, &workload); err != nil {
			return err
		}
	}
	egress := netpol.Egress(cfg, workload)
	setEgressCondition(&gateway.Status.Conditions, gateway.Generation, egress.Status)
	return r.NetPol.Apply(ctx, gateway, gateway.Namespace, egress.Objects)
}

// attachGatewayDestinations derives a gateway's egress from the servers it fronts. A
// gateway that fronts an external server (spec.externalEndpoint) makes that call itself, so
// it is restricted to DNS, the host and port of each external endpoint, the pods of the
// in-cluster servers it fronts, and its other calls (ContextForge database and Redis, the
// tool index query service). A gateway that fronts no external server declares nothing
// and is not restricted. An endpoint that is not a usable http or https URL gets no rule
// and is listed in the workload's Invalid.
func (s *NetPolicySync) attachGatewayDestinations(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway, servers []kubemootv1alpha1.MCPServer, w *netpol.Workload) error {
	if !frontsExternalServer(servers) {
		return nil
	}
	namespace := gateway.Namespace
	w.Rules = &kubemootv1alpha1.EgressRules{}
	other, invalidOther, err := s.gatewayOtherDests(ctx, gateway)
	if err != nil {
		return err
	}
	w.Defaults = append(w.Defaults, other...)
	w.Invalid = append(w.Invalid, invalidOther...)
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
	where := fmt.Sprintf("MCPServer %s/%s externalEndpoint: ", mcp.Namespace, mcp.Name)
	ep, err := netpol.ParseExternalEndpoint(mcp.Spec.ExternalEndpoint, namespace)
	return s.endpointDests(ctx, where, ep, err, []int32{ep.Port()})
}

// endpointDests turns a parsed endpoint into destinations: its host or address, or the
// Service it names read on the given Service ports (every TCP port when ports is nil).
// A parse error, or a Service that cannot be resolved, comes back as a reason prefixed
// with where, and no destination.
func (s *NetPolicySync) endpointDests(ctx context.Context, where string, ep netpol.ExternalEndpoint, parseErr error, ports []int32) ([]netpol.Dest, string, error) {
	if parseErr != nil {
		return nil, where + parseErr.Error(), nil
	}
	if ep.Service == nil {
		return []netpol.Dest{ep.Dest}, "", nil
	}
	res, err := s.resolveService(ctx, ep.Service.Namespace, ep.Service.Name, ports)
	if err != nil {
		return nil, "", err
	}
	if res.Reason != "" {
		return nil, where + res.Reason, nil
	}
	return res.Dests, "", nil
}

// gatewayOtherDests are the gateway's other outbound calls: the database and Redis of a
// ContextForge gateway, and the query service of its tool index. The query service is read
// from its Service on any port the Service lists. A URL read by the same host rule as an
// external endpoint, whose port cannot be determined or whose Service does not exist, is
// returned as a reason, not dropped.
func (s *NetPolicySync) gatewayOtherDests(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway) ([]netpol.Dest, []string, error) {
	type call struct{ what, raw string }
	var calls []call
	if cf := gateway.Spec.ContextForge; cf != nil {
		calls = append(calls, call{"spec.contextForge.databaseURL", cf.DatabaseURL}, call{"spec.contextForge.redisURL", cf.RedisURL})
	}
	if gateway.Spec.ToolIndex != nil {
		calls = append(calls, call{"the tool index query endpoint", gateway.Status.ToolIndexEndpoint})
	}
	var dests []netpol.Dest
	var invalid []string
	for _, c := range calls {
		if !callsTheNetwork(c.raw) {
			continue
		}
		ep, err := netpol.ParseEndpoint(c.raw, gateway.Namespace)
		found, reason, rerr := s.endpointDests(ctx, "gateway "+c.what+": ", ep, err, nil)
		if rerr != nil {
			return nil, nil, rerr
		}
		dests = append(dests, found...)
		if reason != "" {
			invalid = append(invalid, reason)
		}
	}
	return dests, invalid, nil
}

// callsTheNetwork reports whether a configured URL names a server: an empty URL and a
// local file database such as sqlite:///./mcp.db do not.
func callsTheNetwork(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	return err != nil || u.Hostname() != ""
}
