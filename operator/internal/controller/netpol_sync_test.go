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
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/netpol"
)

const (
	npCrewA    = "crew-a"
	npCIDR     = "203.0.113.0/24"
	npCIDRHalf = "203.0.113.128/25"
)

func TestNetPolComponentLabelsMatchTheControllers(t *testing.T) {
	for name, pair := range map[string][2]string{
		"mcp server":         {componentMCPServer, netpol.ComponentMCPServer},
		"mcp gateway":        {componentMCPGateway, netpol.ComponentMCPGateway},
		"query service":      {componentQueryService, netpol.ComponentQueryService},
		"discussion gateway": {"discussion-gateway", netpol.ComponentDiscussionGateway},
		"agent label":        {labelAgent, netpol.LabelAgent},
		"crew label":         {labelCrew, netpol.LabelCrew},
		"gateway port":       {"8080", "8080"},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s: controller says %q, netpol says %q", name, pair[0], pair[1])
		}
	}
	if crewGatewayPort != netpol.DiscussionGatewayPort {
		t.Errorf("the discussion gateway port differs: %d vs %d", crewGatewayPort, netpol.DiscussionGatewayPort)
	}
}

func TestNetPolicySyncResolvedCilium(t *testing.T) {
	cases := []struct {
		mode      netpol.CiliumMode
		installed func() bool
		want      bool
	}{
		{netpol.CiliumOn, func() bool { return false }, true},
		{netpol.CiliumOff, func() bool { return true }, false},
		{netpol.CiliumAuto, func() bool { return true }, true},
		{netpol.CiliumAuto, func() bool { return false }, false},
		{netpol.CiliumAuto, nil, false},
	}
	for _, c := range cases {
		s := &NetPolicySync{Config: netpol.Config{Cilium: c.mode}, CiliumInstalled: c.installed}
		if got := s.Resolved().UseCilium; got != c.want {
			t.Errorf("mode %s: UseCilium = %v, want %v", c.mode, got, c.want)
		}
	}
}

func TestNilNetPolicySyncDoesNothing(t *testing.T) {
	var s *NetPolicySync
	if err := s.Apply(context.Background(), &aiv1alpha1.Agent{}, "ns", nil); err != nil {
		t.Errorf("a nil NetPolicySync must be a no-op: %v", err)
	}
}

func TestCiliumKindInstalled(t *testing.T) {
	gvk := netpol.CiliumPolicyGVK
	known := meta.NewDefaultRESTMapper(nil)
	known.Add(gvk, meta.RESTScopeNamespace)
	if !CiliumKindInstalled(known)() {
		t.Error("the kind is mapped, so it is installed")
	}
	unknown := meta.NewDefaultRESTMapper([]schema.GroupVersion{gvk.GroupVersion()})
	if CiliumKindInstalled(unknown)() {
		t.Error("the kind is not mapped, so it is not installed")
	}
}

func TestSetEgressCondition(t *testing.T) {
	var conds []metav1.Condition
	setEgressCondition(&conds, 3, netpol.EgressStatus{Applicable: true, Reason: netpol.ReasonFQDNNotSupported, Message: "m"})
	c := meta.FindStatusCondition(conds, netpol.ConditionEgressEnforced)
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != netpol.ReasonFQDNNotSupported || c.ObservedGeneration != 3 {
		t.Fatalf("condition = %+v", c)
	}
	setEgressCondition(&conds, 4, netpol.EgressStatus{Applicable: true, Enforced: true, Reason: netpol.ReasonEnforced})
	if c = meta.FindStatusCondition(conds, netpol.ConditionEgressEnforced); c.Status != metav1.ConditionTrue {
		t.Errorf("condition = %+v", c)
	}
	setEgressCondition(&conds, 5, netpol.EgressStatus{})
	if meta.FindStatusCondition(conds, netpol.ConditionEgressEnforced) != nil {
		t.Error("a workload without egress must have no EgressEnforced condition")
	}
}

func TestAPIServerEndpoints(t *testing.T) {
	port := int32(6443)
	slice := &discoveryv1.EndpointSlice{
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.1", "fd00::1"}}},
		Ports:     []discoveryv1.EndpointPort{{Port: &port}, {Name: strPtr("no-port")}},
	}
	got := apiServerEndpoints(slice)
	if len(got) != 2 || got[0].CIDR != "10.0.0.1/32" || got[1].CIDR != "fd00::1/128" || got[0].Port != 6443 {
		t.Errorf("endpoints = %+v", got)
	}
}

func strPtr(s string) *string { return &s }

