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
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	rcServerNS    = "rc-crew"
	rcServerSA    = "rc-kubernetes-mcp"
	rcServerName  = "rc-kubernetes-mcp"
	rcMCPImage    = "example.com/kubernetes-mcp:1"
	rcYes         = "yes"
	rcClusterRole = kindClusterRole
)

var _ = Describe("Kubernetes MCP read consent", func() {
	ctx := context.Background()
	var r *ReadConsentReconciler
	var created []client.Object

	track := func(o client.Object) {
		Expect(k8sClient.Create(ctx, o)).To(Succeed())
		created = append(created, o)
	}
	namespace := func(name string, labels map[string]string) {
		// envtest has no namespace controller, so a namespace is never removed; each spec uses
		// its own names and AfterEach clears the consent label.
		err := k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}})
		if !errors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}
	}
	server := func(name, sa string, caps ...string) *aiv1alpha1.MCPServer {
		s := &aiv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: rcServerNS},
			Spec: aiv1alpha1.MCPServerSpec{
				Image: rcMCPImage, Transport: "stdio", ServiceAccountName: sa, Capabilities: caps,
			},
		}
		track(s)
		return s
	}
	reconcileNS := func(name string) {
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
		Expect(err).NotTo(HaveOccurred())
	}
	reconcileCluster := func() { reconcileNS(readConsentCluster) }
	readBinding := func(ns string) (*rbacv1.RoleBinding, error) {
		b := &rbacv1.RoleBinding{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: kubernetesReadBinding, Namespace: ns}, b)
		return b, err
	}
	count := func(name string) *int32 {
		s := &aiv1alpha1.MCPServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: rcServerNS}, s)).To(Succeed())
		return s.Status.ReadConsentNamespaces
	}
	relabel := func(name string, labels map[string]string) {
		ns := &corev1.Namespace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, ns)).To(Succeed())
		ns.Labels = labels
		Expect(k8sClient.Update(ctx, ns)).To(Succeed())
	}

	BeforeEach(func() {
		r = &ReadConsentReconciler{Client: k8sClient}
		created = nil
		namespace(rcServerNS, nil)
	})

	AfterEach(func() {
		consenting := &corev1.NamespaceList{}
		Expect(k8sClient.List(ctx, consenting, client.MatchingLabels{labelReadConsent: valueTrue})).To(Succeed())
		for i := range consenting.Items {
			consenting.Items[i].Labels = nil
			Expect(k8sClient.Update(ctx, &consenting.Items[i])).To(Succeed())
		}
		for i := len(created) - 1; i >= 0; i-- {
			_ = k8sClient.Delete(ctx, created[i])
		}
		_ = k8sClient.Delete(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: kubernetesNamespacesBinding}})
		_ = k8sClient.Delete(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: legacyKubernetesBinding}})
	})

	It("binds the server account in a labeled namespace and removes the binding when the label goes", func() {
		server(rcServerName, rcServerSA, "kubernetes")
		namespace("rc-team-a", map[string]string{labelReadConsent: valueTrue})
		reconcileNS("rc-team-a")

		b, err := readBinding("rc-team-a")
		Expect(err).NotTo(HaveOccurred())
		Expect(b.RoleRef.Kind).To(Equal(rcClusterRole))
		Expect(b.RoleRef.Name).To(Equal(kubernetesReadRole))
		Expect(b.Subjects).To(Equal([]rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: rcServerSA, Namespace: rcServerNS}}))

		relabel("rc-team-a", nil)
		reconcileNS("rc-team-a")
		_, err = readBinding("rc-team-a")
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("does not bind a namespace whose label value is not true", func() {
		server(rcServerName, rcServerSA, "kubernetes")
		namespace("rc-false", map[string]string{labelReadConsent: "false"})
		reconcileNS("rc-false")
		_, err := readBinding("rc-false")
		Expect(errors.IsNotFound(err)).To(BeTrue())

		relabel("rc-false", map[string]string{labelReadConsent: rcYes})
		reconcileNS("rc-false")
		_, err = readBinding("rc-false")
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("grants nothing in a namespace being deleted and ignores one that is gone", func() {
		server(rcServerName, rcServerSA, "kubernetes")
		namespace("rc-doomed", map[string]string{labelReadConsent: valueTrue})
		reconcileNS("rc-doomed")
		_, err := readBinding("rc-doomed")
		Expect(err).NotTo(HaveOccurred())

		// envtest has no namespace controller, so the namespace stays Terminating.
		Expect(k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "rc-doomed"}})).To(Succeed())
		reconcileNS("rc-doomed")
		_, err = readBinding("rc-doomed")
		Expect(errors.IsNotFound(err)).To(BeTrue())

		reconcileNS("rc-never-existed")
	})

	It("reports zero consenting namespaces and binds nothing", func() {
		server(rcServerName, rcServerSA, "kubernetes")
		namespace("rc-quiet", nil)
		reconcileCluster()

		Expect(count(rcServerName)).NotTo(BeNil())
		Expect(*count(rcServerName)).To(BeZero())
		_, err := readBinding("rc-quiet")
		Expect(errors.IsNotFound(err)).To(BeTrue())
		// The server can still list namespaces.
		crb := &rbacv1.ClusterRoleBinding{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: kubernetesNamespacesBinding}, crb)).To(Succeed())
		Expect(crb.RoleRef.Name).To(Equal(kubernetesNamespacesRole))
		Expect(crb.Subjects).To(HaveLen(1))
	})

	It("counts consenting namespaces on the server status", func() {
		server(rcServerName, rcServerSA, "kubernetes")
		namespace("rc-one", map[string]string{labelReadConsent: valueTrue})
		namespace("rc-two", map[string]string{labelReadConsent: valueTrue})
		reconcileCluster()
		Expect(*count(rcServerName)).To(BeEquivalentTo(2))
		for _, ns := range []string{"rc-one", "rc-two"} {
			_, err := readBinding(ns)
			Expect(err).NotTo(HaveOccurred())
		}
	})

	It("binds every Kubernetes server account and skips servers without the capability or an account", func() {
		server(rcServerName, rcServerSA, "kubernetes")
		server("rc-second", "rc-second-sa", "kubernetes", "pods")
		server("rc-fetch", "rc-fetch-sa", "http")
		server("rc-default-account", "", "kubernetes")
		namespace("rc-team-b", map[string]string{labelReadConsent: valueTrue})
		reconcileCluster()

		b, err := readBinding("rc-team-b")
		Expect(err).NotTo(HaveOccurred())
		Expect(b.Subjects).To(Equal([]rbacv1.Subject{
			{Kind: rbacv1.ServiceAccountKind, Name: rcServerSA, Namespace: rcServerNS},
			{Kind: rbacv1.ServiceAccountKind, Name: "rc-second-sa", Namespace: rcServerNS},
		}))
		Expect(count("rc-fetch")).To(BeNil())
	})

	It("makes no binding for a crew without a Kubernetes MCP server", func() {
		server("rc-fetch", "rc-fetch-sa", "http")
		namespace("rc-team-c", map[string]string{labelReadConsent: valueTrue})
		reconcileCluster()
		reconcileNS("rc-team-c")

		_, err := readBinding("rc-team-c")
		Expect(errors.IsNotFound(err)).To(BeTrue())
		err = k8sClient.Get(ctx, types.NamespacedName{Name: kubernetesNamespacesBinding}, &rbacv1.ClusterRoleBinding{})
		Expect(errors.IsNotFound(err)).To(BeTrue())
		Expect(count("rc-fetch")).To(BeNil())
	})

	It("removes a binding left in a namespace that stopped consenting while the operator was down", func() {
		server(rcServerName, rcServerSA, "kubernetes")
		namespace("rc-stale", map[string]string{labelReadConsent: valueTrue})
		reconcileNS("rc-stale")
		relabel("rc-stale", nil)

		reconcileCluster()
		_, err := readBinding("rc-stale")
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("leaves a binding of the same name that the operator did not make", func() {
		server(rcServerName, rcServerSA, "kubernetes")
		namespace("rc-hand", nil)
		track(&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: kubernetesReadBinding, Namespace: "rc-hand"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: rcClusterRole, Name: "view"},
		})
		reconcileNS("rc-hand")
		_, err := readBinding("rc-hand")
		Expect(err).NotTo(HaveOccurred())
	})

	It("refuses to overwrite a binding of its name that it did not make", func() {
		server(rcServerName, rcServerSA, "kubernetes")
		namespace("rc-foreign", map[string]string{labelReadConsent: valueTrue})
		track(&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: kubernetesReadBinding, Namespace: "rc-foreign"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: rcClusterRole, Name: "view"},
		})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "rc-foreign"}})
		Expect(err).To(HaveOccurred())
		b, getErr := readBinding("rc-foreign")
		Expect(getErr).NotTo(HaveOccurred())
		Expect(b.RoleRef.Name).To(Equal("view"))
	})

	It("never binds the default account, named or not", func() {
		server(rcServerName, "default", "kubernetes")
		namespace("rc-default-sa", map[string]string{labelReadConsent: valueTrue})
		reconcileCluster()
		_, err := readBinding("rc-default-sa")
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("records an event when the count changes", func() {
		rec := events.NewFakeRecorder(5)
		r.Recorder = rec
		server(rcServerName, rcServerSA, "kubernetes")
		reconcileCluster()
		Expect(<-rec.Events).To(ContainSubstring(reasonNoReadConsent))
		namespace("rc-evt", map[string]string{labelReadConsent: valueTrue})
		reconcileNS("rc-evt")
		Expect(<-rec.Events).To(ContainSubstring(reasonReadConsent))
	})

	Describe("the cluster-wide binding earlier releases made", func() {
		legacy := func(role string, subjects ...rbacv1.Subject) {
			track(&rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: legacyKubernetesBinding},
				Subjects:   subjects,
				RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: rcClusterRole, Name: role},
			})
		}
		gone := func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{Name: legacyKubernetesBinding}, &rbacv1.ClusterRoleBinding{})
			return errors.IsNotFound(err)
		}

		It("is removed at startup", func() {
			legacy(legacyKubernetesRole, rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: legacyKubernetesAccount, Namespace: defaultOperatorNamespace})
			Expect(RemoveLegacyKubernetesBinding(ctx, k8sClient, k8sClient)).To(Succeed())
			Expect(gone()).To(BeTrue())
			// A second start finds nothing to do.
			Expect(RemoveLegacyKubernetesBinding(ctx, k8sClient, k8sClient)).To(Succeed())
		})

		It("is left alone when the name now points at something else", func() {
			legacy("view", rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "someone-else", Namespace: defaultOperatorNamespace})
			Expect(RemoveLegacyKubernetesBinding(ctx, k8sClient, k8sClient)).To(Succeed())
			Expect(gone()).To(BeFalse())
		})
	})
})

