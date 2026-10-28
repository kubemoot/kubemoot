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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/netpol"
)

const (
	esPromApp    = "prom"
	esNATSNS     = "nats"
	esAppKey     = "app"
	esDocsQuery  = "docs-query"
	esPlacement  = "ps"
	esSvcNS      = "np-svc"
	esObsNS      = "np-obs"
	esAgentNS    = "np-query"
	esPlacedNS   = "np-placed"
	esMCPName    = "tool"
	esLater      = "later"
	esMapA       = "np-map-a"
	esWith       = "with"
	esLateNS     = "np-svc-late"
	esMetadataNS = "kubernetes.io/metadata.name"
)

func esService(ctx context.Context, namespace, name string, selector map[string]string, labels map[string]string, port corev1.ServicePort) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec:       corev1.ServiceSpec{Selector: selector, Ports: []corev1.ServicePort{port}},
	}
	Expect(k8sClient.Create(ctx, svc)).To(Succeed())
}

// esRulesOnPort are the egress rules of the policy that allow the port.
func esRulesOnPort(p networkingv1.NetworkPolicy, port int32) []networkingv1.NetworkPolicyEgressRule {
	var out []networkingv1.NetworkPolicyEgressRule
	for _, rule := range p.Spec.Egress {
		for _, rp := range rule.Ports {
			if rp.Port != nil && rp.Port.IntVal == port && rp.Protocol != nil && *rp.Protocol == corev1.ProtocolTCP {
				out = append(out, rule)
				break
			}
		}
	}
	return out
}

func esCondition(conditions []metav1.Condition) *metav1.Condition {
	return meta.FindStatusCondition(conditions, netpol.ConditionEgressEnforced)
}

