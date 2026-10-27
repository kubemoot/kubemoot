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
	networkingv1 "k8s.io/api/networking/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/netpol"
)

const (
	npOperatorNS = "kubemoot"
	npSMTP       = "smtp.example.com"
	npOffNS      = "np-off"
	npMail       = "mail"
	npToggleNS   = "np-toggle"
	npDocs       = "docs"
	npHandWrote  = "km-hand-written"
)

func npConfig(enabled bool) netpol.Config {
	return netpol.Config{
		Enabled: enabled, Ingress: true, Egress: netpol.EgressDeclared, Cilium: netpol.CiliumAuto,
		OperatorNamespace: npOperatorNS,
		DNS:               netpol.PodRef{Namespace: testKubeSystemNS, Labels: map[string]string{"k8s-app": "kube-dns"}},
	}
}

func npSync(cfg netpol.Config, cilium bool) *NetPolicySync {
	return &NetPolicySync{
		Client: k8sClient, Scheme: k8sClient.Scheme(), Config: cfg,
		CiliumInstalled: func() bool { return cilium },
	}
}

func npGenerated(ctx context.Context, namespace string) map[string]networkingv1.NetworkPolicy {
	list := &networkingv1.NetworkPolicyList{}
	Expect(k8sClient.List(ctx, list, client.InNamespace(namespace),
		client.MatchingLabels{netpol.LabelGeneratedBy: netpol.GeneratedByValue})).To(Succeed())
	out := map[string]networkingv1.NetworkPolicy{}
	for _, p := range list.Items {
		out[p.Name] = p
	}
	return out
}

func npNamespace(ctx context.Context, name string) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{netpol.LabelCrew: name}}}
	Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, ns))).To(Succeed())
}

// npCrew creates a Crew, so the namespace holds one.
func npCrew(ctx context.Context, namespace, name string) *aiv1alpha1.Crew {
	crew := &aiv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	Expect(k8sClient.Create(ctx, crew)).To(Succeed())
	return crew
}

func npReconcileNamespace(ctx context.Context, name string) {
	nsr := &NamespaceReconciler{Client: k8sClient, ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(true), false)}
	_, err := nsr.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
	Expect(err).NotTo(HaveOccurred())
}

func npHandWritten(ctx context.Context, namespace, name string) {
	Expect(k8sClient.Create(ctx, &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"hand": "written"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	})).To(Succeed())
}

func npMCPServer(ctx context.Context, namespace, name string, egress *aiv1alpha1.EgressRules) *aiv1alpha1.MCPServer {
	s := &aiv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       aiv1alpha1.MCPServerSpec{Image: specImage, Port: 3000, Egress: egress},
	}
	Expect(k8sClient.Create(ctx, s)).To(Succeed())
	return s
}

func npReconcileMCPServer(ctx context.Context, r *MCPServerReconciler, namespace, name string) {
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}
	for i := 0; i < 2; i++ { // the first pass adds the finalizer
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
	}
}

func npOwnedBy(obj client.Object, kind, name string) {
	refs := obj.GetOwnerReferences()
	Expect(refs).To(HaveLen(1))
	Expect(refs[0].Kind).To(Equal(kind))
	Expect(refs[0].Name).To(Equal(name))
	Expect(*refs[0].Controller).To(BeTrue())
	Expect(refs[0].UID).NotTo(BeEmpty())
}