func TestAttachAPIServer(t *testing.T) {
	port := int32(6443)
	scheme := runtime.NewScheme()
	if err := discoveryv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: kubernetesServiceName, Namespace: metav1.NamespaceDefault,
			Labels: map[string]string{discoveryv1.LabelServiceName: kubernetesServiceName},
		},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"192.168.1.10"}}},
		Ports:     []discoveryv1.EndpointPort{{Port: &port}},
	}
	s := &NetPolicySync{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(slice).Build()}
	cfg := netpol.Config{Enabled: true, Egress: netpol.EgressDeclared}

	asks := netpol.Workload{Rules: &aiv1alpha1.EgressRules{APIServer: true}}
	if err := s.attachAPIServer(context.Background(), cfg, &asks); err != nil || len(asks.APIServer) != 1 {
		t.Errorf("a workload that asks gets the endpoints: %v %+v", err, asks.APIServer)
	}
	for name, w := range map[string]netpol.Workload{
		"does not ask":  {Rules: &aiv1alpha1.EgressRules{}},
		"no rules":      {},
		"with Cilium":   {Rules: &aiv1alpha1.EgressRules{APIServer: true}},
		"egress is off": {Rules: &aiv1alpha1.EgressRules{APIServer: true}},
	} {
		c := cfg
		switch name {
		case "with Cilium":
			c = c.WithCilium(true)
		case "egress is off":
			c.Egress = netpol.EgressOff
		}
		if err := s.attachAPIServer(context.Background(), c, &w); err != nil || len(w.APIServer) != 0 {
			t.Errorf("%s: must not read the endpoints: %v %+v", name, err, w.APIServer)
		}
	}
}

func TestGatewayAdminPolicyToolPortNarrowsOnlyUnderTheFlag(t *testing.T) {
	gw := &aiv1alpha1.MCPGateway{ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: npCrewA}, Spec: aiv1alpha1.MCPGatewaySpec{Port: 8080}}
	open := (&MCPGatewayReconciler{}).buildAdminNetworkPolicy(gw)
	if len(open.Spec.Ingress[0].From) != 0 {
		t.Errorf("without the flag the tool port stays open to every pod: %+v", open.Spec.Ingress[0])
	}
	off := (&MCPGatewayReconciler{NetPol: &NetPolicySync{Config: netpol.Config{OperatorNamespace: defaultOperatorNamespace}}}).buildAdminNetworkPolicy(gw)
	if len(off.Spec.Ingress[0].From) != 0 {
		t.Errorf("with the flag off the tool port stays as it was: %+v", off.Spec.Ingress[0])
	}
	on := (&MCPGatewayReconciler{NetPol: &NetPolicySync{Config: netpol.Config{Enabled: true, Ingress: true, OperatorNamespace: defaultOperatorNamespace}}}).buildAdminNetworkPolicy(gw)
	rule := on.Spec.Ingress[0]
	if len(rule.From) != 2 || rule.Ports[0].Port.IntVal != 8080 {
		t.Errorf("with the flag on the tool port admits the namespace and the operator: %+v", rule)
	}
	if len(on.Spec.Ingress) != 2 || on.Spec.Ingress[1].Ports[0].Port.IntVal != gatewayAdminPort(gw) {
		t.Errorf("the admin port rule must stay: %+v", on.Spec.Ingress)
	}
}

var _ = Describe("Egress validation on the CRDs", func() {
	ctx := context.Background()
	bad := map[string]aiv1alpha1.EgressRules{
		"a scheme in a host":       {AllowedHosts: []string{"https://api.example.com"}},
		"a path in a host":         {AllowedHosts: []string{"api.example.com/v1"}},
		"a port in a host":         {AllowedHosts: []string{"api.example.com:443"}},
		"a wildcard in the middle": {AllowedHosts: []string{"api.*.example.com"}},
		"a host with one label":    {AllowedHosts: []string{"localhost"}},
		"an upper case host":       {AllowedHosts: []string{"API.example.com"}},
		"a bad CIDR":               {AllowedCIDRs: []aiv1alpha1.EgressCIDR{{CIDR: "10.0.0.0/33"}}},
		"a CIDR that is a host":    {AllowedCIDRs: []aiv1alpha1.EgressCIDR{{CIDR: "api.example.com"}}},
		"a bad except entry":       {AllowedCIDRs: []aiv1alpha1.EgressCIDR{{CIDR: "10.0.0.0/8", Except: []string{"not-a-cidr"}}}},
		"port 0":                   {Ports: []int32{0}},
		"a port above 65535":       {Ports: []int32{65536}},
	}
	for name, rules := range bad {
		It("rejects "+name, func() {
			rules := rules
			s := &aiv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "bad-egress-", Namespace: metav1.NamespaceDefault},
				Spec:       aiv1alpha1.MCPServerSpec{Image: specImage, Egress: &rules},
			}
			Expect(k8sClient.Create(ctx, s)).NotTo(Succeed())
		})
	}

	It("accepts hosts, a wildcard, ranges with exceptions, and ports on an MCPServer and an Agent", func() {
		rules := &aiv1alpha1.EgressRules{
			AllowedHosts: []string{"smtp.example.com", "*.example.org"},
			AllowedCIDRs: []aiv1alpha1.EgressCIDR{{CIDR: npCIDR, Except: []string{npCIDRHalf}}, {CIDR: "2001:db8::/32"}},
			Ports:        []int32{443, 587}, APIServer: true,
		}
		Expect(k8sClient.Create(ctx, &aiv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "ok-egress", Namespace: metav1.NamespaceDefault},
			Spec:       aiv1alpha1.MCPServerSpec{Image: specImage, Egress: rules},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &aiv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: "ok-egress", Namespace: metav1.NamespaceDefault},
			Spec:       aiv1alpha1.AgentSpec{Capabilities: []string{candReasoning}, Egress: rules},
		})).To(Succeed())
	})
})
