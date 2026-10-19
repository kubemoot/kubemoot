/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

var _ = Describe("ModelProvider admission", func() {
	newMP := func(name, typ string) *aiv1alpha1.ModelProvider {
		return &aiv1alpha1.ModelProvider{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: metav1.NamespaceDefault},
			Spec: aiv1alpha1.ModelProviderSpec{
				Type:     aiv1alpha1.ProviderType(typ),
				Endpoint: "http://ollama.example:11434",
			},
		}
	}

	newVLLM := func(name string, servers ...aiv1alpha1.VLLMServer) *aiv1alpha1.ModelProvider {
		return &aiv1alpha1.ModelProvider{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: metav1.NamespaceDefault},
			Spec: aiv1alpha1.ModelProviderSpec{
				Type: aiv1alpha1.ProviderTypeVLLM,
				VLLM: &aiv1alpha1.VLLMSpec{Servers: servers},
			},
		}
	}
	oneServer := aiv1alpha1.VLLMServer{Model: vllmModel14, Endpoint: vllmEndpoint14}

	It("accepts type vllm with servers and defaults the sleep level to 1", func() {
		mp := newVLLM("adm-vllm", oneServer, aiv1alpha1.VLLMServer{Model: vllmModel8, Endpoint: "https://vllm-8b.models:8000"})
		Expect(k8sClient.Create(ctx, mp)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, mp) })
		Expect(mp.Spec.VLLM.SleepLevel).To(Equal(1))
	})

	It("rejects type vllm without spec.vllm", func() {
		mp := newMP("adm-vllm-bare", "vllm")
		mp.Spec.Endpoint = ""
		err := k8sClient.Create(ctx, mp)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("a vllm provider needs spec.vllm"))
	})

	It("rejects spec.endpoint on a vllm provider", func() {
		mp := newVLLM("adm-vllm-endpoint", oneServer)
		mp.Spec.Endpoint = "http://elsewhere:8000"
		err := k8sClient.Create(ctx, mp)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("a vllm provider has no spec.endpoint"))
	})

	It("rejects a vllm provider with no servers", func() {
		Expect(k8sClient.Create(ctx, newVLLM("adm-vllm-empty"))).NotTo(Succeed())
	})

	It("rejects spec.vllm on an ollama provider", func() {
		mp := newMP("adm-ollama-vllm", testOllama)
		mp.Spec.VLLM = &aiv1alpha1.VLLMSpec{Servers: []aiv1alpha1.VLLMServer{oneServer}}
		err := k8sClient.Create(ctx, mp)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.vllm is only for a vllm provider"))
	})

	It("rejects a sleep level other than 1 or 2", func() {
		mp := newVLLM("adm-vllm-level", oneServer)
		mp.Spec.VLLM.SleepLevel = 3
		Expect(k8sClient.Create(ctx, mp)).NotTo(Succeed())
	})

	It("rejects two servers for one served model", func() {
		other := aiv1alpha1.VLLMServer{Model: oneServer.Model, Endpoint: "http://elsewhere:8000"}
		Expect(k8sClient.Create(ctx, newVLLM("adm-vllm-dup", oneServer, other))).NotTo(Succeed())
	})

	It("rejects a server endpoint that is not an http URL", func() {
		bad := aiv1alpha1.VLLMServer{Model: vllmModel14, Endpoint: "vllm-14b.models:8000"}
		Expect(k8sClient.Create(ctx, newVLLM("adm-vllm-url", bad))).NotTo(Succeed())
	})

	It("accepts type ollama", func() {
		mp := newMP("adm-ollama", testOllama)
		Expect(k8sClient.Create(ctx, mp)).To(Succeed())
		Expect(k8sClient.Delete(ctx, mp)).To(Succeed())
	})

	for _, typ := range unsupportedProviderTypes {
		It("rejects type "+typ+" with the not-supported message", func() {
			err := k8sClient.Create(ctx, newMP("adm-"+typ, typ))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(aiv1alpha1.UnsupportedProviderTypeMessage))
		})
	}
})
