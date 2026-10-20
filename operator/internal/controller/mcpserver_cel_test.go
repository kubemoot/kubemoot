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

// The MCPServer CRD rejects more than one of image, externalEndpoint, and
// kubemootImage at the API server, so the rule holds even where the admission
// webhook is not installed.
var _ = Describe("MCPServer image selection validation", func() {
	create := func(name string, spec kubemootv1alpha1.MCPServerSpec) error {
		ms := &kubemootv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       spec,
		}
		err := k8sClient.Create(ctx, ms)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, ms) })
		}
		return err
	}

	It("accepts kubemootImage alone", func() {
		Expect(create("cel-kubemoot-only", kubemootv1alpha1.MCPServerSpec{
			KubemootImage: kubemootv1alpha1.KubemootImageArtifactAccess,
		})).To(Succeed())
	})

	It("rejects kubemootImage with image", func() {
		err := create("cel-image-and-kubemoot", kubemootv1alpha1.MCPServerSpec{
			Image:         "registry/mcp:1",
			KubemootImage: kubemootv1alpha1.KubemootImageArtifactAccess,
		})
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
		Expect(err.Error()).To(ContainSubstring("mutually exclusive"))
	})

	It("rejects kubemootImage with externalEndpoint", func() {
		err := create("cel-external-and-kubemoot", kubemootv1alpha1.MCPServerSpec{
			ExternalEndpoint: "http://external:9000",
			KubemootImage:    kubemootv1alpha1.KubemootImageArtifactAccess,
		})
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
	})

	It("rejects a kubemootImage name the operator does not know", func() {
		err := create("cel-unknown-name", kubemootv1alpha1.MCPServerSpec{KubemootImage: unknownImage})
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
	})
})
