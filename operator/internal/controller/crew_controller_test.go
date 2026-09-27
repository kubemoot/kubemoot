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
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

var _ = Describe("Crew Controller", func() {
	const (
		crewName  = "test-crew"
		namespace = "default"
	)

	ctx := context.Background()

	newReconciler := func() *CrewReconciler {
		return &CrewReconciler{
			Client:      k8sClient,
			Scheme:      k8sClient.Scheme(),
			ConfigCache: NewConfigCache(),
		}
	}

	namespacedName := types.NamespacedName{
		Name:      crewName,
		Namespace: namespace,
	}

	Context("Finalizer management", func() {
		It("should add the crew finalizer on first reconciliation", func() {
			crew := &aiv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{
					Name:      crewName,
					Namespace: namespace,
				},
				Spec: aiv1alpha1.CrewSpec{
					Description: "test crew for finalizer",
				},
			}
			Expect(k8sClient.Create(ctx, crew)).To(Succeed())

			reconciler := newReconciler()
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Requeue).To(BeTrue(), "should requeue after adding finalizer")

			// Verify finalizer was added
			updated := &aiv1alpha1.Crew{}
			Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(updated, crewFinalizer)).To(BeTrue())

			// Cleanup
			updated.Finalizers = nil
			Expect(k8sClient.Update(ctx, updated)).To(Succeed())
			Expect(k8sClient.Delete(ctx, updated)).To(Succeed())
		})
	})

	Context("Deletion handling", func() {
		It("should clean up ClusterRole and ClusterRoleBinding on deletion", func() {
			crew := &aiv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "crew-rbac-test",
					Namespace: namespace,
				},
				Spec: aiv1alpha1.CrewSpec{
					Description: "crew for RBAC cleanup test",
				},
			}
			Expect(k8sClient.Create(ctx, crew)).To(Succeed())

			reconciler := newReconciler()

			// First reconcile adds finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "crew-rbac-test", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Create cluster-scoped RBAC resources that handleDeletion should clean up:
			// the namespaced name and the unscoped legacy name it replaces.
			rbacName := "crew-" + namespace + "-crew-rbac-test-discussion"
			legacyName := "crew-crew-rbac-test-discussion"
			for _, name := range []string{rbacName, legacyName} {
				createGatewayClusterRBAC(ctx, name, "crew-rbac-test-discussion", namespace)
			}

			// Mark the crew for deletion
			updated := &aiv1alpha1.Crew{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "crew-rbac-test", Namespace: namespace}, updated)).To(Succeed())
			Expect(k8sClient.Delete(ctx, updated)).To(Succeed())

			// Reconcile should handle the deletion
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "crew-rbac-test", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify ClusterRole was deleted
			err = k8sClient.Get(ctx, types.NamespacedName{Name: rbacName}, &rbacv1.ClusterRole{})
			Expect(errors.IsNotFound(err)).To(BeTrue(), "ClusterRole should be deleted")

			// Verify ClusterRoleBinding was deleted
			err = k8sClient.Get(ctx, types.NamespacedName{Name: rbacName}, &rbacv1.ClusterRoleBinding{})
			Expect(errors.IsNotFound(err)).To(BeTrue(), "ClusterRoleBinding should be deleted")

			// The legacy RBAC bound this crew's service account, so it goes too
			err = k8sClient.Get(ctx, types.NamespacedName{Name: legacyName}, &rbacv1.ClusterRoleBinding{})
			Expect(errors.IsNotFound(err)).To(BeTrue(), "legacy ClusterRoleBinding should be deleted")
		})

		It("should keep a legacy ClusterRoleBinding that binds another namespace's gateway", func() {
			crew := &aiv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{Name: "crew-rbac-foreign", Namespace: namespace},
				Spec:       aiv1alpha1.CrewSpec{Description: "crew whose legacy name is taken elsewhere"},
			}
			Expect(k8sClient.Create(ctx, crew)).To(Succeed())
			key := types.NamespacedName{Name: "crew-rbac-foreign", Namespace: namespace}
			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			legacyName := "crew-crew-rbac-foreign-discussion"
			createGatewayClusterRBAC(ctx, legacyName, "crew-rbac-foreign-discussion", "some-other-namespace")

			updated := &aiv1alpha1.Crew{}
			Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
			Expect(k8sClient.Delete(ctx, updated)).To(Succeed())
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: legacyName}, &rbacv1.ClusterRoleBinding{})).To(Succeed(),
				"another namespace's legacy binding must survive")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: legacyName}, &rbacv1.ClusterRole{})).To(Succeed(),
				"another namespace's legacy role must survive")
			Expect(k8sClient.Delete(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: legacyName}})).To(Succeed())
			Expect(k8sClient.Delete(ctx, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: legacyName}})).To(Succeed())
		})

		It("should NOT delete namespace when crew label is absent", func() {
			// Create a crew in the default namespace (which is NOT labeled with kubemoot.ai/crew)
			crew := &aiv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "crew-no-ns-delete",
					Namespace: namespace,
				},
				Spec: aiv1alpha1.CrewSpec{
					Description: "crew in shared namespace",
				},
			}
			Expect(k8sClient.Create(ctx, crew)).To(Succeed())

			reconciler := newReconciler()

			// First reconcile adds finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "crew-no-ns-delete", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Delete the crew
			updated := &aiv1alpha1.Crew{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "crew-no-ns-delete", Namespace: namespace}, updated)).To(Succeed())
			Expect(k8sClient.Delete(ctx, updated)).To(Succeed())

			// Reconcile handles deletion
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "crew-no-ns-delete", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify namespace still exists (default namespace should not be deleted)
			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, ns)).To(Succeed())
			Expect(ns.DeletionTimestamp.IsZero()).To(BeTrue(), "namespace without crew label must not be deleted")
		})

		It("should delete namespace when kubemoot.ai/crew label is present", func() {
			// Create a dedicated namespace with the crew label
			nsName := "crew-managed-ns-test"
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
					Labels: map[string]string{
						crewLabelKey:                   "crew-managed-delete",
						"app.kubernetes.io/managed-by": "kubemoot-operator",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())

			crew := &aiv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "crew-managed-delete",
					Namespace: nsName,
					Annotations: map[string]string{
						manageNamespaceAnno: "true",
					},
				},
				Spec: aiv1alpha1.CrewSpec{
					Description: "crew with managed namespace",
				},
			}
			Expect(k8sClient.Create(ctx, crew)).To(Succeed())

			reconciler := newReconciler()

			// First reconcile adds finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "crew-managed-delete", Namespace: nsName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Delete the crew
			updated := &aiv1alpha1.Crew{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "crew-managed-delete", Namespace: nsName}, updated)).To(Succeed())
			Expect(k8sClient.Delete(ctx, updated)).To(Succeed())

			// Reconcile handles deletion
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "crew-managed-delete", Namespace: nsName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify namespace has been marked for deletion
			deletedNs := &corev1.Namespace{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, deletedNs)
			if err == nil {
				Expect(deletedNs.DeletionTimestamp.IsZero()).To(BeFalse(), "labeled namespace should be marked for deletion")
			}
			// If NotFound, that also means deletion succeeded
		})
	})

	Context("Namespace labeling", func() {
		It("should label namespace when manage-namespace annotation is set", func() {
			nsName := "crew-label-ns-test"
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
				},
			}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())

			crew := &aiv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "crew-label-test",
					Namespace: nsName,
					Annotations: map[string]string{
						manageNamespaceAnno: "true",
					},
				},
				Spec: aiv1alpha1.CrewSpec{
					Description: "crew for namespace labeling test",
				},
			}
			Expect(k8sClient.Create(ctx, crew)).To(Succeed())

			reconciler := newReconciler()

			// First reconcile adds finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "crew-label-test", Namespace: nsName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile processes the crew logic (including namespace labeling)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "crew-label-test", Namespace: nsName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify namespace was labeled
			updatedNs := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, updatedNs)).To(Succeed())
			Expect(updatedNs.Labels[crewLabelKey]).To(Equal("crew-label-test"))
			Expect(updatedNs.Labels["app.kubernetes.io/managed-by"]).To(Equal("kubemoot-operator"))

			// Cleanup: remove finalizer and delete
			crewObj := &aiv1alpha1.Crew{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "crew-label-test", Namespace: nsName}, crewObj)).To(Succeed())
			crewObj.Finalizers = nil
			Expect(k8sClient.Update(ctx, crewObj)).To(Succeed())
			Expect(k8sClient.Delete(ctx, crewObj)).To(Succeed())
		})

		It("should NOT label namespace when manage-namespace annotation is missing", func() {
			nsName := "crew-nolabel-ns-test"
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
				},
			}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())

			crew := &aiv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "crew-nolabel-test",
					Namespace: nsName,
					// No manage-namespace annotation
				},
				Spec: aiv1alpha1.CrewSpec{
					Description: "crew without manage-namespace",
				},
			}
			Expect(k8sClient.Create(ctx, crew)).To(Succeed())

			reconciler := newReconciler()

			// First reconcile adds finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "crew-nolabel-test", Namespace: nsName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile processes the crew logic
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "crew-nolabel-test", Namespace: nsName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify namespace was NOT labeled
			updatedNs := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, updatedNs)).To(Succeed())
			Expect(updatedNs.Labels[crewLabelKey]).To(BeEmpty())

			// Cleanup
			crewObj := &aiv1alpha1.Crew{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "crew-nolabel-test", Namespace: nsName}, crewObj)).To(Succeed())
			crewObj.Finalizers = nil
			Expect(k8sClient.Update(ctx, crewObj)).To(Succeed())
			Expect(k8sClient.Delete(ctx, crewObj)).To(Succeed())
		})
	})

	Context("Revision record", func() {
		It("records a status revision, mirrors it on the namespace, and removes it on deletion", func() {
			const nsName = "crew-revisions-ns"
			const revCrew = "crew-rev-test"
			Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}})).To(Succeed())

			crew := &aiv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{
					Name:        revCrew,
					Namespace:   nsName,
					Annotations: crewForgeAnnotations(),
					Labels:      map[string]string{crewVersionLabel: revTestVersion},
				},
				Spec: aiv1alpha1.CrewSpec{Description: "crew for revision record test"},
			}
			Expect(k8sClient.Create(ctx, crew)).To(Succeed())

			key := types.NamespacedName{Name: revCrew, Namespace: nsName}
			reconciler := newReconciler()
			for range 2 {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
				Expect(err).NotTo(HaveOccurred())
			}

			updated := &aiv1alpha1.Crew{}
			Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
			Expect(updated.Status.Revisions).To(HaveLen(1))
			rev := updated.Status.Revisions[0]
			Expect(rev.Revision).To(Equal(revTestHash))
			Expect(rev.Source).To(Equal(revTestSource))
			Expect(rev.Owner).To(Equal(revTestOwner))
			Expect(rev.Channel).To(Equal("bundle"))
			Expect(rev.CrewVersion).To(Equal(revTestVersion))
			Expect(rev.DeployedAt).To(Equal(revTestDeployedAt))
			Expect(rev.ObservedAt.IsZero()).To(BeFalse())

			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, ns)).To(Succeed())
			var crews map[string]map[string]string
			Expect(json.Unmarshal([]byte(ns.Annotations[annoNamespaceCrews]), &crews)).To(Succeed())
			Expect(crews).To(HaveKeyWithValue(revCrew, map[string]string{
				"source": revTestSource, "owner": revTestOwner, "revision": revTestHash,
				"channel": "bundle", "crewVersion": revTestVersion, "deployedAt": revTestDeployedAt,
			}))

			// A redeploy with a new revision prepends a second entry.
			updated.Annotations[annoCrewForgeRevision] = "b1c2d3e-dirty"
			Expect(k8sClient.Update(ctx, updated)).To(Succeed())
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
			Expect(updated.Status.Revisions).To(HaveLen(2))
			Expect(updated.Status.Revisions[0].Revision).To(Equal("b1c2d3e-dirty"))

			Expect(k8sClient.Delete(ctx, updated)).To(Succeed())
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(errors.IsNotFound(k8sClient.Get(ctx, key, &aiv1alpha1.Crew{}))).To(BeTrue())

			ns = &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, ns)).To(Succeed())
			Expect(ns.Annotations).NotTo(HaveKey(annoNamespaceCrews))
		})
	})
})

// createGatewayClusterRBAC creates a discussion-gateway ClusterRole and a
// ClusterRoleBinding of one name, binding the service account saName in saNamespace.
func createGatewayClusterRBAC(ctx context.Context, name, saName, saNamespace string) {
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{"kubemoot.ai"}, Resources: []string{"agents"}, Verbs: []string{"get"}},
		},
	}
	Expect(k8sClient.Create(ctx, cr)).To(Succeed())
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: name},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: saName, Namespace: saNamespace}},
	}
	Expect(k8sClient.Create(ctx, crb)).To(Succeed())
}
