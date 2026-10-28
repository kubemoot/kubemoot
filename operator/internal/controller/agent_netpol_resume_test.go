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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	resumeNS         = "np-resume"
	resumeFallbackNS = "np-resume-fallback"
	resumeNoneNS     = "np-resume-none"
	resumeAgentName  = "lead"
	resumePolicy     = "km-agent-lead-egress"
	resumeSourceKey  = "kubemoot.ai/rag-source"
)

var _ = Describe("Egress to the resume query service of a coordinator", func() {
	ctx := context.Background()

	agentIn := func(namespace, role string) *aiv1alpha1.Agent {
		agent := &aiv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: resumeAgentName, Namespace: namespace},
			Spec: aiv1alpha1.AgentSpec{
				Capabilities: []string{candReasoning},
				DiscussRole:  role,
				Egress:       &aiv1alpha1.EgressRules{},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())
		return agent
	}
	reconcile := func(agent *aiv1alpha1.Agent) {
		r := &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(true), false)}
		Expect(r.reconcileNetworkPolicies(ctx, agent, nil, nil)).To(Succeed())
	}

	It("takes its port and pods from the resume query Service, with no RAG source named", func() {
		npNamespace(ctx, resumeNS)
		esService(ctx, resumeNS, resumeRAGSourceName(resumeNS)+"-query", map[string]string{resumeSourceKey: resumeRAGSourceName(resumeNS)}, nil,
			corev1.ServicePort{Port: 9000, TargetPort: intstr.FromInt32(9001)})
		reconcile(agentIn(resumeNS, roleCoordinator))

		policy := npGenerated(ctx, resumeNS)[resumePolicy]
		rules := esRulesOnPort(policy, 9001)
		Expect(rules).To(HaveLen(1))
		Expect(rules[0].To[0].PodSelector.MatchLabels).To(HaveKeyWithValue(resumeSourceKey, resumeRAGSourceName(resumeNS)))
		Expect(esRulesOnPort(policy, resumeQueryPort)).To(BeEmpty(), "the default port is only a fallback")
	})

	It("falls back to the endpoint in its configuration while the Service does not exist", func() {
		npNamespace(ctx, resumeFallbackNS)
		reconcile(agentIn(resumeFallbackNS, roleCoordinator))
		Expect(esRulesOnPort(npGenerated(ctx, resumeFallbackNS)[resumePolicy], resumeQueryPort)).To(HaveLen(1))
	})

	It("gives an agent that is not a coordinator and names no RAG source no query rule", func() {
		npNamespace(ctx, resumeNoneNS)
		esService(ctx, resumeNoneNS, resumeRAGSourceName(resumeNoneNS)+"-query", map[string]string{resumeSourceKey: "x"}, nil,
			corev1.ServicePort{Port: resumeQueryPort})
		reconcile(agentIn(resumeNoneNS, roleToolerResume))
		Expect(esRulesOnPort(npGenerated(ctx, resumeNoneNS)[resumePolicy], resumeQueryPort)).To(BeEmpty())
	})
})
