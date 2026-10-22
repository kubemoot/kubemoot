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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

var _ = Describe("Crew deletion cascade", func() {
	ctx := context.Background()
	const ns = "default"

	newAgent := func(name string, labels map[string]string) *aiv1alpha1.Agent {
		a := &aiv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
			Spec:       aiv1alpha1.AgentSpec{Capabilities: []string{"reasoning"}},
		}
		Expect(k8sClient.Create(ctx, a)).To(Succeed())
		return a
	}
	exists := func(name string) bool {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &aiv1alpha1.Agent{})
		return !errors.IsNotFound(err)
	}
	reconcileCrew := func(r *CrewReconciler, name string) {
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}})
		Expect(err).NotTo(HaveOccurred())
	}

	It("removes member Agents, keeps other crews' and unlabeled Agents, and owns members", func() {
		crew := &aiv1alpha1.Crew{
			ObjectMeta: metav1.ObjectMeta{Name: "cascade-crew", Namespace: ns},
			Spec:       aiv1alpha1.CrewSpec{Description: "member cascade"},
		}
		Expect(k8sClient.Create(ctx, crew)).To(Succeed())
		member := newAgent("cascade-member", map[string]string{labelCrew: "cascade-crew"})
		other := newAgent("cascade-other", map[string]string{labelCrew: "another-crew"})
		bare := newAgent("cascade-bare", nil)

		r := &CrewReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache()}
		reconcileCrew(r, "cascade-crew") // adds finalizer
		reconcileCrew(r, "cascade-crew") // adopts members

		got := &aiv1alpha1.Agent{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: member.Name, Namespace: ns}, got)).To(Succeed())
		Expect(got.OwnerReferences).To(HaveLen(1))
		Expect(got.OwnerReferences[0].Kind).To(Equal("Crew"))
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: other.Name, Namespace: ns}, got)).To(Succeed())
		Expect(got.OwnerReferences).To(BeEmpty())

		Expect(k8sClient.Delete(ctx, crew)).To(Succeed())
		reconcileCrew(r, "cascade-crew")

		Expect(exists(member.Name)).To(BeFalse(), "member agent should be deleted")
		Expect(exists(other.Name)).To(BeTrue(), "another crew's agent should survive")
		Expect(exists(bare.Name)).To(BeTrue(), "an agent without the crew label should survive")
	})

	It("removes pre-existing labeled Agents that have no owner reference", func() {
		crew := &aiv1alpha1.Crew{
			ObjectMeta: metav1.ObjectMeta{Name: "legacy-crew", Namespace: ns},
			Spec:       aiv1alpha1.CrewSpec{Description: "unowned member"},
		}
		Expect(k8sClient.Create(ctx, crew)).To(Succeed())
		legacy := newAgent("legacy-member", map[string]string{labelCrew: "legacy-crew"})

		r := &CrewReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache()}
		reconcileCrew(r, "legacy-crew") // adds finalizer only
		Expect(k8sClient.Delete(ctx, crew)).To(Succeed())
		reconcileCrew(r, "legacy-crew")

		Expect(exists(legacy.Name)).To(BeFalse(), "finalizer should delete the unowned member")
	})
})
