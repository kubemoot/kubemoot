/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// The Agent and Crew CRDs take the four workspace access values at the API server and refuse
// anything else, and a resource that declares none is admitted as it always was.
var _ = Describe("Workspace access declarations", func() {
	agentWith := func(name string, access kubemootv1alpha1.WorkspaceAccess) *kubemootv1alpha1.Agent {
		a := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: celNamespace}}
		if access != "" {
			a.Spec.Workspace = &kubemootv1alpha1.AgentWorkspace{Access: access}
		}
		return a
	}
	crewWith := func(name string, access kubemootv1alpha1.WorkspaceAccess) *kubemootv1alpha1.Crew {
		c := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: celNamespace}}
		c.Spec.Workspace = &kubemootv1alpha1.CrewWorkspace{DefaultAccess: access}
		return c
	}

	for _, value := range []kubemootv1alpha1.WorkspaceAccess{
		kubemootv1alpha1.WorkspaceAccessNone, kubemootv1alpha1.WorkspaceAccessRead,
		kubemootv1alpha1.WorkspaceAccessWrite, kubemootv1alpha1.WorkspaceAccessReadWrite,
	} {
		It("admits an Agent and a Crew declaring "+string(value), func() {
			a, c := agentWith("ws-agent-"+strings.ToLower(string(value)), value), crewWith("ws-crew-"+strings.ToLower(string(value)), value)
			Expect(k8sClient.Create(ctx, a)).To(Succeed())
			Expect(k8sClient.Create(ctx, c)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, a); _ = k8sClient.Delete(ctx, c) })
		})
	}

	It("refuses an access value that is not one of the four", func() {
		Expect(k8sClient.Create(ctx, agentWith("ws-agent-bad", "writeOnly"))).NotTo(Succeed())
		Expect(k8sClient.Create(ctx, crewWith("ws-crew-bad", "all"))).NotTo(Succeed())
	})

	It("admits an Agent that declares nothing, with an unset workspace", func() {
		a := agentWith("ws-agent-undeclared", "")
		Expect(k8sClient.Create(ctx, a)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, a) })
		Expect(a.Spec.Workspace).To(BeNil())
	})
})