var _ = Describe("Kubernetes MCP read consent triggers", func() {
	It("treats only the value true on a live namespace as consent", func() {
		labeled := func(v string) *corev1.Namespace {
			return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{labelReadConsent: v}}}
		}
		Expect(consents(labeled(valueTrue))).To(BeTrue())
		Expect(consents(labeled("false"))).To(BeFalse())
		Expect(consents(labeled("True"))).To(BeFalse())
		Expect(consents(&corev1.Namespace{})).To(BeFalse())
		now := metav1.Now()
		terminating := labeled(valueTrue)
		terminating.DeletionTimestamp = &now
		Expect(consents(terminating)).To(BeFalse())
	})

	It("lists server accounts sorted and without repeats", func() {
		mk := func(ns, sa string) aiv1alpha1.MCPServer {
			return aiv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns},
				Spec:       aiv1alpha1.MCPServerSpec{ServiceAccountName: sa},
			}
		}
		got := serverSubjects([]aiv1alpha1.MCPServer{mk("b", "x"), mk("a", "y"), mk("b", "x"), mk("a", "")})
		Expect(got).To(Equal([]rbacv1.Subject{
			{Kind: rbacv1.ServiceAccountKind, Name: "y", Namespace: "a"},
			{Kind: rbacv1.ServiceAccountKind, Name: "x", Namespace: "b"},
		}))
	})
})

