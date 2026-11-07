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
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	bcNSOne          = "bc-one"
	bcNSTwo          = "bc-two"
	bcReadName       = "kubemoot-kubernetes-mcp-read"
	bcChartComponent = "homelab-pilot-crew"
)

var _ = Describe("Kubernetes MCP binding cleanup", func() {
	ctx := context.Background()
	var created []client.Object

	track := func(o client.Object) {
		Expect(k8sClient.Create(ctx, o)).To(Succeed())
		created = append(created, o)
	}
	namespace := func(name string) {
		// envtest has no namespace controller, so a namespace is never removed.
		err := k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})
		if !errors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}
	}
	exists := func(key types.NamespacedName, obj client.Object) bool {
		err := k8sClient.Get(ctx, key, obj)
		if err != nil {
			Expect(errors.IsNotFound(err)).To(BeTrue())
		}
		return err == nil
	}
	account := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "crew-kubernetes-mcp", Namespace: "bc-crew"}
	roleRef := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: kindClusterRole, Name: bcReadName}
	consentBinding := func(ns, name string, labels map[string]string) *rbacv1.RoleBinding {
		return &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
			Subjects:   []rbacv1.Subject{account},
			RoleRef:    roleRef,
		}
	}
	consentLabels := map[string]string{labelManagedBy: managedByValue, labelComponent: readConsentComponent}

	BeforeEach(func() {
		created = nil
		namespace(bcNSOne)
		namespace(bcNSTwo)
	})

	AfterEach(func() {
		for i := len(created) - 1; i >= 0; i-- {
			_ = k8sClient.Delete(ctx, created[i])
		}
		_ = k8sClient.Delete(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: legacyKubernetesBinding}})
		_ = k8sClient.Delete(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: readConsentNamespacesBinding}})
	})

	Describe("the bindings the namespace read-consent controller made", func() {
		It("removes every labeled RoleBinding in every namespace and the namespaces ClusterRoleBinding", func() {
			track(consentBinding(bcNSOne, bcReadName, consentLabels))
			track(consentBinding(bcNSTwo, bcReadName, consentLabels))
			track(consentBinding(bcNSTwo, "kubemoot-kubernetes-mcp-write", consentLabels))
			track(&rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: readConsentNamespacesBinding, Labels: consentLabels},
				Subjects:   []rbacv1.Subject{account},
				RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: kindClusterRole, Name: "kubemoot-kubernetes-mcp-namespaces"},
			})

			Expect(RemoveReadConsentBindings(ctx, k8sClient, k8sClient)).To(Succeed())

			for _, key := range []types.NamespacedName{
				{Namespace: bcNSOne, Name: bcReadName},
				{Namespace: bcNSTwo, Name: bcReadName},
				{Namespace: bcNSTwo, Name: "kubemoot-kubernetes-mcp-write"},
			} {
				Expect(exists(key, &rbacv1.RoleBinding{})).To(BeFalse(), key.String())
			}
			Expect(exists(types.NamespacedName{Name: readConsentNamespacesBinding}, &rbacv1.ClusterRoleBinding{})).To(BeFalse())
		})

		It("leaves bindings the crew chart made, even under the same name", func() {
			// The crew chart's binding for a release named "kubemoot" has the operator's old name
			// and carries the chart's labels, not the consent component.
			chartLabels := map[string]string{labelComponent: bcChartComponent, labelManagedBy: "Helm"}
			// The component label alone, from a third party, does not make a binding the operator's.
			foreign := map[string]string{labelComponent: readConsentComponent, labelManagedBy: "someone-else"}
			track(consentBinding(bcNSOne, "third-party-read", foreign))
			track(consentBinding(bcNSOne, bcReadName, chartLabels))
			track(consentBinding(bcNSTwo, "crew-kubernetes-mcp-read", nil))
			track(&rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: readConsentNamespacesBinding, Labels: chartLabels},
				Subjects:   []rbacv1.Subject{account},
				RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: kindClusterRole, Name: "view"},
			})

			Expect(RemoveReadConsentBindings(ctx, k8sClient, k8sClient)).To(Succeed())

			Expect(exists(types.NamespacedName{Namespace: bcNSOne, Name: bcReadName}, &rbacv1.RoleBinding{})).To(BeTrue())
			Expect(exists(types.NamespacedName{Namespace: bcNSOne, Name: "third-party-read"}, &rbacv1.RoleBinding{})).To(BeTrue())
			Expect(exists(types.NamespacedName{Namespace: bcNSTwo, Name: "crew-kubernetes-mcp-read"}, &rbacv1.RoleBinding{})).To(BeTrue())
			Expect(exists(types.NamespacedName{Name: readConsentNamespacesBinding}, &rbacv1.ClusterRoleBinding{})).To(BeTrue())
		})

		It("finds nothing to do on a second start", func() {
			track(consentBinding(bcNSOne, bcReadName, consentLabels))
			keep := consentBinding(bcNSTwo, "crew-kubernetes-mcp-read", nil)
			track(keep)
			Expect(RemoveReadConsentBindings(ctx, k8sClient, k8sClient)).To(Succeed())
			Expect(RemoveReadConsentBindings(ctx, k8sClient, k8sClient)).To(Succeed())
			Expect(exists(types.NamespacedName{Namespace: bcNSOne, Name: bcReadName}, &rbacv1.RoleBinding{})).To(BeFalse())
			Expect(exists(client.ObjectKeyFromObject(keep), &rbacv1.RoleBinding{})).To(BeTrue())
		})

		It("succeeds on a cluster that never had the controller", func() {
			Expect(RemoveReadConsentBindings(ctx, k8sClient, k8sClient)).To(Succeed())
		})
	})

	Describe("the cluster-wide binding earlier releases made", func() {
		legacy := func(role string, subjects ...rbacv1.Subject) {
			track(&rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: legacyKubernetesBinding},
				Subjects:   subjects,
				RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: kindClusterRole, Name: role},
			})
		}
		present := func() bool {
			return exists(types.NamespacedName{Name: legacyKubernetesBinding}, &rbacv1.ClusterRoleBinding{})
		}

		It("is removed at startup", func() {
			legacy(legacyKubernetesRole, rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: legacyKubernetesAccount, Namespace: defaultOperatorNamespace})
			Expect(RemoveLegacyKubernetesBinding(ctx, k8sClient, k8sClient)).To(Succeed())
			Expect(present()).To(BeFalse())
			// A second start finds nothing to do.
			Expect(RemoveLegacyKubernetesBinding(ctx, k8sClient, k8sClient)).To(Succeed())
		})

		It("is left alone when the name now points at something else", func() {
			legacy("view", rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "someone-else", Namespace: defaultOperatorNamespace})
			Expect(RemoveLegacyKubernetesBinding(ctx, k8sClient, k8sClient)).To(Succeed())
			Expect(present()).To(BeTrue())
		})
	})
})
