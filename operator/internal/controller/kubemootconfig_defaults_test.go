/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	queryEngineKey  = "queryEngine"
	legacyArtifacts = "artifacts"
	workspaceKey    = "workspace"
)

// spec.workspace.queryEngine and the old spec.artifacts.queryEngine are stored as written and
// never defaulted by the apiserver, so the operator can tell an unset engine (sqlite) from one
// the user chose.
var _ = Describe("KubemootConfig workspace.queryEngine", func() {
	cases := []struct {
		name          string
		spec          map[string]any
		wantWorkspace string
		wantArtifacts string
	}{
		{"nothing set", map[string]any{}, "", ""},
		{"workspace none", map[string]any{workspaceKey: map[string]any{queryEngineKey: coreTestNone}}, coreTestNone, ""},
		{"workspace duckdb", map[string]any{workspaceKey: map[string]any{queryEngineKey: duckdbEngine}}, duckdbEngine, ""},
		{"old artifacts duckdb", map[string]any{legacyArtifacts: map[string]any{queryEngineKey: duckdbEngine}}, "", duckdbEngine},
	}

	for i, tc := range cases {
		It("stores "+tc.name, func() {
			cfgObj := &unstructured.Unstructured{}
			cfgObj.SetGroupVersionKind(aiv1alpha1.GroupVersion.WithKind("KubemootConfig"))
			cfgObj.SetName(fmt.Sprintf("query-engine-default-%d", i))
			Expect(unstructured.SetNestedMap(cfgObj.Object, tc.spec, "spec")).To(Succeed())
			Expect(k8sClient.Create(ctx, cfgObj)).To(Succeed())
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, cfgObj))).To(Succeed()) })

			got := &aiv1alpha1.KubemootConfig{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cfgObj), got)).To(Succeed())
			workspace := ""
			if got.Spec.Workspace != nil {
				workspace = string(got.Spec.Workspace.QueryEngine)
			}
			Expect(workspace).To(Equal(tc.wantWorkspace))
			Expect(string(got.Spec.Artifacts.QueryEngine)).To(Equal(tc.wantArtifacts))
		})
	}

	It("refuses an engine that is not none, sqlite or duckdb", func() {
		cfgObj := &unstructured.Unstructured{}
		cfgObj.SetGroupVersionKind(aiv1alpha1.GroupVersion.WithKind("KubemootConfig"))
		cfgObj.SetName("query-engine-invalid")
		spec := map[string]any{workspaceKey: map[string]any{queryEngineKey: "postgres"}}
		Expect(unstructured.SetNestedMap(cfgObj.Object, spec, "spec")).To(Succeed())
		Expect(k8sClient.Create(ctx, cfgObj)).NotTo(Succeed())
	})
})

// spec.artifacts.perCallArtifacts was removed: every tool result is always stored as its own
// object. A KubemootConfig written before the removal (by Helm, Flux or kubectl) still carries the
// field; it is accepted and dropped, so an upgrade never rejects an existing object.
var _ = Describe("KubemootConfig with the removed artifacts.perCallArtifacts field", func() {
	cases := []struct {
		name      string
		artifacts map[string]any
	}{
		{"set true", map[string]any{"perCallArtifacts": true}},
		{"set false", map[string]any{"perCallArtifacts": false}},
	}

	for i, tc := range cases {
		It("accepts it when "+tc.name, func() {
			cfgObj := &unstructured.Unstructured{}
			cfgObj.SetGroupVersionKind(aiv1alpha1.GroupVersion.WithKind("KubemootConfig"))
			cfgObj.SetName(fmt.Sprintf("removed-per-call-artifacts-%d", i))
			Expect(unstructured.SetNestedMap(cfgObj.Object, map[string]any{legacyArtifacts: tc.artifacts}, "spec")).To(Succeed())
			Expect(k8sClient.Create(ctx, cfgObj)).To(Succeed())
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, cfgObj))).To(Succeed()) })

			got := &aiv1alpha1.KubemootConfig{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cfgObj), got)).To(Succeed())
		})
	}
})

// The apiserver defaults spec.coordinator.eventLog and resume to true, and leaves lease
// off, whether the coordinator object is absent, empty, or partly set; an explicit false
// is kept, because the fields are pointers.
var _ = Describe("KubemootConfig coordinator defaults", func() {
	flags := func(c *aiv1alpha1.CoordinatorConfig) [3]string {
		on := func(b *bool) string {
			if b == nil {
				return "not-set"
			}
			return fmt.Sprint(*b)
		}
		return [3]string{on(c.EventLog), on(c.Resume), fmt.Sprint(c.Lease)}
	}
	cases := []struct {
		name        string
		coordinator map[string]any // nil leaves spec.coordinator out
		want        [3]string      // eventLog, resume, lease
	}{
		{"coordinator absent", nil, [3]string{valueTrue, valueTrue, valueFalse}},
		{"coordinator empty", map[string]any{}, [3]string{valueTrue, valueTrue, valueFalse}},
		{"only lease set", map[string]any{"lease": true}, [3]string{valueTrue, valueTrue, valueTrue}},
		{"resume off keeps the log on", map[string]any{"resume": false}, [3]string{valueTrue, valueFalse, valueFalse}},
		{"both off", map[string]any{"eventLog": false, "resume": false}, [3]string{valueFalse, valueFalse, valueFalse}},
	}

	for i, tc := range cases {
		It("resolves "+tc.name, func() {
			cfgObj := &unstructured.Unstructured{}
			cfgObj.SetGroupVersionKind(aiv1alpha1.GroupVersion.WithKind("KubemootConfig"))
			cfgObj.SetName(fmt.Sprintf("coordinator-default-%d", i))
			spec := map[string]any{}
			if tc.coordinator != nil {
				spec["coordinator"] = tc.coordinator
			}
			Expect(unstructured.SetNestedMap(cfgObj.Object, spec, "spec")).To(Succeed())
			Expect(k8sClient.Create(ctx, cfgObj)).To(Succeed())
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, cfgObj))).To(Succeed()) })

			got := &aiv1alpha1.KubemootConfig{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cfgObj), got)).To(Succeed())
			Expect(got.Spec.Coordinator).NotTo(BeNil())
			Expect(flags(got.Spec.Coordinator)).To(Equal(tc.want))
		})
	}
})