var _ = Describe("Kubernetes MCP read consent predicates", func() {
	ns := func(v string) *corev1.Namespace {
		return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{labelReadConsent: v}}}
	}
	srv := func(sa string, deleting bool, caps ...string) *aiv1alpha1.MCPServer {
		s := &aiv1alpha1.MCPServer{Spec: aiv1alpha1.MCPServerSpec{ServiceAccountName: sa, Capabilities: caps}}
		if deleting {
			now := metav1.Now()
			s.DeletionTimestamp = &now
		}
		return s
	}

	It("passes a namespace only when its consent changes", func() {
		p := consentLabelPredicate()
		Expect(p.Create(event.CreateEvent{Object: ns(valueTrue)})).To(BeTrue())
		Expect(p.Create(event.CreateEvent{Object: ns("false")})).To(BeFalse())
		Expect(p.Update(event.UpdateEvent{ObjectOld: ns("false"), ObjectNew: ns(valueTrue)})).To(BeTrue())
		Expect(p.Update(event.UpdateEvent{ObjectOld: ns(valueTrue), ObjectNew: ns("false")})).To(BeTrue())
		Expect(p.Update(event.UpdateEvent{ObjectOld: ns(valueTrue), ObjectNew: ns(valueTrue)})).To(BeFalse())
		Expect(p.Delete(event.DeleteEvent{Object: ns(valueTrue)})).To(BeFalse())
	})

	It("passes a Kubernetes server's lifecycle and account change, not a status write", func() {
		p := kubernetesServerPredicate()
		k := srv("a", false, capabilityKubernetes)
		Expect(p.Create(event.CreateEvent{Object: k})).To(BeTrue())
		Expect(p.Create(event.CreateEvent{Object: srv("a", false, "http")})).To(BeFalse())
		Expect(p.Delete(event.DeleteEvent{Object: k})).To(BeTrue())
		Expect(p.Update(event.UpdateEvent{ObjectOld: k, ObjectNew: srv("b", false, capabilityKubernetes)})).To(BeTrue())
		Expect(p.Update(event.UpdateEvent{ObjectOld: srv("a", false, "http"), ObjectNew: k})).To(BeTrue())
		Expect(p.Update(event.UpdateEvent{ObjectOld: k, ObjectNew: srv("a", true, capabilityKubernetes)})).To(BeTrue())
		Expect(p.Update(event.UpdateEvent{ObjectOld: k, ObjectNew: k.DeepCopy()})).To(BeFalse())
		Expect(p.Update(event.UpdateEvent{ObjectOld: srv("a", false, "http"), ObjectNew: srv("b", false, "http")})).To(BeFalse())
	})
})
