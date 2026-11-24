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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const gqEnvNS = "gq-env-a"

// The Go query service of a namespace, against a real API server: the objects the operator
// builds are accepted, every RAGSource owns them, and the last one to go takes them away.
var _ = Describe("RAGSource Go query service", func() {
	var (
		reconciler *RAGSourceReconciler
		cache      *ConfigCache
		em         *aiv1alpha1.EmbeddingModel
	)

	setEngine := func(engine aiv1alpha1.QueryServiceEngine) {
		cache.Update(&aiv1alpha1.KubemootConfig{Spec: aiv1alpha1.KubemootConfigSpec{
			Images:       aiv1alpha1.ImageConfig{QueryServiceGo: gqImage},
			QueryService: &aiv1alpha1.QueryServiceSettings{Engine: engine},
		}})
	}

	reconcileSources := func() {
		var list aiv1alpha1.RAGSourceList
		Expect(k8sClient.List(ctx, &list, client.InNamespace(gqEnvNS))).To(Succeed())
		for i := range list.Items {
			reconciler.reconcileQueryService(ctx, &list.Items[i], em)
		}
	}

	BeforeEach(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: gqEnvNS}}
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, ns))).To(Succeed())

		em = gqEmbeddingModel(gqEnvNS)
		status := em.Status
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, em))).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(em), em)).To(Succeed())
		em.Status = status
		Expect(k8sClient.Status().Update(ctx, em)).To(Succeed())

		cache = NewConfigCache()
		setEngine(aiv1alpha1.QueryServiceEngineGo)
		reconciler = &RAGSourceReconciler{Client: k8sClient, Scheme: scheme.Scheme, ConfigCache: cache}

		for _, name := range []string{gqDocsA, gqDocsB, gqDocsC} {
			rs := gqSource(name, gqEnvNS)
			rs.UID = ""
			Expect(k8sClient.Create(ctx, rs)).To(Succeed())
		}
	})

	AfterEach(func() {
		var list aiv1alpha1.RAGSourceList
		Expect(k8sClient.List(ctx, &list, client.InNamespace(gqEnvNS))).To(Succeed())
		for i := range list.Items {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &list.Items[i]))).To(Succeed())
		}
		Expect(reconciler.reconcileQueryGroup(ctx, gqEnvNS)).To(Succeed())
		var deps appsv1.DeploymentList
		Expect(k8sClient.List(ctx, &deps, client.InNamespace(gqEnvNS))).To(Succeed())
		for i := range deps.Items {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &deps.Items[i]))).To(Succeed())
		}
	})

	It("runs one Deployment, one Service and one registry for three RAGSources", func() {
		reconcileSources()

		var deps appsv1.DeploymentList
		Expect(k8sClient.List(ctx, &deps, client.InNamespace(gqEnvNS))).To(Succeed())
		Expect(deps.Items).To(HaveLen(1))
		Expect(deps.Items[0].Name).To(Equal(queryGroupName(gqEnvNS)))
		Expect(deps.Items[0].OwnerReferences).To(HaveLen(3))
		for _, o := range deps.Items[0].OwnerReferences {
			Expect(o.UID).NotTo(BeEmpty())
			Expect(o.Controller).To(BeNil())
		}

		cm := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: gqEnvNS, Name: queryRegistryName(gqEnvNS)}, cm)).To(Succeed())
		Expect(parseRegistry(cm.Data[registryKey])).To(HaveLen(3))

		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: gqEnvNS, Name: queryGroupName(gqEnvNS)}, svc)).To(Succeed())
		Expect(svc.Spec.Ports[0].Port).To(Equal(int32(8000)))
		Expect(svc.Spec.Selector).To(Equal(queryGroupSelector(gqEnvNS)))

		var again appsv1.DeploymentList
		reconcileSources()
		Expect(k8sClient.List(ctx, &again, client.InNamespace(gqEnvNS))).To(Succeed())
		Expect(again.Items[0].ResourceVersion).To(Equal(deps.Items[0].ResourceVersion), "a second pass must change nothing")
	})

	It("moves to the Python engine and back, leaving one engine's objects at a time", func() {
		reconcileSources()
		setEngine(aiv1alpha1.QueryServiceEnginePython)
		reconcileSources()

		var deps appsv1.DeploymentList
		Expect(k8sClient.List(ctx, &deps, client.InNamespace(gqEnvNS))).To(Succeed())
		Expect(deps.Items).To(HaveLen(4), "the group stays until each RAGSource's own Deployment is available")

		setEngine(aiv1alpha1.QueryServiceEngineGo)
		reconcileSources()
		Expect(k8sClient.List(ctx, &deps, client.InNamespace(gqEnvNS))).To(Succeed())
		Expect(deps.Items).To(HaveLen(4), "nothing is removed before the other engine is available")
	})

	It("removes the group when the last RAGSource goes", func() {
		reconcileSources()
		var list aiv1alpha1.RAGSourceList
		Expect(k8sClient.List(ctx, &list, client.InNamespace(gqEnvNS))).To(Succeed())
		for i := range list.Items {
			Expect(k8sClient.Delete(ctx, &list.Items[i])).To(Succeed())
			Expect(reconciler.reconcileQueryGroup(ctx, gqEnvNS)).To(Succeed())
		}

		var deps appsv1.DeploymentList
		Expect(k8sClient.List(ctx, &deps, client.InNamespace(gqEnvNS))).To(Succeed())
		Expect(deps.Items).To(BeEmpty())
		err := k8sClient.Get(ctx, client.ObjectKey{Namespace: gqEnvNS, Name: queryRegistryName(gqEnvNS)}, &corev1.ConfigMap{})
		Expect(client.IgnoreNotFound(err)).To(Succeed())
		Expect(err).To(HaveOccurred())
	})
})

// spec.queryService.engine is python or go; the apiserver refuses any other engine, and a
// KubemootConfig that leaves it out reads as python.
var _ = Describe("KubemootConfig queryService.engine", func() {
	newConfig := func(name string, spec map[string]any) *unstructured.Unstructured {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(aiv1alpha1.GroupVersion.WithKind("KubemootConfig"))
		obj.SetName(name)
		Expect(unstructured.SetNestedMap(obj.Object, spec, "spec")).To(Succeed())
		return obj
	}

	for _, engine := range []string{"python", "go"} {
		It("accepts "+engine, func() {
			obj := newConfig("query-engine-"+engine, map[string]any{"queryService": map[string]any{"engine": engine}})
			Expect(k8sClient.Create(ctx, obj)).To(Succeed())
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, obj))).To(Succeed()) })

			got := &aiv1alpha1.KubemootConfig{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), got)).To(Succeed())
			Expect(string(got.Spec.QueryService.Engine)).To(Equal(engine))
		})
	}

	It("refuses an engine that is not python or go", func() {
		obj := newConfig("query-engine-rust", map[string]any{"queryService": map[string]any{"engine": "rust"}})
		Expect(k8sClient.Create(ctx, obj)).NotTo(Succeed())
	})

	It("reads as python when the field is left out", func() {
		obj := newConfig("query-engine-unset", map[string]any{})
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, obj))).To(Succeed()) })

		got := &aiv1alpha1.KubemootConfig{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), got)).To(Succeed())
		cache := NewConfigCache()
		cache.Update(got)
		Expect(cache.GetQueryServiceEngine()).To(Equal(aiv1alpha1.QueryServiceEnginePython))
	})
})
