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

var _ = Describe("Model spec accelerator memory admission", func() {
	newModel := func(name string, accelerator, vram int32) *aiv1alpha1.Model {
		return &aiv1alpha1.Model{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: metav1.NamespaceDefault},
			Spec: aiv1alpha1.ModelSpec{
				ServerRef: "adm-server", Model: "qwen3:14b",
				AcceleratorMemoryMiB: accelerator, VRAMMib: vram,
			},
		}
	}
	accepts := func(name string, accelerator, vram int32) {
		m := newModel(name, accelerator, vram)
		Expect(k8sClient.Create(ctx, m)).To(Succeed())
		Expect(k8sClient.Delete(ctx, m)).To(Succeed())
	}

	It("accepts acceleratorMemoryMiB alone", func() { accepts("mem-new", 11000, 0) })
	It("accepts the deprecated vramMib alone", func() { accepts("mem-old", 0, 11000) })
	It("accepts both with the same value", func() { accepts("mem-same", 11000, 11000) })
	It("rejects both with different values", func() {
		err := k8sClient.Create(ctx, newModel("mem-differ", 11000, 12000))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("old name of spec.acceleratorMemoryMiB"))
	})
})
