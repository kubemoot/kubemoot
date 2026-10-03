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

	It("accepts type ollama", func() {
		mp := newMP("adm-ollama", "ollama")
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
