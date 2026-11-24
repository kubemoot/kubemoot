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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	tierCRDGroup = "kubemoot.ai"
	tierCRDSpec  = "spec"
)

// These specs run against the real CRD in envtest: the schema is where an unknown or
// empty tier is refused, before any controller sees the suite.
var _ = Describe("CrewFitnessSuite spec.tier", func() {
	const namespace = "default"
	gvk := schema.GroupVersionKind{Group: tierCRDGroup, Version: "v1alpha1", Kind: "CrewFitnessSuite"}
	ctx := context.Background()

	suite := func(name string, spec map[string]any) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{tierCRDSpec: spec}}
		u.SetGroupVersionKind(gvk)
		u.SetName(name)
		u.SetNamespace(namespace)
		return u
	}
	base := func() map[string]any { return map[string]any{"crewRef": "demo", "iterations": int64(1)} }
	with := func(k string, v any) map[string]any { s := base(); s[k] = v; return s }
	script := []any{map[string]any{"testRef": "a", "testContent": "DESCRIPTION a"}}

	DescribeTable("accepts the three tiers with no scripts",
		func(tier string) {
			u := suite("tier-ok-"+tier, with("tier", tier))
			Expect(k8sClient.Create(ctx, u)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, u) })
		},
		Entry("smoke", "smoke"), Entry("standard", "standard"), Entry("full", "full"),
	)

	DescribeTable("refuses",
		func(spec map[string]any, message string) {
			err := k8sClient.Create(ctx, suite("tier-bad", spec))
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), err.Error())
			Expect(err.Error()).To(ContainSubstring(message))
		},
		Entry("an unknown tier", with("tier", "weekly"), "Unsupported value"),
		Entry("an empty tier", with("tier", ""), "Unsupported value"),
		Entry("neither scripts nor a tier", base(), "spec.tier must name a tier"),
		Entry("an empty script list and no tier", with("scripts", []any{}), "spec.tier must name a tier"),
		Entry("a tier with a re-judge", func() map[string]any {
			s := with("tier", "smoke")
			s["rejudge"] = map[string]any{"suite": "old", "runId": "r1"}
			return s
		}(), "mutually exclusive"),
	)

	It("keeps accepting a suite that lists its scripts", func() {
		u := suite("tier-scripts-only", with("scripts", script))
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, u) })
	})

	It("refuses to change the tier after creation", func() {
		u := suite("tier-immutable", with("tier", "smoke"))
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, u) })

		Expect(unstructured.SetNestedField(u.Object, "full", tierCRDSpec, "tier")).To(Succeed())
		err := k8sClient.Update(ctx, u)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.tier is immutable"))
	})

	It("lets the operator write the resolved scripts beside the tier", func() {
		u := suite("tier-resolved", with("tier", "smoke"))
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, u) })

		Expect(unstructured.SetNestedSlice(u.Object, script, tierCRDSpec, "scripts")).To(Succeed())
		Expect(k8sClient.Update(ctx, u)).To(Succeed())
	})
})
