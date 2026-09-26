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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

var _ = Describe("CrewFitness Controller", func() {
	const namespace = "default"

	ctx := context.Background()

	newReconciler := func() *CrewFitnessReconciler {
		return &CrewFitnessReconciler{
			Client:      k8sClient,
			Scheme:      k8sClient.Scheme(),
			ConfigCache: NewConfigCache(),
		}
	}

	// Helper: create a Crew with a discussion endpoint so startTest can proceed
	// past the Crew lookup. The endpoint is set via status, which requires the
	// Crew to exist first (envtest has no running controller to set it).
	ensureCrew := func(name string) {
		crew := &aiv1alpha1.Crew{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: aiv1alpha1.CrewSpec{
				Description: "crew for fitness test",
				Discussion:  &aiv1alpha1.DiscussionConfig{Enabled: true},
			},
		}
		err := k8sClient.Create(ctx, crew)
		if err != nil && !errors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}
		// Set discussion endpoint in status
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, crew)).To(Succeed())
		crew.Status.DiscussionEndpoint = "http://" + name + "-discussion." + namespace + ".svc:80/api/v1/discussions/" + name
		Expect(k8sClient.Status().Update(ctx, crew)).To(Succeed())
	}

	Context("testContent field", func() {
		It("should create a ConfigMap owned by the CrewFitness CR", func() {
			crewName := "fitness-cm-crew"
			cfName := "fitness-testcontent"
			ensureCrew(crewName)

			cf := &aiv1alpha1.CrewFitness{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cfName,
					Namespace: namespace,
				},
				Spec: aiv1alpha1.CrewFitnessSpec{
					CrewRef: crewName,
					TestRef: "basic-test",
					TestContent: `DESCRIPTION "basic fitness test"
ASSERT(response CONTAINS "hello")`,
				},
			}
			Expect(k8sClient.Create(ctx, cf)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: cfName, Namespace: namespace},
			})
			// startTest will fail at Job creation or RBAC but ConfigMap should already
			// be created by that point. Tolerate the error and check the ConfigMap.
			_ = err

			// Verify the operator-managed ConfigMap was created
			cmName := cfName + "-test"
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cmName, Namespace: namespace}, cm)).To(Succeed())

			// Verify it contains the test content under the correct key
			testKey := "basic-test.adl"
			Expect(cm.Data).To(HaveKey(testKey))
			Expect(cm.Data[testKey]).To(ContainSubstring("ASSERT"))

			// Verify owner reference points back to the CrewFitness CR
			Expect(cm.OwnerReferences).To(HaveLen(1))
			Expect(cm.OwnerReferences[0].Name).To(Equal(cfName))
			Expect(cm.OwnerReferences[0].Kind).To(Equal("CrewFitness"))

			// Cleanup
			Expect(k8sClient.Delete(ctx, cf)).To(Succeed())
		})
	})

	Context("configMapRef validation", func() {
		It("should succeed when referenced ConfigMap exists with the correct key", func() {
			crewName := "fitness-cmref-crew"
			cfName := "fitness-cmref-exists"
			ensureCrew(crewName)

			// Create the ConfigMap that the CrewFitness will reference
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "external-tests",
					Namespace: namespace,
				},
				Data: map[string]string{
					"smoke-test.adl": `DESCRIPTION "smoke test"
ASSERT(response CONTAINS "ok")`,
				},
			}
			err := k8sClient.Create(ctx, cm)
			if err != nil && !errors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}

			cf := &aiv1alpha1.CrewFitness{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cfName,
					Namespace: namespace,
				},
				Spec: aiv1alpha1.CrewFitnessSpec{
					CrewRef:      crewName,
					TestRef:      "smoke-test",
					ConfigMapRef: "external-tests",
				},
			}
			Expect(k8sClient.Create(ctx, cf)).To(Succeed())

			reconciler := newReconciler()
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: cfName, Namespace: namespace},
			})
			// Reconcile proceeds past ConfigMap validation. It may fail later
			// (e.g., Job creation) but should NOT set an error about missing ConfigMap.
			_ = err

			updated := &aiv1alpha1.CrewFitness{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cfName, Namespace: namespace}, updated)).To(Succeed())
			// If status has an error, it should NOT be about the ConfigMap
			if updated.Status.Phase == aiv1alpha1.CrewFitnessPhaseError {
				Expect(updated.Status.Error).NotTo(ContainSubstring("ConfigMap"))
				Expect(updated.Status.Error).NotTo(ContainSubstring("not found"))
			}

			// Cleanup
			Expect(k8sClient.Delete(ctx, cf)).To(Succeed())
		})

		It("should set error status when referenced ConfigMap does not exist", func() {
			crewName := "fitness-cmref-missing-crew"
			cfName := "fitness-cmref-missing"
			ensureCrew(crewName)

			cf := &aiv1alpha1.CrewFitness{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cfName,
					Namespace: namespace,
				},
				Spec: aiv1alpha1.CrewFitnessSpec{
					CrewRef:      crewName,
					TestRef:      "nonexistent-test",
					ConfigMapRef: "does-not-exist",
				},
			}
			Expect(k8sClient.Create(ctx, cf)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: cfName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify status was set to Error with a message about the missing ConfigMap
			updated := &aiv1alpha1.CrewFitness{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cfName, Namespace: namespace}, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(aiv1alpha1.CrewFitnessPhaseError))
			Expect(updated.Status.Error).To(ContainSubstring("does-not-exist"))
			Expect(updated.Status.Error).To(ContainSubstring("not found"))

			// Cleanup
			Expect(k8sClient.Delete(ctx, cf)).To(Succeed())
		})

		It("should set error status when ConfigMap exists but is missing the test key", func() {
			crewName := "fitness-cmref-nokey-crew"
			cfName := "fitness-cmref-nokey"
			ensureCrew(crewName)

			// Create a ConfigMap without the expected key
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "tests-wrong-key",
					Namespace: namespace,
				},
				Data: map[string]string{
					"other-test.adl": "some content",
				},
			}
			err := k8sClient.Create(ctx, cm)
			if err != nil && !errors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}

			cf := &aiv1alpha1.CrewFitness{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cfName,
					Namespace: namespace,
				},
				Spec: aiv1alpha1.CrewFitnessSpec{
					CrewRef:      crewName,
					TestRef:      "missing-key",
					ConfigMapRef: "tests-wrong-key",
				},
			}
			Expect(k8sClient.Create(ctx, cf)).To(Succeed())

			reconciler := newReconciler()
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: cfName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify status indicates the test key was not found
			updated := &aiv1alpha1.CrewFitness{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cfName, Namespace: namespace}, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(aiv1alpha1.CrewFitnessPhaseError))
			Expect(updated.Status.Error).To(ContainSubstring("missing-key"))
			Expect(updated.Status.Error).To(ContainSubstring("not found"))

			// Cleanup
			Expect(k8sClient.Delete(ctx, cf)).To(Succeed())
		})
	})

	Context("crew endpoint not ready", func() {
		It("should WAIT (not error) when the crew has no discussion endpoint yet", func() {
			// A crew applied alongside its fitness suite has no DiscussionEndpoint
			// for the first ~15s. The run must wait for it, not error instantly.
			crewName := "fitness-noendpoint-crew"
			cfName := "fitness-noendpoint"
			crew := &aiv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{Name: crewName, Namespace: namespace},
				Spec: aiv1alpha1.CrewSpec{
					Description: "crew with no endpoint yet",
					Discussion:  &aiv1alpha1.DiscussionConfig{Enabled: true},
				},
			}
			err := k8sClient.Create(ctx, crew)
			if err != nil && !errors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}
			// Deliberately do NOT set crew.Status.DiscussionEndpoint.

			cf := &aiv1alpha1.CrewFitness{
				ObjectMeta: metav1.ObjectMeta{Name: cfName, Namespace: namespace},
				Spec: aiv1alpha1.CrewFitnessSpec{
					CrewRef:     crewName,
					TestRef:     "smoke",
					TestContent: `DESCRIPTION "smoke"`,
				},
			}
			Expect(k8sClient.Create(ctx, cf)).To(Succeed())

			reconciler := newReconciler()
			res, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: cfName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			// It should requeue to wait, not error.
			Expect(res.RequeueAfter).To(Equal(endpointWaitRequeue))

			updated := &aiv1alpha1.CrewFitness{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cfName, Namespace: namespace}, updated)).To(Succeed())
			Expect(updated.Status.Phase).NotTo(Equal(aiv1alpha1.CrewFitnessPhaseError))

			Expect(k8sClient.Delete(ctx, cf)).To(Succeed())
		})

		It("should ERROR immediately when the referenced crew does not exist", func() {
			// A missing/misspelled crewRef is permanent - it must NOT take the
			// wait-for-endpoint path (which is only for a crew that exists but is
			// still coming up). It should error on the first reconcile.
			cfName := "fitness-nocrew"
			cf := &aiv1alpha1.CrewFitness{
				ObjectMeta: metav1.ObjectMeta{Name: cfName, Namespace: namespace},
				Spec: aiv1alpha1.CrewFitnessSpec{
					CrewRef:     "crew-that-does-not-exist",
					TestRef:     "smoke",
					TestContent: `DESCRIPTION "smoke"`,
				},
			}
			Expect(k8sClient.Create(ctx, cf)).To(Succeed())

			reconciler := newReconciler()
			res, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: cfName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			// Permanent error: no requeue, Phase=Error immediately.
			Expect(res.RequeueAfter).To(BeZero())

			updated := &aiv1alpha1.CrewFitness{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cfName, Namespace: namespace}, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(aiv1alpha1.CrewFitnessPhaseError))
			Expect(updated.Status.Error).To(ContainSubstring("not found"))

			Expect(k8sClient.Delete(ctx, cf)).To(Succeed())
		})
	})
})
