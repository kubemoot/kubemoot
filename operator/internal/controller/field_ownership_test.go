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
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// userFieldManager stands in for the chart (helm) or kmctl that owns the spec.
const (
	userFieldManager = "helm"
	ownershipNS      = "default"
)

// userApply server-side applies a kubemoot.ai object as the user would: one
// spec field set to false, no forced ownership, so a conflicting owner fails it.
func userApply(ctx context.Context, kind, name string, spec map[string]interface{}) error {
	u := &unstructured.Unstructured{Object: map[string]interface{}{"spec": spec}}
	u.SetAPIVersion(aiv1alpha1.GroupVersion.String())
	u.SetKind(kind)
	u.SetName(name)
	u.SetNamespace(ownershipNS)
	return k8sClient.Patch(ctx, u, client.Apply, client.FieldOwner(userFieldManager))
}

// specFieldOwners lists the managers that own spec.<path> on the object. Each
// managedFields entry's FieldsV1 is a JSON tree keyed "f:<field>"; a manager owns
// the path when the tree contains it.
func specFieldOwners(obj client.Object, path ...string) []string {
	var owners []string
	for _, mf := range obj.GetManagedFields() {
		if mf.FieldsV1 != nil && fieldsContain(mf.FieldsV1.Raw, append([]string{"spec"}, path...)) {
			owners = append(owners, mf.Manager)
		}
	}
	return owners
}

func fieldsContain(raw []byte, path []string) bool {
	var node map[string]interface{}
	if err := json.Unmarshal(raw, &node); err != nil {
		return false
	}
	for _, p := range path {
		next, ok := node["f:"+p].(map[string]interface{})
		if !ok {
			return false
		}
		node = next
	}
	return true
}

var _ = Describe("User-set false on a default-true spec field", func() {
	const ns = ownershipNS
	ctx := context.Background()

	It("keeps MCPGateway adminUI false through reconcile and a re-apply", func() {
		const name = "gw-adminui-false"
		key := types.NamespacedName{Name: name, Namespace: ns}
		spec := map[string]interface{}{"implementation": string(aiv1alpha1.ImplementationKubemoot), "adminUI": false}
		Expect(userApply(ctx, "MCPGateway", name, spec)).To(Succeed())

		r := &MCPGatewayReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache()}
		req := reconcile.Request{NamespacedName: key}
		for range 2 { // finalizer, then Deployment + Service + status
			_, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		}

		Expect(userApply(ctx, "MCPGateway", name, spec)).To(Succeed(),
			"the user's re-apply must not conflict with the operator")

		got := &aiv1alpha1.MCPGateway{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Spec.AdminUI).NotTo(BeNil())
		Expect(*got.Spec.AdminUI).To(BeFalse(), "the operator must not flip adminUI back to true")
		Expect(got.Finalizers).To(ContainElement(mcpGatewayFinalizer))
		Expect(specFieldOwners(got, "adminUI")).To(ConsistOf(userFieldManager),
			"only the user may own spec.adminUI")

		Expect(got.Status.Endpoint).NotTo(BeEmpty(), "the reconcile must have reached the status update")
		Expect(got.Status.AdminEndpoint).To(BeEmpty(), "a disabled admin UI advertises no admin endpoint")

		Expect(k8sClient.Delete(ctx, got)).To(Succeed())
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			return k8sClient.Get(ctx, key, &aiv1alpha1.MCPGateway{}) != nil
		}).Should(BeTrue(), "the finalizer patch must release the gateway")
	})

	It("keeps Crew discussion.enabled false through reconcile and a re-apply", func() {
		const name = "crew-discussion-false"
		key := types.NamespacedName{Name: name, Namespace: ns}
		spec := map[string]interface{}{"description": "discussion off", "discussion": map[string]interface{}{"enabled": false}}
		Expect(userApply(ctx, "Crew", name, spec)).To(Succeed())

		r := &CrewReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache()}
		req := reconcile.Request{NamespacedName: key}
		for range 2 { // finalizer, then the crew's resources and status
			_, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		}

		Expect(userApply(ctx, "Crew", name, spec)).To(Succeed(),
			"the user's re-apply must not conflict with the operator")

		got := &aiv1alpha1.Crew{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Spec.Discussion).NotTo(BeNil())
		Expect(got.Spec.Discussion.IsEnabled()).To(BeFalse(), "the operator must not turn the discussion gateway back on")
		Expect(got.Finalizers).To(ContainElement(crewFinalizer))
		Expect(specFieldOwners(got, "discussion", "enabled")).To(ConsistOf(userFieldManager),
			"only the user may own spec.discussion.enabled")

		Expect(k8sClient.Delete(ctx, got)).To(Succeed())
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			return k8sClient.Get(ctx, key, &aiv1alpha1.Crew{}) != nil
		}).Should(BeTrue(), "the finalizer patch must release the crew")
	})
})