var _ = Describe("Generated network policies", func() {
	ctx := context.Background()

	newMCPReconciler := func(sync *NetPolicySync) *MCPServerReconciler {
		return &MCPServerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache(), NetPol: sync}
	}

	Context("with the flag off", func() {
		It("creates nothing and leaves a hand-written policy alone", func() {
			npNamespace(ctx, npOffNS)
			npHandWritten(ctx, npOffNS, npHandWrote)
			npMCPServer(ctx, npOffNS, npMail, &aiv1alpha1.EgressRules{AllowedHosts: []string{npSMTP}})
			npReconcileMCPServer(ctx, newMCPReconciler(npSync(npConfig(false), false)), npOffNS, npMail)

			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: npOffNS}, ns)).To(Succeed())
			nsr := &NamespaceReconciler{Client: k8sClient, ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(false), false)}
			_, err := nsr.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: npOffNS}})
			Expect(err).NotTo(HaveOccurred())

			Expect(npGenerated(ctx, npOffNS)).To(BeEmpty())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: npOffNS, Name: npHandWrote}, &networkingv1.NetworkPolicy{})).To(Succeed())
			got := &aiv1alpha1.MCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: npOffNS, Name: npMail}, got)).To(Succeed())
			Expect(meta.FindStatusCondition(got.Status.Conditions, netpol.ConditionEgressEnforced)).To(BeNil())
		})
	})

	Context("with the flag on", func() {
		It("creates the namespace baseline, owned by the namespace", func() {
			npNamespace(ctx, "np-base")
			npCrew(ctx, "np-base", "base")
			nsr := &NamespaceReconciler{Client: k8sClient, ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(true), false)}
			_, err := nsr.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "np-base"}})
			Expect(err).NotTo(HaveOccurred())

			policies := npGenerated(ctx, "np-base")
			Expect(policies).To(HaveLen(2))
			for _, name := range []string{"km-default-deny", "km-crew-internal"} {
				p := policies[name]
				npOwnedBy(&p, "Namespace", "np-base")
			}
		})

		It("leaves the operator namespace to the chart", func() {
			npNamespace(ctx, npOperatorNS)
			nsr := &NamespaceReconciler{Client: k8sClient, ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(true), false)}
			_, err := nsr.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: npOperatorNS}})
			Expect(err).NotTo(HaveOccurred())
			Expect(npGenerated(ctx, npOperatorNS)).To(BeEmpty())
		})

		It("creates ingress and the egress fallback for an MCPServer and reports EgressEnforced=False", func() {
			npNamespace(ctx, "np-mcp")
			npMCPServer(ctx, "np-mcp", npMail, &aiv1alpha1.EgressRules{AllowedHosts: []string{npSMTP}, Ports: []int32{587}})
			npReconcileMCPServer(ctx, newMCPReconciler(npSync(npConfig(true), false)), "np-mcp", npMail)

			policies := npGenerated(ctx, "np-mcp")
			Expect(policies).To(HaveLen(2))
			ingress := policies["km-mcp-mail-ingress"]
			npOwnedBy(&ingress, "MCPServer", npMail)
			Expect(ingress.Spec.Ingress[0].Ports[0].Port.IntVal).To(Equal(int32(3000)))
			egress := policies["km-mcp-mail-egress"]
			npOwnedBy(&egress, "MCPServer", npMail)
			Expect(egress.Spec.PolicyTypes).To(ConsistOf(networkingv1.PolicyTypeEgress))

			got := &aiv1alpha1.MCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "np-mcp", Name: npMail}, got)).To(Succeed())
			cond := meta.FindStatusCondition(got.Status.Conditions, netpol.ConditionEgressEnforced)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(netpol.ReasonFQDNNotSupported))
		})

		It("creates no egress policy for an MCPServer without spec.egress, and no condition", func() {
			npNamespace(ctx, "np-plain")
			npMCPServer(ctx, "np-plain", npDocs, nil)
			npReconcileMCPServer(ctx, newMCPReconciler(npSync(npConfig(true), false)), "np-plain", npDocs)

			Expect(npGenerated(ctx, "np-plain")).To(HaveKey("km-mcp-docs-ingress"))
			Expect(npGenerated(ctx, "np-plain")).To(HaveLen(1))
			got := &aiv1alpha1.MCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "np-plain", Name: npDocs}, got)).To(Succeed())
			Expect(meta.FindStatusCondition(got.Status.Conditions, netpol.ConditionEgressEnforced)).To(BeNil())
		})

		It("creates no policy for an external MCPServer", func() {
			npNamespace(ctx, "np-ext")
			Expect(k8sClient.Create(ctx, &aiv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "remote", Namespace: "np-ext"},
				Spec:       aiv1alpha1.MCPServerSpec{ExternalEndpoint: "http://remote.example.com:9000"},
			})).To(Succeed())
			npReconcileMCPServer(ctx, newMCPReconciler(npSync(npConfig(true), false)), "np-ext", "remote")
			Expect(npGenerated(ctx, "np-ext")).To(BeEmpty())
		})

		It("does not update an unchanged policy and corrects drift", func() {
			npNamespace(ctx, "np-drift")
			npMCPServer(ctx, "np-drift", npDocs, nil)
			r := newMCPReconciler(npSync(npConfig(true), false))
			npReconcileMCPServer(ctx, r, "np-drift", npDocs)
			before := npGenerated(ctx, "np-drift")["km-mcp-docs-ingress"]

			npReconcileMCPServer(ctx, r, "np-drift", npDocs)
			same := npGenerated(ctx, "np-drift")["km-mcp-docs-ingress"]
			Expect(same.ResourceVersion).To(Equal(before.ResourceVersion), "a second reconcile must not write")

			port := intstr.FromInt32(9999)
			drifted := same.DeepCopy()
			drifted.Spec.Ingress[0].Ports[0].Port = &port
			Expect(k8sClient.Update(ctx, drifted)).To(Succeed())
			npReconcileMCPServer(ctx, r, "np-drift", npDocs)
			fixed := npGenerated(ctx, "np-drift")["km-mcp-docs-ingress"]
			Expect(fixed.Spec.Ingress[0].Ports[0].Port.IntVal).To(Equal(int32(3000)))
		})

		It("refuses to overwrite a hand-written policy of the same name", func() {
			npNamespace(ctx, "np-clash")
			npCrew(ctx, "np-clash", "clash")
			npHandWritten(ctx, "np-clash", "km-default-deny")
			nsr := &NamespaceReconciler{Client: k8sClient, ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(true), false)}
			_, err := nsr.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "np-clash"}})
			Expect(err).To(HaveOccurred())
			p := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "np-clash", Name: "km-default-deny"}, p)).To(Succeed())
			Expect(p.Spec.PodSelector.MatchLabels).To(HaveKeyWithValue("hand", "written"))
		})
	})

	Context("when owners collide", func() {
		It("refuses to overwrite a generated object that another owner controls", func() {
			npNamespace(ctx, "np-collide")
			npMCPServer(ctx, "np-collide", npMail, nil)
			r := newMCPReconciler(npSync(npConfig(true), false))
			npReconcileMCPServer(ctx, r, "np-collide", npMail)
			other := npMCPServer(ctx, "np-collide", "other", nil)

			stolen := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "km-mcp-mail-ingress", Namespace: "np-collide"}}
			Expect(npSync(npConfig(true), false).upsert(ctx, other, stolen)).NotTo(Succeed())
			kept := npGenerated(ctx, "np-collide")["km-mcp-mail-ingress"]
			npOwnedBy(&kept, "MCPServer", npMail)
		})
	})

	Context("when a namespace loses the crew label", func() {
		It("removes the baseline", func() {
			npNamespace(ctx, "np-unlabel")
			npCrew(ctx, "np-unlabel", "unlabel")
			nsr := &NamespaceReconciler{Client: k8sClient, ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(true), false)}
			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "np-unlabel"}}
			_, err := nsr.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(npGenerated(ctx, "np-unlabel")).To(HaveLen(2))

			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, req.NamespacedName, ns)).To(Succeed())
			ns.Labels = nil
			Expect(k8sClient.Update(ctx, ns)).To(Succeed())
			_, err = nsr.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(npGenerated(ctx, "np-unlabel")).To(BeEmpty())
		})
	})

	Context("which namespaces count as crew namespaces", func() {
		const operatorSet = "np-stale-operator"

		labeledBy := func(ctx context.Context, name string, labels map[string]string) {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		}

		crewLabels := func(name string) map[string]string {
			return map[string]string{netpol.LabelCrew: name, labelManagedBy: managedByValue}
		}

		It("generates nothing in a labeled namespace that holds no Crew", func() {
			labeledBy(ctx, "np-nocrew", map[string]string{netpol.LabelCrew: "gone"})
			npHandWritten(ctx, "np-nocrew", npHandWrote)
			npReconcileNamespace(ctx, "np-nocrew")

			Expect(npGenerated(ctx, "np-nocrew")).To(BeEmpty())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "np-nocrew", Name: npHandWrote}, &networkingv1.NetworkPolicy{})).To(Succeed())
		})

		It("clears a crew label the operator set when no Crew is left, and keeps one a person set", func() {
			labeledBy(ctx, operatorSet, crewLabels("demo"))
			labeledBy(ctx, "np-stale-person", map[string]string{netpol.LabelCrew: "demo"})
			npReconcileNamespace(ctx, operatorSet)
			npReconcileNamespace(ctx, "np-stale-person")

			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: operatorSet}, ns)).To(Succeed())
			Expect(ns.Labels).NotTo(HaveKey(netpol.LabelCrew))
			Expect(ns.Labels).NotTo(HaveKey(labelManagedBy))
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "np-stale-person"}, ns)).To(Succeed())
			Expect(ns.Labels).To(HaveKey(netpol.LabelCrew))
		})

		It("leaves the crew label alone while the flag is off", func() {
			labeledBy(ctx, "np-flag-off-label", crewLabels("demo"))
			nsr := &NamespaceReconciler{Client: k8sClient, ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(false), false)}
			_, err := nsr.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "np-flag-off-label"}})
			Expect(err).NotTo(HaveOccurred())

			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "np-flag-off-label"}, ns)).To(Succeed())
			Expect(ns.Labels).To(HaveKey(netpol.LabelCrew))
		})

		It("keeps the label when the API server shows a Crew the cache has not seen yet", func() {
			labeledBy(ctx, "np-lag", crewLabels("lag"))
			npCrew(ctx, "np-lag", "lag")
			nsr := &NamespaceReconciler{
				Client: noCrewsSeen{k8sClient}, APIReader: k8sClient,
				ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(true), false),
			}
			_, err := nsr.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "np-lag"}})
			Expect(err).NotTo(HaveOccurred())

			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "np-lag"}, ns)).To(Succeed())
			Expect(ns.Labels).To(HaveKey(netpol.LabelCrew))
		})

		It("keeps the crew label the operator set while a Crew exists", func() {
			labeledBy(ctx, "np-keep", crewLabels("keep"))
			npCrew(ctx, "np-keep", "keep")
			npReconcileNamespace(ctx, "np-keep")

			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "np-keep"}, ns)).To(Succeed())
			Expect(ns.Labels).To(HaveKey(netpol.LabelCrew))
		})

		It("generates the set when a Crew appears and removes it when the last Crew is deleted", func() {
			labeledBy(ctx, "np-life", map[string]string{netpol.LabelCrew: "first"})
			npHandWritten(ctx, "np-life", npHandWrote)
			npReconcileNamespace(ctx, "np-life")
			Expect(npGenerated(ctx, "np-life")).To(BeEmpty())

			first := npCrew(ctx, "np-life", "first")
			second := npCrew(ctx, "np-life", "second")
			npReconcileNamespace(ctx, "np-life")
			Expect(npGenerated(ctx, "np-life")).To(HaveKey("km-default-deny"))
			Expect(npGenerated(ctx, "np-life")).To(HaveKey("km-crew-internal"))

			Expect(k8sClient.Delete(ctx, first)).To(Succeed())
			npReconcileNamespace(ctx, "np-life")
			Expect(npGenerated(ctx, "np-life")).To(HaveLen(2), "one Crew is still there")

			Expect(k8sClient.Delete(ctx, second)).To(Succeed())
			npReconcileNamespace(ctx, "np-life")
			Expect(npGenerated(ctx, "np-life")).To(BeEmpty())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "np-life", Name: npHandWrote}, &networkingv1.NetworkPolicy{})).To(Succeed())
		})

		It("does not give an unlabeled namespace with a Crew a default-deny", func() {
			Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "np-shared"}})).To(Succeed())
			npCrew(ctx, "np-shared", "shared")
			npReconcileNamespace(ctx, "np-shared")
			Expect(npGenerated(ctx, "np-shared")).To(BeEmpty())
		})

		It("maps a Crew to its namespace and passes only creation and deletion", func() {
			crew := &aiv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "np-map"}}
			Expect(crewNamespaceRequests(crew)).To(Equal([]reconcile.Request{{NamespacedName: types.NamespacedName{Name: "np-map"}}}))
			pred := crewLifecyclePredicate()
			Expect(pred.Create(event.CreateEvent{Object: crew})).To(BeTrue())
			Expect(pred.Delete(event.DeleteEvent{Object: crew})).To(BeTrue())
			Expect(pred.Update(event.UpdateEvent{ObjectOld: crew, ObjectNew: crew})).To(BeFalse())
		})

		It("lets a shared tool server admit the gateways of namespaces that hold a Crew, and no others", func() {
			npNamespace(ctx, npOperatorNS)
			labeledBy(ctx, "np-x-crew", map[string]string{netpol.LabelCrew: "x"})
			labeledBy(ctx, "np-x-empty", map[string]string{netpol.LabelCrew: "gone"})
			crew := npCrew(ctx, "np-x-crew", "x")
			npMCPServer(ctx, npOperatorNS, "shared-tool", nil)
			r := &MCPServerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(true), false)}
			npReconcileMCPServer(ctx, r, npOperatorNS, "shared-tool")

			gatewayNamespaces := func() []string {
				p := npGenerated(ctx, npOperatorNS)["km-mcp-shared-tool-ingress"]
				Expect(p.Spec.Ingress).NotTo(BeEmpty())
				for _, peer := range p.Spec.Ingress[0].From {
					if peer.NamespaceSelector != nil && len(peer.NamespaceSelector.MatchExpressions) > 0 {
						return peer.NamespaceSelector.MatchExpressions[0].Values
					}
				}
				return nil
			}
			Expect(gatewayNamespaces()).To(ContainElement("np-x-crew"))
			Expect(gatewayNamespaces()).NotTo(ContainElement("np-x-empty"))

			Expect(k8sClient.Delete(ctx, crew)).To(Succeed())
			npReconcileMCPServer(ctx, r, npOperatorNS, "shared-tool")
			Expect(gatewayNamespaces()).NotTo(ContainElement("np-x-crew"))

			Expect(sharedMCPServerRequests(ctx, k8sClient)).To(ContainElement(
				reconcile.Request{NamespacedName: types.NamespacedName{Namespace: npOperatorNS, Name: "shared-tool"}}))
		})
	})

	Context("turning the flag off", func() {
		It("deletes only the generated objects", func() {
			npNamespace(ctx, npToggleNS)
			npCrew(ctx, npToggleNS, "toggle")
			npHandWritten(ctx, npToggleNS, npHandWrote)
			npMCPServer(ctx, npToggleNS, npMail, &aiv1alpha1.EgressRules{AllowedCIDRs: []aiv1alpha1.EgressCIDR{{CIDR: npCIDR}}})
			nsr := &NamespaceReconciler{Client: k8sClient, ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(true), false)}
			nsReq := reconcile.Request{NamespacedName: types.NamespacedName{Name: npToggleNS}}
			_, err := nsr.Reconcile(ctx, nsReq)
			Expect(err).NotTo(HaveOccurred())
			npReconcileMCPServer(ctx, newMCPReconciler(npSync(npConfig(true), false)), npToggleNS, npMail)
			Expect(npGenerated(ctx, npToggleNS)).To(HaveLen(4))

			nsr.NetPol = npSync(npConfig(false), false)
			_, err = nsr.Reconcile(ctx, nsReq)
			Expect(err).NotTo(HaveOccurred())
			npReconcileMCPServer(ctx, newMCPReconciler(npSync(npConfig(false), false)), npToggleNS, npMail)

			Expect(npGenerated(ctx, npToggleNS)).To(BeEmpty())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: npToggleNS, Name: npHandWrote}, &networkingv1.NetworkPolicy{})).To(Succeed())
			got := &aiv1alpha1.MCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: npToggleNS, Name: npMail}, got)).To(Succeed())
			Expect(meta.FindStatusCondition(got.Status.Conditions, netpol.ConditionEgressEnforced)).To(BeNil())
		})

		It("keeps a labeled object that another owner controls", func() {
			npNamespace(ctx, "np-other")
			npMCPServer(ctx, "np-other", npMail, nil)
			npReconcileMCPServer(ctx, newMCPReconciler(npSync(npConfig(true), false)), "np-other", npMail)
			other := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
				Name: "km-other-owner", Namespace: "np-other",
				Labels: map[string]string{netpol.LabelGeneratedBy: netpol.GeneratedByValue},
			}, Spec: networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}}}
			Expect(k8sClient.Create(ctx, other)).To(Succeed())

			npReconcileMCPServer(ctx, newMCPReconciler(npSync(npConfig(false), false)), "np-other", npMail)
			Expect(npGenerated(ctx, "np-other")).To(HaveKey("km-other-owner"))
			Expect(npGenerated(ctx, "np-other")).To(HaveLen(1))
		})

		It("removes an egress policy when spec.egress is removed", func() {
			npNamespace(ctx, "np-undeclare")
			server := npMCPServer(ctx, "np-undeclare", npMail, &aiv1alpha1.EgressRules{AllowedCIDRs: []aiv1alpha1.EgressCIDR{{CIDR: npCIDR}}})
			r := newMCPReconciler(npSync(npConfig(true), false))
			npReconcileMCPServer(ctx, r, "np-undeclare", npMail)
			Expect(npGenerated(ctx, "np-undeclare")).To(HaveKey("km-mcp-mail-egress"))

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(server), server)).To(Succeed())
			server.Spec.Egress = nil
			Expect(k8sClient.Update(ctx, server)).To(Succeed())
			npReconcileMCPServer(ctx, r, "np-undeclare", npMail)
			Expect(npGenerated(ctx, "np-undeclare")).NotTo(HaveKey("km-mcp-mail-egress"))
			Expect(npGenerated(ctx, "np-undeclare")).To(HaveKey("km-mcp-mail-ingress"))
		})
	})

	Context("for an Agent", func() {
		It("restricts an agent that declares egress to its defaults and records the condition", func() {
			npNamespace(ctx, "np-agent")
			agent := &aiv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{Name: "scout", Namespace: "np-agent"},
				Spec: aiv1alpha1.AgentSpec{
					Capabilities: []string{candReasoning},
					Egress:       &aiv1alpha1.EgressRules{AllowedHosts: []string{"api.example.com"}},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
			Expect(k8sClient.Create(ctx, &aiv1alpha1.MCPGateway{
				ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "np-agent"}, Spec: aiv1alpha1.MCPGatewaySpec{Port: 8080},
			})).To(Succeed())
			r := &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(true), false)}
			pick := &modelPick{ModelID: "m", Endpoint: "http://vllm.models:8000"}
			Expect(r.reconcileNetworkPolicies(ctx, agent, pick, pick)).To(Succeed())

			policy := npGenerated(ctx, "np-agent")["km-agent-scout-egress"]
			npOwnedBy(&policy, "Agent", "scout")
			Expect(policy.Spec.PodSelector.MatchLabels).To(Equal(map[string]string{labelAgent: "scout"}))
			// DNS, the bus, the model server namespace, the gateway, the internet fallback (v4 and v6).
			Expect(len(policy.Spec.Egress)).To(BeNumerically(">=", 5))
			cond := meta.FindStatusCondition(agent.Status.Conditions, netpol.ConditionEgressEnforced)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		})

		It("creates no egress policy for an agent without spec.egress", func() {
			npNamespace(ctx, "np-agent-open")
			agent := &aiv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{Name: "open", Namespace: "np-agent-open"},
				Spec:       aiv1alpha1.AgentSpec{Capabilities: []string{candReasoning}},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
			r := &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(true), false)}
			Expect(r.reconcileNetworkPolicies(ctx, agent, nil, nil)).To(Succeed())
			Expect(npGenerated(ctx, "np-agent-open")).To(BeEmpty())
		})
	})

	Context("for a Crew", func() {
		It("admits the listed namespaces to the discussion gateway, owned by the Crew", func() {
			npNamespace(ctx, "np-crew")
			crew := &aiv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{Name: "pilot", Namespace: "np-crew"},
				Spec: aiv1alpha1.CrewSpec{
					Discussion: &aiv1alpha1.DiscussionConfig{},
					Network: &aiv1alpha1.CrewNetwork{AllowedIngress: []metav1.LabelSelector{
						{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "web"}},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, crew)).To(Succeed())
			r := &CrewReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(true), false)}
			Expect(r.reconcileNetworkPolicies(ctx, crew)).To(Succeed())

			p := npGenerated(ctx, "np-crew")["km-crew-pilot-gateway-ingress"]
			npOwnedBy(&p, "Crew", "pilot")
			Expect(p.Spec.Ingress[0].From[0].NamespaceSelector.MatchLabels).To(HaveKeyWithValue("kubernetes.io/metadata.name", "web"))

			crew.Spec.Discussion = &aiv1alpha1.DiscussionConfig{Enabled: boolPtr(false)}
			Expect(r.reconcileNetworkPolicies(ctx, crew)).To(Succeed())
			Expect(npGenerated(ctx, "np-crew")).To(BeEmpty())
		})
	})

	Context("with Cilium installed", func() {
		var crdInstalled bool

		BeforeEach(func() {
			crdInstalled = installCiliumCRD(ctx)
			if !crdInstalled {
				Skip("the CiliumNetworkPolicy kind could not be installed in the test environment")
			}
		})

		It("emits the host rule as a CiliumNetworkPolicy and removes it when the flag goes off", func() {
			npNamespace(ctx, "np-cilium")
			npMCPServer(ctx, "np-cilium", npMail, &aiv1alpha1.EgressRules{AllowedHosts: []string{npSMTP}, Ports: []int32{587}})
			npReconcileMCPServer(ctx, newMCPReconciler(npSync(npConfig(true), true)), "np-cilium", npMail)

			cnp := &unstructured.Unstructured{}
			cnp.SetGroupVersionKind(netpol.CiliumPolicyGVK)
			key := types.NamespacedName{Namespace: "np-cilium", Name: "km-mcp-mail-egress-fqdn"}
			Expect(k8sClient.Get(ctx, key, cnp)).To(Succeed())
			Expect(cnp.GetOwnerReferences()).To(HaveLen(1))
			Expect(cnp.GetLabels()).To(HaveKeyWithValue(netpol.LabelGeneratedBy, netpol.GeneratedByValue))

			got := &aiv1alpha1.MCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "np-cilium", Name: npMail}, got)).To(Succeed())
			Expect(meta.FindStatusCondition(got.Status.Conditions, netpol.ConditionEgressEnforced).Status).To(Equal(metav1.ConditionTrue))

			npReconcileMCPServer(ctx, newMCPReconciler(npSync(npConfig(false), true)), "np-cilium", npMail)
			err := k8sClient.Get(ctx, key, cnp)
			Expect(errors.IsNotFound(err)).To(BeTrue(), "the generated CiliumNetworkPolicy must be deleted")
		})

		It("adds the gateway entity policy for a crew", func() {
			npNamespace(ctx, "np-cilium-crew")
			crew := &aiv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{Name: "pilot", Namespace: "np-cilium-crew"},
				Spec:       aiv1alpha1.CrewSpec{Discussion: &aiv1alpha1.DiscussionConfig{}},
			}
			Expect(k8sClient.Create(ctx, crew)).To(Succeed())
			r := &CrewReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(true), true)}
			Expect(r.reconcileNetworkPolicies(ctx, crew)).To(Succeed())
			cnp := &unstructured.Unstructured{}
			cnp.SetGroupVersionKind(netpol.CiliumPolicyGVK)
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "np-cilium-crew", Name: "km-crew-pilot-gateway-entities"}, cnp)).To(Succeed())
		})
	})
})