var _ = Describe("Egress to cluster destinations", func() {
	ctx := context.Background()

	newMCP := func() *MCPServerReconciler {
		return &MCPServerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(true), false)}
	}

	getMCP := func(namespace, name string) *aiv1alpha1.MCPServer {
		got := &aiv1alpha1.MCPServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, got)).To(Succeed())
		return got
	}

	Context("the message bus for a tool server", func() {
		It("is reachable with bus: true, at the address the operator is configured with", func() {
			npNamespace(ctx, "np-bus")
			npMCPServer(ctx, "np-bus", esMCPName, &aiv1alpha1.EgressRules{Bus: true})
			npReconcileMCPServer(ctx, newMCP(), "np-bus", esMCPName)

			policy := npGenerated(ctx, "np-bus")["km-mcp-tool-egress"]
			rules := esRulesOnPort(policy, 4222)
			Expect(rules).To(HaveLen(1))
			Expect(rules[0].To[0].NamespaceSelector.MatchLabels).To(HaveKeyWithValue(esMetadataNS, esNATSNS))
			Expect(esCondition(getMCP("np-bus", esMCPName).Status.Conditions).Status).To(Equal(metav1.ConditionTrue))
		})

		It("follows the operator's NATS_URL", func() {
			GinkgoT().Setenv("NATS_URL", "nats://bus.messaging.svc:4333")
			npNamespace(ctx, "np-bus-url")
			npMCPServer(ctx, "np-bus-url", esMCPName, &aiv1alpha1.EgressRules{Bus: true})
			npReconcileMCPServer(ctx, newMCP(), "np-bus-url", esMCPName)

			rules := esRulesOnPort(npGenerated(ctx, "np-bus-url")["km-mcp-tool-egress"], 4333)
			Expect(rules).To(HaveLen(1))
			Expect(rules[0].To[0].NamespaceSelector.MatchLabels).To(HaveKeyWithValue(esMetadataNS, "messaging"))
		})

		It("is not reachable without bus", func() {
			npNamespace(ctx, "np-nobus")
			npMCPServer(ctx, "np-nobus", esMCPName, &aiv1alpha1.EgressRules{AllowedCIDRs: []aiv1alpha1.EgressCIDR{{CIDR: npCIDR}}})
			npReconcileMCPServer(ctx, newMCP(), "np-nobus", esMCPName)
			Expect(esRulesOnPort(npGenerated(ctx, "np-nobus")["km-mcp-tool-egress"], 4222)).To(BeEmpty())
		})
	})

	Context("an allowed Service", func() {
		allow := []aiv1alpha1.EgressService{{Namespace: esObsNS, Name: esPromApp, Ports: []int32{9090}}}

		It("becomes the Service's namespace, pod selector and pod port", func() {
			npNamespace(ctx, esObsNS)
			npNamespace(ctx, esSvcNS)
			esService(ctx, esObsNS, esPromApp, map[string]string{esAppKey: esPromApp}, nil,
				corev1.ServicePort{Port: 9090, TargetPort: intstr.FromInt32(9091)})
			npMCPServer(ctx, esSvcNS, esMCPName, &aiv1alpha1.EgressRules{AllowedServices: allow})
			npReconcileMCPServer(ctx, newMCP(), esSvcNS, esMCPName)

			rules := esRulesOnPort(npGenerated(ctx, esSvcNS)["km-mcp-tool-egress"], 9091)
			Expect(rules).To(HaveLen(1))
			Expect(rules[0].To[0].NamespaceSelector.MatchLabels).To(HaveKeyWithValue(esMetadataNS, esObsNS))
			Expect(rules[0].To[0].PodSelector.MatchLabels).To(Equal(map[string]string{esAppKey: esPromApp}))
			Expect(esRulesOnPort(npGenerated(ctx, esSvcNS)["km-mcp-tool-egress"], 9090)).To(BeEmpty(), "the Service port is not the pod port")
			cond := esCondition(getMCP(esSvcNS, esMCPName).Status.Conditions)
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})

		It("defaults to the workload's namespace and every port", func() {
			npNamespace(ctx, "np-svc-own")
			esService(ctx, "np-svc-own", esPromApp, map[string]string{esAppKey: esPromApp}, nil, corev1.ServicePort{Port: 7000})
			npMCPServer(ctx, "np-svc-own", esMCPName, &aiv1alpha1.EgressRules{AllowedServices: []aiv1alpha1.EgressService{{Name: esPromApp}}})
			npReconcileMCPServer(ctx, newMCP(), "np-svc-own", esMCPName)

			rules := esRulesOnPort(npGenerated(ctx, "np-svc-own")["km-mcp-tool-egress"], 7000)
			Expect(rules).To(HaveLen(1))
			Expect(rules[0].To[0].NamespaceSelector.MatchLabels).To(HaveKeyWithValue(esMetadataNS, "np-svc-own"))
		})

		It("gets no rule while the Service is missing, says so, and gets one when it appears", func() {
			npNamespace(ctx, esLateNS)
			late := []aiv1alpha1.EgressService{{Namespace: esLateNS, Name: esLater}}
			npMCPServer(ctx, esLateNS, esMCPName, &aiv1alpha1.EgressRules{AllowedServices: late})
			r := newMCP()
			npReconcileMCPServer(ctx, r, esLateNS, esMCPName)

			policy := npGenerated(ctx, esLateNS)["km-mcp-tool-egress"]
			Expect(policy.Spec.Egress).To(HaveLen(1), "DNS only")
			cond := esCondition(getMCP(esLateNS, esMCPName).Status.Conditions)
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(netpol.ReasonServiceNotResolved))
			Expect(cond.Message).To(ContainSubstring(esLateNS + "/" + esLater))

			esService(ctx, esLateNS, esLater, map[string]string{esAppKey: esLater}, nil, corev1.ServicePort{Port: 8123})
			Expect(requestNames(mcpServersForService(ctx, k8sClient, &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Namespace: esLateNS, Name: esLater},
			}))).To(ConsistOf(types.NamespacedName{Namespace: esLateNS, Name: esMCPName}))
			npReconcileMCPServer(ctx, r, esLateNS, esMCPName)
			Expect(esRulesOnPort(npGenerated(ctx, esLateNS)["km-mcp-tool-egress"], 8123)).To(HaveLen(1))
			Expect(esCondition(getMCP(esLateNS, esMCPName).Status.Conditions).Status).To(Equal(metav1.ConditionTrue))
		})

		It("gets no rule for a Service without a pod selector", func() {
			npNamespace(ctx, "np-svc-bare")
			esService(ctx, "np-svc-bare", "bare", nil, nil, corev1.ServicePort{Port: 80})
			npMCPServer(ctx, "np-svc-bare", esMCPName, &aiv1alpha1.EgressRules{AllowedServices: []aiv1alpha1.EgressService{{Name: "bare"}}})
			npReconcileMCPServer(ctx, newMCP(), "np-svc-bare", esMCPName)

			Expect(npGenerated(ctx, "np-svc-bare")["km-mcp-tool-egress"].Spec.Egress).To(HaveLen(1))
			cond := esCondition(getMCP("np-svc-bare", esMCPName).Status.Conditions)
			Expect(cond.Reason).To(Equal(netpol.ReasonServiceNotResolved))
			Expect(cond.Message).To(ContainSubstring("no pod selector"))
		})

		It("is not enqueued for an unrelated Service", func() {
			Expect(requestNames(mcpServersForService(ctx, k8sClient, &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Namespace: esLateNS, Name: "unrelated"},
			}))).To(BeEmpty())
		})
	})

	Context("the query service of an Agent", func() {
		agentWith := func(namespace string) *aiv1alpha1.Agent {
			agent := &aiv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{Name: "asker", Namespace: namespace},
				Spec: aiv1alpha1.AgentSpec{
					Capabilities: []string{candReasoning},
					RAGSources:   []aiv1alpha1.RAGSourceRef{{Name: npDocs}},
					Egress:       &aiv1alpha1.EgressRules{},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
			return agent
		}
		reconcileAgent := func(agent *aiv1alpha1.Agent) {
			r := &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(true), false)}
			Expect(r.reconcileNetworkPolicies(ctx, agent, nil, nil)).To(Succeed())
		}

		It("takes its port and pods from the real Service", func() {
			npNamespace(ctx, esAgentNS)
			esService(ctx, esAgentNS, esDocsQuery, map[string]string{"kubemoot.ai/rag-source": npDocs}, nil,
				corev1.ServicePort{Port: 9000, TargetPort: intstr.FromInt32(9001)})
			reconcileAgent(agentWith(esAgentNS))

			policy := npGenerated(ctx, esAgentNS)["km-agent-asker-egress"]
			rules := esRulesOnPort(policy, 9001)
			Expect(rules).To(HaveLen(1))
			Expect(rules[0].To[0].PodSelector.MatchLabels).To(HaveKeyWithValue("kubemoot.ai/rag-source", npDocs))
			Expect(esRulesOnPort(policy, 8000)).To(BeEmpty(), "the default port is only a fallback")
		})

		It("falls back to the endpoint the RAGSource reports while its Service does not exist", func() {
			npNamespace(ctx, "np-query-fallback")
			reconcileAgent(agentWith("np-query-fallback"))
			Expect(esRulesOnPort(npGenerated(ctx, "np-query-fallback")["km-agent-asker-egress"], 8000)).To(HaveLen(1))
		})
	})

	Context("the placement service of an Agent", func() {
		placementCache := func() *ConfigCache {
			cache := NewConfigCache()
			cache.Update(&aiv1alpha1.KubemootConfig{Spec: aiv1alpha1.KubemootConfigSpec{Defaults: aiv1alpha1.DefaultConfig{
				Scheduler: &aiv1alpha1.SchedulerConfig{PlacementService: &aiv1alpha1.PlacementServiceConfig{
					Enabled: true, URL: "http://ps.kubemoot.svc:8080",
				}},
			}}})
			return cache
		}
		agent := func(namespace string) *aiv1alpha1.Agent {
			a := &aiv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{Name: "placed", Namespace: namespace},
				Spec:       aiv1alpha1.AgentSpec{Capabilities: []string{candReasoning}, Egress: &aiv1alpha1.EgressRules{}},
			}
			Expect(k8sClient.Create(ctx, a)).To(Succeed())
			return a
		}

		It("gets no allow when no Service exists behind the configured URL", func() {
			npNamespace(ctx, esPlacedNS)
			a := agent(esPlacedNS)
			r := &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: placementCache(), NetPol: npSync(npConfig(true), false)}
			Expect(r.reconcileNetworkPolicies(ctx, a, nil, nil)).To(Succeed())

			Expect(esRulesOnPort(npGenerated(ctx, esPlacedNS)["km-agent-placed-egress"], 8080)).To(BeEmpty())
			cond := esCondition(a.Status.Conditions)
			Expect(cond.Reason).To(Equal(netpol.ReasonServiceNotResolved))
			Expect(cond.Message).To(ContainSubstring("placement service"))
		})

		It("targets the Service's pods when it exists", func() {
			npNamespace(ctx, "np-placed-yes")
			npNamespace(ctx, npOperatorNS)
			esService(ctx, npOperatorNS, esPlacement, map[string]string{labelComponent: componentPlacementService},
				map[string]string{labelComponent: componentPlacementService}, corev1.ServicePort{Port: 8080, TargetPort: intstr.FromInt32(8080)})
			a := agent("np-placed-yes")
			r := &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: placementCache(), NetPol: npSync(npConfig(true), false)}
			Expect(r.reconcileNetworkPolicies(ctx, a, nil, nil)).To(Succeed())

			rules := esRulesOnPort(npGenerated(ctx, "np-placed-yes")["km-agent-placed-egress"], 8080)
			Expect(rules).To(HaveLen(1))
			Expect(rules[0].To[0].NamespaceSelector.MatchLabels).To(HaveKeyWithValue(esMetadataNS, npOperatorNS))
			Expect(rules[0].To[0].PodSelector.MatchLabels).To(Equal(map[string]string{labelComponent: componentPlacementService}))
			Expect(esCondition(a.Status.Conditions).Status).To(Equal(metav1.ConditionTrue))

			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: npOperatorNS, Name: esPlacement}, svc)).To(Succeed())
			Expect(requestNames(agentsForService(ctx, k8sClient, svc))).To(ContainElement(
				types.NamespacedName{Namespace: "np-placed-yes", Name: "placed"}))
		})
	})
})

