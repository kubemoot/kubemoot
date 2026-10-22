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
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

var _ = Describe("Crew deletion cascade", func() {
	ctx := context.Background()
	const (
		ns          = "default"
		twinCrew    = "twin-crew"
		failingCrew = "failing-crew"
	)

	newAgent := func(name string, labels map[string]string) *aiv1alpha1.Agent {
		a := &aiv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
			Spec:       aiv1alpha1.AgentSpec{Capabilities: []string{candReasoning}},
		}
		Expect(k8sClient.Create(ctx, a)).To(Succeed())
		return a
	}
	exists := func(name string) bool {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &aiv1alpha1.Agent{})
		return !errors.IsNotFound(err)
	}
	watchClient := func() client.WithWatch {
		c, err := client.NewWithWatch(cfg, client.Options{Scheme: k8sClient.Scheme()})
		Expect(err).NotTo(HaveOccurred())
		return c
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

	It("keeps a foreign ownerReference when adopting, and does not patch again", func() {
		crew := &aiv1alpha1.Crew{
			ObjectMeta: metav1.ObjectMeta{Name: "foreign-crew", Namespace: ns},
			Spec:       aiv1alpha1.CrewSpec{Description: "foreign owner"},
		}
		Expect(k8sClient.Create(ctx, crew)).To(Succeed())
		agent := &aiv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foreign-member", Namespace: ns,
				Labels: map[string]string{labelCrew: "foreign-crew"},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "v1", Kind: "ConfigMap", Name: "someone", UID: "11111111-2222-3333-4444-555555555555",
				}},
			},
			Spec: aiv1alpha1.AgentSpec{Capabilities: []string{candReasoning}},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		patches := 0
		counting := interceptor.NewClient(watchClient(), interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*aiv1alpha1.Agent); ok {
					patches++
				}
				return c.Patch(ctx, obj, p, opts...)
			},
		})
		r := &CrewReconciler{Client: counting, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache()}
		reconcileCrew(r, "foreign-crew") // finalizer
		reconcileCrew(r, "foreign-crew") // adopts
		Expect(patches).To(Equal(1))

		got := &aiv1alpha1.Agent{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: ns}, got)).To(Succeed())
		Expect(got.OwnerReferences).To(HaveLen(2))
		Expect(got.OwnerReferences[0].Kind).To(Equal("ConfigMap"))

		reconcileCrew(r, "foreign-crew")
		Expect(patches).To(Equal(1), "an already-owned agent must not be patched again")
	})

	It("does not touch Agents of a same-named Crew in another namespace", func() {
		const otherNS = "cascade-other-ns"
		err := k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: otherNS}})
		Expect(client.IgnoreAlreadyExists(err)).To(Succeed())

		crew := &aiv1alpha1.Crew{
			ObjectMeta: metav1.ObjectMeta{Name: twinCrew, Namespace: ns},
			Spec:       aiv1alpha1.CrewSpec{Description: "twin"},
		}
		Expect(k8sClient.Create(ctx, crew)).To(Succeed())
		mine := newAgent("twin-mine", map[string]string{labelCrew: twinCrew})
		theirs := &aiv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: "twin-theirs", Namespace: otherNS, Labels: map[string]string{labelCrew: twinCrew}},
			Spec:       aiv1alpha1.AgentSpec{Capabilities: []string{candReasoning}},
		}
		Expect(k8sClient.Create(ctx, theirs)).To(Succeed())

		r := &CrewReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache()}
		reconcileCrew(r, twinCrew)
		reconcileCrew(r, twinCrew)
		got := &aiv1alpha1.Agent{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: theirs.Name, Namespace: otherNS}, got)).To(Succeed())
		Expect(got.OwnerReferences).To(BeEmpty())

		Expect(k8sClient.Delete(ctx, crew)).To(Succeed())
		reconcileCrew(r, twinCrew)
		Expect(exists(mine.Name)).To(BeFalse())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: theirs.Name, Namespace: otherNS}, got)).To(Succeed())
	})

	It("keeps the finalizer when an Agent delete fails", func() {
		crew := &aiv1alpha1.Crew{
			ObjectMeta: metav1.ObjectMeta{Name: failingCrew, Namespace: ns},
			Spec:       aiv1alpha1.CrewSpec{Description: "delete failure"},
		}
		Expect(k8sClient.Create(ctx, crew)).To(Succeed())
		member := newAgent("failing-member", map[string]string{labelCrew: failingCrew})

		failing := interceptor.NewClient(watchClient(), interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if _, ok := obj.(*aiv1alpha1.Agent); ok {
					return fmt.Errorf("simulated delete failure")
				}
				return c.Delete(ctx, obj, opts...)
			},
		})
		r := &CrewReconciler{Client: failing, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache()}
		reconcileCrew(r, failingCrew)
		Expect(k8sClient.Delete(ctx, crew)).To(Succeed())
		res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: failingCrew, Namespace: ns}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))

		got := &aiv1alpha1.Crew{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: failingCrew, Namespace: ns}, got)).To(Succeed())
		Expect(got.Finalizers).To(ContainElement(crewFinalizer))
		Expect(exists(member.Name)).To(BeTrue())
	})
})

var _ = Describe("hasOwnerUID", func() {
	crew := &aiv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{UID: "crew-uid"}}

	It("matches only a reference with the Crew's UID", func() {
		agent := &aiv1alpha1.Agent{}
		Expect(hasOwnerUID(agent, crew)).To(BeFalse())
		agent.OwnerReferences = []metav1.OwnerReference{{UID: testMismatchName}}
		Expect(hasOwnerUID(agent, crew)).To(BeFalse())
		agent.OwnerReferences = append(agent.OwnerReferences, metav1.OwnerReference{UID: "crew-uid"})
		Expect(hasOwnerUID(agent, crew)).To(BeTrue())
	})
})
