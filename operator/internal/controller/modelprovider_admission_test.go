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
				Type:    aiv1alpha1.ProviderTypeVLLM,
				Servers: servers,
			},
		}
	}
	newRef := func(name string) *aiv1alpha1.ModelProvider {
		mp := newVLLM(name)
		mp.Spec.ServiceRef = &aiv1alpha1.VLLMServiceRef{Name: "vllm-gpu"}
		return mp
	}
	oneServer := aiv1alpha1.VLLMServer{Model: vllmModel14, Endpoint: vllmEndpoint14}
	create := func(mp *aiv1alpha1.ModelProvider) error {
		err := k8sClient.Create(ctx, mp)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, mp) })
		}
		return err
	}
	rejected := func(err error, message string) {
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(message))
	}

	It("accepts type vllm with servers and leaves the sleep level unset", func() {
		mp := newVLLM("adm-vllm", oneServer, aiv1alpha1.VLLMServer{Model: vllmModel8, Endpoint: "https://vllm-8b.models:8000"})
		Expect(create(mp)).To(Succeed())
		Expect(mp.Spec.SleepLevel).To(Equal(0))
		Expect(mp.Spec.SleepLevelOrDefault()).To(Equal(1))
	})

	It("accepts type vllm with a serviceRef", func() {
		mp := newRef("adm-vllm-ref")
		mp.Spec.ServiceRef.Namespace = modelsNamespace
		mp.Spec.SleepLevel = 2
		Expect(create(mp)).To(Succeed())
	})

	It("rejects a vllm provider with neither serviceRef nor servers", func() {
		rejected(create(newVLLM("adm-vllm-bare")), "exactly one of spec.serviceRef")
	})

	It("rejects a vllm provider with both serviceRef and servers", func() {
		mp := newRef("adm-vllm-both")
		mp.Spec.Servers = []aiv1alpha1.VLLMServer{oneServer}
		rejected(create(mp), "exactly one of spec.serviceRef")
	})

	It("rejects a serviceRef without a name", func() {
		mp := newRef("adm-vllm-noname")
		mp.Spec.ServiceRef.Name = ""
		Expect(create(mp)).NotTo(Succeed())
	})

	It("rejects spec.endpoint on a vllm provider", func() {
		mp := newVLLM("adm-vllm-endpoint", oneServer)
		mp.Spec.Endpoint = "http://elsewhere:8000"
		rejected(create(mp), "spec.endpoint is only for an ollama provider")
	})

	It("rejects serviceRef, servers, and sleepLevel on an ollama provider", func() {
		withRef := newMP("adm-ollama-ref", testOllama)
		withRef.Spec.ServiceRef = &aiv1alpha1.VLLMServiceRef{Name: "x"}
		rejected(create(withRef), "spec.serviceRef is only for a vllm provider")

		withServers := newMP("adm-ollama-servers", testOllama)
		withServers.Spec.Servers = []aiv1alpha1.VLLMServer{oneServer}
		rejected(create(withServers), "spec.servers is only for a vllm provider")

		withLevel := newMP("adm-ollama-level", testOllama)
		withLevel.Spec.SleepLevel = 1
		rejected(create(withLevel), "spec.sleepLevel is only for a vllm provider")
	})

	It("rejects a sleep level other than 1 or 2", func() {
		mp := newVLLM("adm-vllm-level", oneServer)
		mp.Spec.SleepLevel = 3
		Expect(create(mp)).NotTo(Succeed())
	})

	It("rejects changing the sleep level", func() {
		mp := newVLLM("adm-vllm-immutable", oneServer)
		mp.Spec.SleepLevel = 1
		Expect(create(mp)).To(Succeed())
		mp.Spec.SleepLevel = 2
		rejected(k8sClient.Update(ctx, mp), "sleepLevel is immutable")
	})

	It("rejects setting a sleep level of 2 on a provider that left it unset", func() {
		mp := newVLLM("adm-vllm-unset-2", oneServer)
		Expect(create(mp)).To(Succeed())
		mp.Spec.SleepLevel = 2
		rejected(k8sClient.Update(ctx, mp), "sleepLevel is immutable")
	})

	It("rejects unsetting a sleep level of 2", func() {
		mp := newVLLM("adm-vllm-unset-from-2", oneServer)
		mp.Spec.SleepLevel = 2
		Expect(create(mp)).To(Succeed())
		mp.Spec.SleepLevel = 0
		rejected(k8sClient.Update(ctx, mp), "sleepLevel is immutable")
	})

	It("allows setting the sleep level to 1 on a provider that left it unset", func() {
		mp := newVLLM("adm-vllm-unset", oneServer)
		Expect(create(mp)).To(Succeed())
		mp.Spec.SleepLevel = 1 // absent means 1
		Expect(k8sClient.Update(ctx, mp)).To(Succeed())
	})

	It("accepts an update that keeps the sleep level and changes the servers", func() {
		mp := newVLLM("adm-vllm-servers", oneServer)
		mp.Spec.SleepLevel = 2
		Expect(create(mp)).To(Succeed())
		mp.Spec.Servers = append(mp.Spec.Servers, aiv1alpha1.VLLMServer{Model: vllmModel8, Endpoint: "http://vllm-8b.models:8000"})
		Expect(k8sClient.Update(ctx, mp)).To(Succeed())
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