var _ = Describe("Which workloads a Service change refreshes", func() {
	ctx := context.Background()

	It("refreshes Agents with egress beside a query service, all of them for the placement service, and none otherwise", func() {
		npNamespace(ctx, esMapA)
		npNamespace(ctx, "np-map-b")
		for _, ns := range []string{esMapA, "np-map-b"} {
			Expect(k8sClient.Create(ctx, &aiv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{Name: esWith, Namespace: ns},
				Spec:       aiv1alpha1.AgentSpec{Capabilities: []string{candReasoning}, Egress: &aiv1alpha1.EgressRules{}},
			})).To(Succeed())
		}
		Expect(k8sClient.Create(ctx, &aiv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: "without", Namespace: esMapA},
			Spec:       aiv1alpha1.AgentSpec{Capabilities: []string{candReasoning}},
		})).To(Succeed())

		svc := func(ns, component string) *corev1.Service {
			return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "x", Labels: map[string]string{labelComponent: component}}}
		}
		query := requestNames(agentsForService(ctx, k8sClient, svc(esMapA, componentQueryService)))
		Expect(query).To(ConsistOf(types.NamespacedName{Namespace: esMapA, Name: esWith}))

		placement := requestNames(agentsForService(ctx, k8sClient, svc(npOperatorNS, componentPlacementService)))
		Expect(placement).To(ContainElements(
			types.NamespacedName{Namespace: esMapA, Name: esWith}, types.NamespacedName{Namespace: "np-map-b", Name: esWith}))
		Expect(placement).NotTo(ContainElement(types.NamespacedName{Namespace: esMapA, Name: "without"}))

		Expect(requestNames(agentsForService(ctx, k8sClient, svc(esMapA, "other")))).To(BeEmpty())
	})

	It("keys an allowed Service by the workload's namespace when it names none", func() {
		Expect(egressServiceKey(aiv1alpha1.EgressService{Name: "s"}, "ns")).To(Equal(types.NamespacedName{Namespace: "ns", Name: "s"}))
		Expect(egressServiceKey(aiv1alpha1.EgressService{Namespace: "o", Name: "s"}, "ns")).To(Equal(types.NamespacedName{Namespace: "o", Name: "s"}))
		Expect(servicesReferencing(nil, "ns")).To(BeEmpty())
	})

	It("does not repeat the bus rule for an agent that sets bus", func() {
		npNamespace(ctx, "np-agent-bus")
		agent := &aiv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: "busy", Namespace: "np-agent-bus"},
			Spec:       aiv1alpha1.AgentSpec{Capabilities: []string{candReasoning}, Egress: &aiv1alpha1.EgressRules{Bus: true}},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())
		r := &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(true), false)}
		Expect(r.reconcileNetworkPolicies(ctx, agent, nil, nil)).To(Succeed())
		Expect(esRulesOnPort(npGenerated(ctx, "np-agent-bus")["km-agent-busy-egress"], 4222)).To(HaveLen(1))
	})
})

// requestNames are the namespaced names of reconcile requests.
func requestNames(reqs []reconcile.Request) []types.NamespacedName {
	out := make([]types.NamespacedName, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.NamespacedName)
	}
	return out
}
