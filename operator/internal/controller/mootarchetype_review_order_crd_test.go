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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// These specs run against the real CRD in envtest: the schema is where an unknown
// review order is refused, and where an absent one is defaulted to together.
var _ = Describe("MootArchetype spec.review.order", func() {
	gvk := schema.GroupVersionKind{Group: tierCRDGroup, Version: "v1alpha1", Kind: "MootArchetype"}
	ctx := context.Background()

	archetype := func(name string, review map[string]any) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{tierCRDSpec: map[string]any{
			"phases": []any{map[string]any{"name": "triaging"}},
			"review": review,
		}}}
		u.SetGroupVersionKind(gvk)
		u.SetName(name)
		return u
	}

	DescribeTable("accepts",
		func(order string) {
			u := archetype("review-order-"+strings.ToLower(order), map[string]any{"order": order})
			Expect(k8sClient.Create(ctx, u)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, u) })
		},
		Entry("together", "together"), Entry("generalistsFirst", "generalistsFirst"),
	)

	It("defaults an absent order to together", func() {
		u := archetype("review-order-default", map[string]any{})
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, u) })
		got := &unstructured.Unstructured{}
		got.SetGroupVersionKind(gvk)
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: u.GetName()}, got)).To(Succeed())
		order, _, _ := unstructured.NestedString(got.Object, tierCRDSpec, "review", "order")
		Expect(order).To(Equal("together"))
	})

	DescribeTable("refuses",
		func(order string) {
			err := k8sClient.Create(ctx, archetype("review-order-bad", map[string]any{"order": order}))
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), err.Error())
			Expect(err.Error()).To(ContainSubstring("Unsupported value"))
		},
		Entry("an unknown order", "domainsFirst"),
		Entry("an empty order", ""),
		Entry("the wrong case", "generalistsfirst"),
	)
})
