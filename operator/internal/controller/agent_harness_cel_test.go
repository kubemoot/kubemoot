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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	celNamespace = "default"
	celImage     = "example/harness:1"
	celDeclared  = "declared"
)

// The Agent CRD rejects a harness block that cannot work, at the API server, so the rules hold
// where the admission webhook is not installed.
var _ = Describe("Agent harness validation", func() {
	create := func(name string, spec kubemootv1alpha1.AgentSpec) (*kubemootv1alpha1.Agent, error) {
		a := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: celNamespace}, Spec: spec}
		err := k8sClient.Create(ctx, a)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, a) })
		}
		return a, err
	}
	harness := func(h kubemootv1alpha1.HarnessSpec) kubemootv1alpha1.AgentSpec {
		return kubemootv1alpha1.AgentSpec{Harness: &h}
	}

	It("leaves an Agent without a harness alone", func() {
		_, err := create("cel-no-harness", kubemootv1alpha1.AgentSpec{})
		Expect(err).NotTo(HaveOccurred())
	})

	It("defaults a harness block to legacy mode, port 9000, one turn at a time, no native tools", func() {
		a, err := create("cel-defaults", harness(kubemootv1alpha1.HarnessSpec{Name: "runtime"}))
		Expect(err).NotTo(HaveOccurred())
		h := a.Spec.Harness
		Expect(h.Mode).To(Equal(kubemootv1alpha1.HarnessModeLegacy))
		Expect(h.Port).To(BeEquivalentTo(9000))
		Expect(h.Concurrency).To(BeEquivalentTo(1))
		Expect(h.NativeTools).To(Equal("none"))
		Expect(h.Protocol).To(Equal("a2a"))
	})

	It("accepts sidecar mode with a harness image", func() {
		_, err := create("cel-sidecar-image", harness(kubemootv1alpha1.HarnessSpec{
			Mode: kubemootv1alpha1.HarnessModeSidecar, Image: "ghcr.io/example/harness@sha256:abc",
		}))
		Expect(err).NotTo(HaveOccurred())
	})

	It("accepts sidecar mode when the deployment names the harness image", func() {
		spec := harness(kubemootv1alpha1.HarnessSpec{Mode: kubemootv1alpha1.HarnessModeSidecar})
		spec.Deployment = &kubemootv1alpha1.DeploymentConfig{Image: celImage}
		_, err := create("cel-sidecar-deployment-image", spec)
		Expect(err).NotTo(HaveOccurred())
	})

	It("accepts sidecar mode for a harness named goose with no image, which takes the Kubemoot-built one", func() {
		_, err := create("cel-sidecar-goose", harness(kubemootv1alpha1.HarnessSpec{
			Name: HarnessNameGoose, Mode: kubemootv1alpha1.HarnessModeSidecar,
		}))
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects sidecar mode with no image anywhere", func() {
		_, err := create("cel-sidecar-no-image", harness(kubemootv1alpha1.HarnessSpec{Mode: kubemootv1alpha1.HarnessModeSidecar}))
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
		Expect(err.Error()).To(ContainSubstring("needs the harness image"))
	})

	It("rejects more than one turn at a time outside sidecar mode", func() {
		_, err := create("cel-concurrency-legacy", harness(kubemootv1alpha1.HarnessSpec{Concurrency: 2}))
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
		Expect(err.Error()).To(ContainSubstring("concurrency"))
	})

	It("accepts more than one turn at a time in sidecar mode", func() {
		_, err := create("cel-concurrency-sidecar", harness(kubemootv1alpha1.HarnessSpec{
			Mode: kubemootv1alpha1.HarnessModeSidecar, Image: celImage, Concurrency: 3,
		}))
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects native tools outside sidecar mode", func() {
		_, err := create("cel-native-legacy", harness(kubemootv1alpha1.HarnessSpec{NativeTools: "all"}))
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
		Expect(err.Error()).To(ContainSubstring("nativeTools"))
	})

	It("accepts declared native tools in sidecar mode", func() {
		_, err := create("cel-native-sidecar", harness(kubemootv1alpha1.HarnessSpec{
			Mode: kubemootv1alpha1.HarnessModeSidecar, Image: celImage, NativeTools: celDeclared,
		}))
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects a mode, protocol and native-tools value the operator does not know", func() {
		for name, h := range map[string]kubemootv1alpha1.HarnessSpec{
			"cel-bad-mode":     {Mode: "hybrid"},
			"cel-bad-protocol": {Protocol: "grpc"},
			"cel-bad-native":   {Mode: kubemootv1alpha1.HarnessModeSidecar, Image: "i", NativeTools: "some"},
			"cel-bad-port":     {Port: 70000},
		} {
			_, err := create(name, harness(h))
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%s: got %v", name, err)
		}
	})
})
