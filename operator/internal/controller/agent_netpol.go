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
		if err := r.attachAgentDestinations(ctx, agent, &workload, mulling, triage); err != nil {
			return err
		}
	}
	egress := netpol.Egress(cfg, workload)
	setEgressCondition(&agent.Status.Conditions, agent.Generation, egress.Status)
	return r.NetPol.Apply(ctx, agent, agent.Namespace, egress.Objects)
}

// attachAgentDestinations fills the workload with what an agent always reaches and what
// its egress names in the cluster.
func (r *AgentReconciler) attachAgentDestinations(ctx context.Context, agent *kubemootv1alpha1.Agent, w *netpol.Workload, mulling, triage *modelPick) error {
	defaults, unresolved, err := r.agentEgressDefaults(ctx, agent, mulling, triage)
	if err != nil {
		return err
	}
	w.Defaults, w.Unresolved = defaults, unresolved
	return r.NetPol.attachDeclaredDestinations(ctx, w)
}

// agentEgressDefaults are the destinations an agent always reaches: the message bus, the
// crew's tool gateway, every model server it may be placed on, its RAG query services (and, for a coordinator, the resume query service), the
// placement service, and the telemetry collector. The query and placement services are read
// from their Services, so the rule carries the pods and port the Service really routes to.
// The placement service is optional: with no Service behind the configured URL there is
// nothing to allow, and the second result says so.
func (r *AgentReconciler) agentEgressDefaults(ctx context.Context, agent *kubemootv1alpha1.Agent, mulling, triage *modelPick) ([]netpol.Dest, []string, error) {
	urls := []string{agentNATSURL()}
	if r.ConfigCache != nil {
		urls = append(urls, r.ConfigCache.GetOTelCollectorEndpoint())
	}
	for _, pick := range []*modelPick{mulling, triage} {
		if pick != nil {
			urls = append(urls, pick.Endpoint)
		}
	}
	urls = append(urls, r.modelServerEndpoints(ctx, agent.Namespace)...)

	var dests []netpol.Dest
	for _, u := range urls {
		dests = append(dests, netpol.DestsFromURL(u)...)
	}
	queryDests, err := r.queryServiceDests(ctx, agent)
	if err != nil {
		return nil, nil, err
	}
	dests = append(dests, queryDests...)
	placement, unresolved, err := r.placementDests(ctx, agent.Namespace)
	if err != nil {
		return nil, nil, err
	}
	dests = append(dests, placement...)
	if _, port, ok := r.findCrewGateway(ctx, agent.Namespace); ok {
		dests = append(dests, netpol.Dest{
			SameNamespace: true, Port: port,
			PodLabels: map[string]string{labelComponent: componentMCPGateway},
		})
	}
	return dests, unresolved, nil
}

// placementDests are the destinations of the placement service, taken from the Service
// behind the realm's URL. With no Service there is nothing to allow and the second result
// says so; with no URL the realm places in-process and both results are empty.
func (r *AgentReconciler) placementDests(ctx context.Context, namespace string) ([]netpol.Dest, []string, error) {
	url := r.placementServiceURL()
	if url == "" {
		return nil, nil, nil
	}
	found, reason, err := r.NetPol.serviceURLDests(ctx, url, namespace)
	if err != nil || reason == "" {
		return found, nil, err
	}
	return found, []string{"placement service: " + reason}, nil
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

// queryServiceEndpoints are the query services an agent is configured to call, the same
// ones buildEnvVars gives it: the endpoint of each RAG source it names and, for a
// coordinator, the crew's resume query service.
func (r *AgentReconciler) queryServiceEndpoints(ctx context.Context, agent *kubemootv1alpha1.Agent) []string {
	var out []string
	for _, t := range r.ragQueryTargets(ctx, agent) {
		out = append(out, t.Endpoint)
	}
	if agent.Spec.DiscussRole == roleCoordinator {
		out = append(out, resumeSearchEndpoint(agent, agent.Labels[labelCrew]))
	}
	return out
}

// queryServiceDests are the destinations of the query services the agent is configured to
// call, taken from each query Service. A Service that does not exist yet falls back to the
// endpoint in the agent's configuration, which is the port the RAGSource controller is
// about to create it with.
func (r *AgentReconciler) queryServiceDests(ctx context.Context, agent *kubemootv1alpha1.Agent) ([]netpol.Dest, error) {
	var out []netpol.Dest
	for _, endpoint := range r.queryServiceEndpoints(ctx, agent) {
		found, reason, err := r.NetPol.serviceURLDestsAnyPort(ctx, endpoint, agent.Namespace)
		if err != nil {
			return nil, err
		}
		if reason != "" {
			found = netpol.DestsFromURL(endpoint)
		}
		out = append(out, found...)
	}
	return out, nil
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