// installCiliumCRD installs a CiliumNetworkPolicy kind that accepts any spec, and reports
// whether the API server serves it. It is left installed for the rest of the run.
func installCiliumCRD(ctx context.Context) bool {
	scheme := runtime.NewScheme()
	Expect(apiextensionsv1.AddToScheme(scheme)).To(Succeed())
	cli, err := client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "ciliumnetworkpolicies.cilium.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "cilium.io", Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: "ciliumnetworkpolicies", Singular: "ciliumnetworkpolicy", Kind: "CiliumNetworkPolicy", ListKind: "CiliumNetworkPolicyList",
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: "v2", Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "object", XPreserveUnknownFields: boolPtr(true),
				}},
			}},
		},
	}
	if err := cli.Create(ctx, crd); err != nil && !errors.IsAlreadyExists(err) {
		return false
	}
	Eventually(func() error {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(netpol.CiliumPolicyGVK.GroupVersion().WithKind("CiliumNetworkPolicyList"))
		return k8sClient.List(ctx, list, client.InNamespace("default"))
	}, "30s", "250ms").Should(Succeed())
	return true
}

// noCrewsSeen is a client whose cache has not seen any Crew yet.
type noCrewsSeen struct{ client.Client }

func (c noCrewsSeen) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*aiv1alpha1.CrewList); ok {
		return nil
	}
	return c.Client.List(ctx, list, opts...)
}
