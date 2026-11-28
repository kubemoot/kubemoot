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
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	sigCRDVersion = "v1alpha1"
	sigFieldName  = "name"
	sigConcern    = "concern"
	sigNotBool    = "yes"
	sigPhases     = "phases"
	sigMeaning    = "meaning"
)

// These specs run against the real CRD in envtest: startsRecheck is an optional boolean
// on a signal, kept as declared and absent when not declared.
var _ = Describe("MootArchetype spec.signals startsRecheck", func() {
	gvk := schema.GroupVersionKind{Group: tierCRDGroup, Version: sigCRDVersion, Kind: "MootArchetype"}
	ctx := context.Background()

	archetype := func(name string, signal map[string]any) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{tierCRDSpec: map[string]any{
			sigPhases: []any{map[string]any{sigFieldName: "triaging"}},
			"signals": []any{signal},
			"recheck": map[string]any{"maxRounds": int64(1)},
		}}}
		u.SetGroupVersionKind(gvk)
		u.SetName(name)
		return u
	}

	stored := func(u *unstructured.Unstructured) map[string]any {
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, u) })
		got := &unstructured.Unstructured{}
		got.SetGroupVersionKind(gvk)
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: u.GetName()}, got)).To(Succeed())
		signals, _, _ := unstructured.NestedSlice(got.Object, tierCRDSpec, "signals")
		Expect(signals).To(HaveLen(1))
		return signals[0].(map[string]any)
	}

	It("keeps a concern that is addressed and starts a recheck", func() {
		s := stored(archetype("signal-concern-recheck", map[string]any{
			sigFieldName: sigConcern, sigMeaning: "a finding is wrong", "addressed": true, "startsRecheck": true}))
		Expect(s).To(HaveKeyWithValue("addressed", true))
		Expect(s).To(HaveKeyWithValue("startsRecheck", true))
	})

	It("leaves startsRecheck absent when a signal does not declare it", func() {
		s := stored(archetype("signal-no-recheck", map[string]any{sigFieldName: signalTestAgree, sigMeaning: "it stands"}))
		Expect(s).NotTo(HaveKey("startsRecheck"))
	})

	It("refuses a startsRecheck that is not a boolean", func() {
		err := k8sClient.Create(ctx, archetype("signal-bad-recheck", map[string]any{
			sigFieldName: sigConcern, sigMeaning: "x", "startsRecheck": sigNotBool}))
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), err.Error())
	})
})
