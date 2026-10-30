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

const sqliteEngine = string(aiv1alpha1.ArtifactQueryEngineSQLite)

// The apiserver defaults spec.artifacts.queryEngine to sqlite whether the artifacts
// object is absent, empty, or already carries the field; an explicit value is kept.
var _ = Describe("KubemootConfig artifacts.queryEngine default", func() {
	cases := []struct {
		name      string
		artifacts map[string]any // nil leaves spec.artifacts out
		want      string
	}{
		{"artifacts absent", nil, sqliteEngine},
		{"artifacts empty", map[string]any{}, sqliteEngine},
		{"none is explicit", map[string]any{"queryEngine": coreTestNone}, coreTestNone},
		{"duckdb is explicit", map[string]any{"queryEngine": duckdbEngine}, duckdbEngine},
	}

	for i, tc := range cases {
		It("resolves "+tc.name, func() {
			cfgObj := &unstructured.Unstructured{}
			cfgObj.SetGroupVersionKind(aiv1alpha1.GroupVersion.WithKind("KubemootConfig"))
			cfgObj.SetName(fmt.Sprintf("query-engine-default-%d", i))
			spec := map[string]any{}
			if tc.artifacts != nil {
				spec["artifacts"] = tc.artifacts
			}
			Expect(unstructured.SetNestedMap(cfgObj.Object, spec, "spec")).To(Succeed())
			Expect(k8sClient.Create(ctx, cfgObj)).To(Succeed())
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, cfgObj))).To(Succeed()) })

			got := &aiv1alpha1.KubemootConfig{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cfgObj), got)).To(Succeed())
			Expect(string(got.Spec.Artifacts.QueryEngine)).To(Equal(tc.want))
		})
	}
})
