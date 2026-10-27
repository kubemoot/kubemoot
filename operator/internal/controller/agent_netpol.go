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

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/netpol"
)

// reconcileNetworkPolicies applies the egress policy of an Agent that declares egress and
// records the EgressEnforced condition on the Agent. Agents need no ingress policy: the
// namespace baseline covers them.
func (r *AgentReconciler) reconcileNetworkPolicies(ctx context.Context, agent *kubemootv1alpha1.Agent, mulling, triage *modelPick) error {
	if r.NetPol == nil {
		return nil
	}
	cfg := r.NetPol.Resolved()
	workload := netpol.Workload{
		Name: "agent-" + agent.Name, Namespace: agent.Namespace,
		Selector: map[string]string{labelAgent: agent.Name}, Rules: agent.Spec.Egress,
	}
	if cfg.EgressOn() && agent.Spec.Egress != nil {
		workload.Defaults = r.agentEgressDefaults(ctx, agent, mulling, triage)
	}
	egress := netpol.Egress(cfg, workload)
	setEgressCondition(&agent.Status.Conditions, agent.Generation, egress.Status)
	return r.NetPol.Apply(ctx, agent, agent.Namespace, egress.Objects)
}

// agentEgressDefaults are the destinations an agent always reaches: the message bus, the
// crew's tool gateway, every model server it may be placed on, its RAG query services, the
// placement service, and the telemetry collector.
func (r *AgentReconciler) agentEgressDefaults(ctx context.Context, agent *kubemootv1alpha1.Agent, mulling, triage *modelPick) []netpol.Dest {
	urls := []string{agentNATSURL(), r.placementServiceURL()}
	if r.ConfigCache != nil {
		urls = append(urls, r.ConfigCache.GetOTelCollectorEndpoint())
	}
	for _, pick := range []*modelPick{mulling, triage} {
		if pick != nil {
			urls = append(urls, pick.Endpoint)
		}
	}
	urls = append(urls, r.modelServerEndpoints(ctx, agent.Namespace)...)
	urls = append(urls, r.ragEndpoints(ctx, agent)...)

	var dests []netpol.Dest
	for _, u := range urls {
		dests = append(dests, netpol.DestsFromURL(u)...)
	}
	if _, port, ok := r.findCrewGateway(ctx, agent.Namespace); ok {
		dests = append(dests, netpol.Dest{
			SameNamespace: true, Port: port,
			PodLabels: map[string]string{labelComponent: componentMCPGateway},
		})
	}
	return dests
}

// modelServerEndpoints lists the endpoints of the ModelServers an agent in the namespace
// can be placed on: those in its namespace and in the operator's.
func (r *AgentReconciler) modelServerEndpoints(ctx context.Context, namespace string) []string {
	var out []string
	for _, ns := range uniqueStrings(namespace, operatorNamespace()) {
		servers := &kubemootv1alpha1.ModelServerList{}
		if err := r.List(ctx, servers, client.InNamespace(ns)); err != nil {
			continue
		}
		for i := range servers.Items {
			spec := servers.Items[i].EffectiveSpec()
			out = append(out, spec.Endpoint)
			for _, s := range spec.Servers {
				out = append(out, s.Endpoint)
			}
		}
	}
	return out
}

// ragEndpoints are the query endpoints of the RAG sources the agent names.
func (r *AgentReconciler) ragEndpoints(ctx context.Context, agent *kubemootv1alpha1.Agent) []string {
	var out []string
	for _, ref := range agent.Spec.RAGSources {
		if endpoint, ok := r.ragQueryEndpointFor(ctx, ref.Name, agent.Namespace); ok {
			out = append(out, endpoint)
		}
	}
	return out
}

func uniqueStrings(values ...string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
