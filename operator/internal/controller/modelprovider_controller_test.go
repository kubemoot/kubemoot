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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

var _ = Describe("ModelProvider Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}
		modelprovider := &aiv1alpha1.ModelProvider{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind ModelProvider")
			err := k8sClient.Get(ctx, typeNamespacedName, modelprovider)
			if err != nil && errors.IsNotFound(err) {
				resource := &aiv1alpha1.ModelProvider{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: aiv1alpha1.ModelProviderSpec{
						Type:     aiv1alpha1.ProviderTypeOllama,
						Endpoint: "http://ollama.test:11434",
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &aiv1alpha1.ModelProvider{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance ModelProvider")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &ModelProviderReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying the resource still exists after reconciliation")
			reconciled := &aiv1alpha1.ModelProvider{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, reconciled)).To(Succeed())
			Expect(reconciled.Spec.Type).To(Equal(aiv1alpha1.ProviderTypeOllama))
			Expect(reconciled.Spec.Endpoint).To(Equal("http://ollama.test:11434"))
		})
	})
})
