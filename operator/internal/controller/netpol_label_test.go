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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/netpol"
)

const (
	npLabelHelm      = "Helm"
	npLabelDenyName  = "km-default-deny"
	npLabelInternal  = "km-crew-internal"
	npLabelSharedSrv = "shared-label-tool"
)

var _ = Describe("The crew namespace label follows the Crews", func() {
	ctx := context.Background()

	bareNamespace := func(name string, labels map[string]string) {
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}})).To(Succeed())
	}
	getNamespace := func(name string) *corev1.Namespace {
		ns := &corev1.Namespace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, ns)).To(Succeed())
		return ns
	}
	crewReconciler := func(enabled bool) *CrewReconciler {
		return &CrewReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(enabled), false)}
	}

	It("labels a namespace that holds a Crew and has no label, and gives it the baseline", func() {
		bareNamespace("npl-new", map[string]string{labelManagedBy: npLabelHelm})
		npHandWritten(ctx, "npl-new", npHandWrote)
		npCrew(ctx, "npl-new", "sched")
		npReconcileNamespace(ctx, "npl-new")

		ns := getNamespace("npl-new")
		Expect(ns.Labels).To(HaveKeyWithValue(netpol.LabelCrew, "sched"))
		Expect(ns.Labels).To(HaveKeyWithValue(labelManagedBy, npLabelHelm), "the chart's own label stays")
		Expect(ns.Annotations).To(HaveKeyWithValue(annoCrewLabelBy, managedByValue))
		Expect(npGenerated(ctx, "npl-new")).To(HaveKey(npLabelDenyName))
		Expect(npGenerated(ctx, "npl-new")).To(HaveKey(npLabelInternal))
	})

	It("keeps the label and the baseline when one of two Crews goes, and drops both with the last", func() {
		bareNamespace("npl-two", nil)
		npHandWritten(ctx, "npl-two", npHandWrote)
		first := npCrew(ctx, "npl-two", "alpha")
		second := npCrew(ctx, "npl-two", "beta")
		npReconcileNamespace(ctx, "npl-two")
		Expect(getNamespace("npl-two").Labels).To(HaveKeyWithValue(netpol.LabelCrew, "alpha"))
		Expect(npGenerated(ctx, "npl-two")).To(HaveLen(2))

		Expect(k8sClient.Delete(ctx, first)).To(Succeed())
		npReconcileNamespace(ctx, "npl-two")
		Expect(getNamespace("npl-two").Labels).To(HaveKey(netpol.LabelCrew))
		Expect(npGenerated(ctx, "npl-two")).To(HaveLen(2))

		Expect(k8sClient.Delete(ctx, second)).To(Succeed())
		npReconcileNamespace(ctx, "npl-two")
		ns := getNamespace("npl-two")
		Expect(ns.Labels).NotTo(HaveKey(netpol.LabelCrew))
		Expect(ns.Annotations).NotTo(HaveKey(annoCrewLabelBy))
		Expect(npGenerated(ctx, "npl-two")).To(BeEmpty())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "npl-two", Name: npHandWrote}, &networkingv1.NetworkPolicy{})).To(Succeed())
	})

	It("removes only what the operator set, and leaves the chart's managed-by label", func() {
		bareNamespace("npl-helm", map[string]string{labelManagedBy: npLabelHelm})
		crew := npCrew(ctx, "npl-helm", "sched")
		npReconcileNamespace(ctx, "npl-helm")
		Expect(k8sClient.Delete(ctx, crew)).To(Succeed())
		npReconcileNamespace(ctx, "npl-helm")

		ns := getNamespace("npl-helm")
		Expect(ns.Labels).NotTo(HaveKey(netpol.LabelCrew))
		Expect(ns.Labels).To(HaveKeyWithValue(labelManagedBy, npLabelHelm))
	})

	It("never rewrites a label another source set, so Crews from several sources do not flap", func() {
		bareNamespace("npl-chart", map[string]string{netpol.LabelCrew: "from-chart"})
		other := npCrew(ctx, "npl-chart", "zeta")
		Expect(crewReconciler(true).reconcileNamespaceLabel(ctx, other)).To(Succeed())
		Expect(crewReconciler(true).reconcileNamespaceLabel(ctx, other)).To(Succeed())

		ns := getNamespace("npl-chart")
		Expect(ns.Labels).To(HaveKeyWithValue(netpol.LabelCrew, "from-chart"))
		Expect(ns.Annotations).NotTo(HaveKey(annoCrewLabelBy), "a label the operator did not set is never removed by it")

		bareNamespace("npl-flap", nil)
		b := npCrew(ctx, "npl-flap", "b-crew")
		a := npCrew(ctx, "npl-flap", "a-crew")
		Expect(crewReconciler(true).reconcileNamespaceLabel(ctx, b)).To(Succeed())
		Expect(crewReconciler(true).reconcileNamespaceLabel(ctx, a)).To(Succeed())
		npReconcileNamespace(ctx, "npl-flap")
		Expect(getNamespace("npl-flap").Labels).To(HaveKeyWithValue(netpol.LabelCrew, "a-crew"),
			"both reconcilers use the first Crew by name, and the value stays")
	})

	It("keeps replacing the value for a Crew that asks for namespace management while the flag is off", func() {
		bareNamespace("npl-managed", map[string]string{netpol.LabelCrew: "older", labelManagedBy: npLabelHelm})
		crew := npCrew(ctx, "npl-managed", "mine")
		crew.Annotations = map[string]string{manageNamespaceAnno: manageNamespaceRequested}
		Expect(crewReconciler(false).reconcileNamespaceLabel(ctx, crew)).To(Succeed())

		ns := getNamespace("npl-managed")
		Expect(ns.Labels).To(HaveKeyWithValue(netpol.LabelCrew, "mine"))
		Expect(ns.Labels).To(HaveKeyWithValue(labelManagedBy, managedByValue))
	})

	It("labels from the Crew reconciler too, and not with the flag off or in a protected namespace", func() {
		bareNamespace("npl-crew", nil)
		crew := npCrew(ctx, "npl-crew", "c1")
		Expect(crewReconciler(true).reconcileNamespaceLabel(ctx, crew)).To(Succeed())
		Expect(getNamespace("npl-crew").Labels).To(HaveKeyWithValue(netpol.LabelCrew, "c1"))

		bareNamespace("npl-off", nil)
		off := npCrew(ctx, "npl-off", "c2")
		Expect(crewReconciler(false).reconcileNamespaceLabel(ctx, off)).To(Succeed())
		Expect(getNamespace("npl-off").Labels).NotTo(HaveKey(netpol.LabelCrew))

		inKubeSystem := &aiv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: "npl-ks", Namespace: testKubeSystemNS}}
		Expect(crewReconciler(true).reconcileNamespaceLabel(ctx, inKubeSystem)).To(Succeed())
		Expect(getNamespace(testKubeSystemNS).Labels).NotTo(HaveKey(netpol.LabelCrew))
	})

	It("lets a shared tool server admit the gateway of a namespace the operator labeled", func() {
		npNamespace(ctx, npOperatorNS)
		bareNamespace("npl-shared", nil)
		npCrew(ctx, "npl-shared", "sched")
		npReconcileNamespace(ctx, "npl-shared")
		Expect(getNamespace("npl-shared").Labels).To(HaveKey(netpol.LabelCrew))

		npMCPServer(ctx, npOperatorNS, npLabelSharedSrv, nil)
		r := &MCPServerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache(), NetPol: npSync(npConfig(true), false)}
		npReconcileMCPServer(ctx, r, npOperatorNS, npLabelSharedSrv)

		p := npGenerated(ctx, npOperatorNS)["km-mcp-"+npLabelSharedSrv+"-ingress"]
		Expect(p.Spec.Ingress).NotTo(BeEmpty())
		var admitted []string
		for _, peer := range p.Spec.Ingress[0].From {
			if peer.NamespaceSelector != nil && len(peer.NamespaceSelector.MatchExpressions) > 0 {
				admitted = peer.NamespaceSelector.MatchExpressions[0].Values
			}
		}
		Expect(admitted).To(ContainElement("npl-shared"))
	})
})
