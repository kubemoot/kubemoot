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
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	gwTestNS       = "crew-ns"
	gwTestProvider = "prov1"
)

func mkGateway(name, ns string) *kubemootv1alpha1.MCPGateway {
	return &kubemootv1alpha1.MCPGateway{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       kubemootv1alpha1.MCPGatewaySpec{Port: 8080},
	}
}

func TestGatewayChangeEnqueuesTheNamespacesAgents(t *testing.T) {
	s := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	cli := fake.NewClientBuilder().WithScheme(s).WithObjects(
		mkAgent("a1", gwTestNS, "demo"), mkAgent("a2", gwTestNS, "demo"), mkAgent("b1", "elsewhere", "demo"),
	).Build()
	reqs := namespaceAgentRequests[*kubemootv1alpha1.MCPGateway](context.Background(), cli, "MCPGateway", mkGateway("gw", gwTestNS))
	if len(reqs) != 2 {
		t.Fatalf("want both agents in crew-ns, got %v", reqs)
	}
	for _, r := range reqs {
		if r.Namespace != gwTestNS {
			t.Fatalf("enqueued an agent from another namespace: %v", r)
		}
	}
	if got := namespaceAgentRequests[*kubemootv1alpha1.MCPGateway](context.Background(), cli, "MCPGateway", mkGateway("gw", "empty-ns")); len(got) != 0 {
		t.Fatalf("a gateway in a namespace without agents enqueues nothing, got %v", got)
	}
	if got := namespaceAgentRequests[*kubemootv1alpha1.MCPGateway](context.Background(), cli, "MCPGateway", mkAgent("x", gwTestNS, "demo")); got != nil {
		t.Fatalf("a non-MCPGateway object maps to nothing, got %v", got)
	}
}

func TestGatewayChangeListFailureEnqueuesNothing(t *testing.T) {
	// A scheme without the kubemoot types makes the Agent list fail.
	cli := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build()
	if got := namespaceAgentRequests[*kubemootv1alpha1.MCPGateway](context.Background(), cli, "MCPGateway", mkGateway("gw", gwTestNS)); got != nil {
		t.Fatalf("a failed agent list enqueues nothing, got %v", got)
	}
}

func TestGatewayWiringChangedPredicate(t *testing.T) {
	p := gatewayWiringChanged()
	base := mkGateway("gw", "ns")
	if !p.Create(event.CreateEvent{Object: base}) {
		t.Error("a new gateway must enqueue: agents created before it need its endpoint")
	}
	if !p.Delete(event.DeleteEvent{Object: base}) {
		t.Error("a removed gateway must enqueue")
	}
	if p.Generic(event.GenericEvent{Object: base}) {
		t.Error("generic events must not enqueue")
	}
	moved := base.DeepCopy()
	moved.Spec.Port = 9090
	if !p.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: moved}) {
		t.Error("a port change must enqueue: the agents' endpoint carries the port")
	}
	statusOnly := base.DeepCopy()
	statusOnly.Status.Phase = "Running"
	statusOnly.Spec.AdminUI = ptr.To(false)
	if p.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: statusOnly}) {
		t.Error("changes the agents' env does not use must not enqueue")
	}
	terminating := base.DeepCopy()
	now := metav1.Now()
	terminating.DeletionTimestamp = &now
	if !p.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: terminating}) {
		t.Error("the start of a finalizer-held deletion must enqueue: agents leave a terminating gateway")
	}
	if p.Update(event.UpdateEvent{ObjectOld: mkAgent("a", "ns", "c"), ObjectNew: mkAgent("a", "ns", "c")}) {
		t.Error("non-MCPGateway objects must not enqueue")
	}
}

// The Agent controller runs under a real manager: an Agent deployed before any
// MCPGateway exists has no gateway env; creating the gateway afterwards must
// re-render the agent's Deployment with the gateway endpoint, with nothing else
// touching the Agent; removing the gateway takes the endpoint away again.
var _ = Describe("Agent controller and a gateway created after its agents", func() {
	It("wires the late gateway into the existing agent", func() {
		ctx, stop := context.WithCancel(context.Background())
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "gw-after-agents-"}}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		ns := namespace.Name
		provider := &kubemootv1alpha1.ModelProvider{
			ObjectMeta: metav1.ObjectMeta{Name: gwTestProvider, Namespace: ns},
			Spec:       kubemootv1alpha1.ModelProviderSpec{Type: kubemootv1alpha1.ProviderTypeOllama, Endpoint: "http://prov1:11434"},
		}
		Expect(k8sClient.Create(ctx, provider)).To(Succeed())
		provider.Status.Ready = true
		Expect(k8sClient.Status().Update(ctx, provider)).To(Succeed())
		model := &kubemootv1alpha1.Model{
			ObjectMeta: metav1.ObjectMeta{Name: "m1", Namespace: ns},
			Spec:       kubemootv1alpha1.ModelSpec{Model: testModelID, ProviderRef: gwTestProvider},
		}
		Expect(k8sClient.Create(ctx, model)).To(Succeed())
		model.Status.Ready = true
		Expect(k8sClient.Status().Update(ctx, model)).To(Succeed())

		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:                 scheme.Scheme,
			Metrics:                metricsserver.Options{BindAddress: "0"},
			HealthProbeBindAddress: "0",
			Cache:                  cache.Options{DefaultNamespaces: map[string]cache.Config{ns: {}}},
			Controller:             config.Controller{SkipNameValidation: ptr.To(true)},
		})
		Expect(err).NotTo(HaveOccurred())
		r := &AgentReconciler{Client: mgr.GetClient(), ConfigCache: NewConfigCache()}
		Expect(r.SetupWithManager(mgr)).To(Succeed())
		done := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			defer close(done)
			Expect(mgr.Start(ctx)).To(Succeed())
		}()
		DeferCleanup(func() {
			stop()
			<-done
		})

		agent := &kubemootv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: "a1", Namespace: ns, Labels: map[string]string{labelCrew: "crew1"}},
			Spec:       kubemootv1alpha1.AgentSpec{Capabilities: []string{candReasoning}},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		gatewayEnv := func() string {
			dep := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "a1", Namespace: ns}, dep); err != nil {
				return "no deployment"
			}
			for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
				if e.Name == "KUBEMOOT_GATEWAY_ENDPOINT" {
					return e.Value
				}
			}
			return ""
		}
		Eventually(gatewayEnv, "10s").Should(BeEmpty(), "with no gateway the agent deploys in direct mode")

		gw := mkGateway("crew-gw", ns)
		Expect(k8sClient.Create(ctx, gw)).To(Succeed())
		Eventually(gatewayEnv, "10s").Should(Equal("http://crew-gw."+ns+":8080"),
			"the late gateway must reach the agent without anything touching the Agent")

		Expect(k8sClient.Delete(ctx, gw)).To(Succeed())
		Eventually(gatewayEnv, "10s").Should(BeEmpty(), "a removed gateway must leave the agent in direct mode")
	})
})
