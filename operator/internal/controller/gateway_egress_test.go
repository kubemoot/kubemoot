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

	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/netpol"
)

const (
	gwEgressNS     = "crew-egress"
	gwEgressName   = "gw"
	gwEgressPolicy = "km-gateway-gw-egress"
	gwOReilly      = "api.oreilly.com"
)

func gwEgressConfig(egress netpol.EgressMode) netpol.Config {
	return netpol.Config{
		Enabled: true, Ingress: true, Egress: egress, OperatorNamespace: npOperatorNS,
		DNS: netpol.PodRef{Namespace: testKubeSystemNS, Labels: map[string]string{"k8s-app": "kube-dns"}},
	}
}

func gwEgressGateway() *aiv1alpha1.MCPGateway {
	return &aiv1alpha1.MCPGateway{
		ObjectMeta: metav1.ObjectMeta{Name: gwEgressName, Namespace: gwEgressNS, UID: "gw-uid", Generation: 3},
		Spec:       aiv1alpha1.MCPGatewaySpec{Implementation: aiv1alpha1.ImplementationKubemoot},
	}
}

func gwEgressServer(name, external string) *aiv1alpha1.MCPServer {
	spec := aiv1alpha1.MCPServerSpec{Image: specImage, Port: 3000}
	if external != "" {
		spec = aiv1alpha1.MCPServerSpec{ExternalEndpoint: external}
	}
	return &aiv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gwEgressNS}, Spec: spec}
}

type gwEgressEnv struct {
	t   *testing.T
	cli client.Client
	r   *MCPGatewayReconciler
	gw  *aiv1alpha1.MCPGateway
}

func newGwEgressEnv(t *testing.T, cfg netpol.Config, cilium bool, servers ...*aiv1alpha1.MCPServer) *gwEgressEnv {
	t.Helper()
	scheme := gatewayScheme(t)
	gw := gwEgressGateway()
	objs := make([]client.Object, 0, 1+len(servers))
	objs = append(objs, gw)
	for _, s := range servers {
		objs = append(objs, s)
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	r := &MCPGatewayReconciler{Client: cli, Scheme: scheme, NetPol: &NetPolicySync{
		Client: cli, Scheme: scheme, Config: cfg, CiliumInstalled: func() bool { return cilium },
	}}
	return &gwEgressEnv{t: t, cli: cli, r: r, gw: gw}
}

func (e *gwEgressEnv) reconcile() {
	e.t.Helper()
	if err := e.r.reconcileEgress(context.Background(), e.gw); err != nil {
		e.t.Fatalf("reconcileEgress: %v", err)
	}
}

func (e *gwEgressEnv) policy() *networkingv1.NetworkPolicy {
	e.t.Helper()
	p := &networkingv1.NetworkPolicy{}
	if err := e.cli.Get(context.Background(), types.NamespacedName{Namespace: gwEgressNS, Name: gwEgressPolicy}, p); err != nil {
		return nil
	}
	return p
}

// worldPorts are the ports of the rules that allow addresses outside the cluster.
func worldPorts(p *networkingv1.NetworkPolicy) []int32 {
	var out []int32
	for _, rule := range p.Spec.Egress {
		if len(rule.To) == 1 && rule.To[0].IPBlock != nil {
			for _, port := range rule.Ports {
				out = append(out, port.Port.IntVal)
			}
		}
	}
	return out
}

func fqdnRule(t *testing.T, cli client.Client) []any {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(netpol.CiliumPolicyGVK)
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: gwEgressNS, Name: gwEgressPolicy + "-fqdn"}, u); err != nil {
		t.Fatalf("the Cilium policy: %v", err)
	}
	egress, _, _ := unstructured.NestedSlice(u.Object, "spec", "egress")
	return egress
}

// fqdnHosts are the host names the Cilium policy allows, in rule order.
func fqdnHosts(rules []any) []string {
	var hosts []string
	for _, r := range rules {
		rule, _ := r.(map[string]any)
		fqdns, _ := rule["toFQDNs"].([]any)
		for _, f := range fqdns {
			hosts = append(hosts, f.(map[string]any)["matchName"].(string))
		}
	}
	return hosts
}

// fqdnPorts maps each allowed host to the ports its rule allows.
func fqdnPorts(rules []any) map[string][]string {
	out := map[string][]string{}
	for _, r := range rules {
		rule, _ := r.(map[string]any)
		fqdns, _ := rule["toFQDNs"].([]any)
		groups, _ := rule["toPorts"].([]any)
		for _, f := range fqdns {
			for _, g := range groups {
				ports, _ := g.(map[string]any)["ports"].([]any)
				for _, p := range ports {
					host := f.(map[string]any)["matchName"].(string)
					out[host] = append(out[host], p.(map[string]any)["port"].(string))
				}
			}
		}
	}
	return out
}

// assertHostPorts checks the policy allows exactly the given hosts, each on its one port.
func assertHostPorts(t *testing.T, rules []any, want map[string]string) {
	t.Helper()
	got := fqdnPorts(rules)
	if len(got) != len(want) {
		t.Errorf("exactly the endpoint hosts %v: %v", want, got)
	}
	for host, port := range want {
		if len(got[host]) != 1 || got[host][0] != port {
			t.Errorf("%s on port %s only: %v", host, port, got[host])
		}
	}
}

func TestGatewayFrontingAnExternalServerAllowsExactlyThatHostAndPort(t *testing.T) {
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), true,
		gwEgressServer("oreilly-mcp", "https://"+gwOReilly+"/mcp"), gwEgressServer("other", "http://tools.example.com:8080/mcp"))
	e.reconcile()

	assertHostPorts(t, fqdnRule(t, e.cli), map[string]string{gwOReilly: "443", "tools.example.com": "8080"})
	if c := meta.FindStatusCondition(e.gw.Status.Conditions, netpol.ConditionEgressEnforced); c == nil || c.Status != metav1.ConditionTrue {
		t.Errorf("EgressEnforced is True: %+v", c)
	}
}

func TestGatewayEgressWithoutCiliumAllowsOnlyTheEndpointPorts(t *testing.T) {
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), false, gwEgressServer("oreilly-mcp", "https://"+gwOReilly))
	e.reconcile()
	p := e.policy()
	if p == nil {
		t.Fatal("a gateway that fronts an external server gets an egress policy")
	}
	if got := worldPorts(p); len(got) != 2 || got[0] != 443 {
		t.Errorf("the internet fallback on port 443 only (IPv4 and IPv6 rules): %v", got)
	}
	if c := meta.FindStatusCondition(e.gw.Status.Conditions, netpol.ConditionEgressEnforced); c == nil || c.Status != metav1.ConditionFalse || c.Reason != netpol.ReasonFQDNNotSupported {
		t.Errorf("the fallback is reported: %+v", c)
	}
}

func TestGatewayKeepsDNSAndItsInClusterServers(t *testing.T) {
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), false,
		gwEgressServer("oreilly-mcp", "https://"+gwOReilly), gwEgressServer("search", ""))
	e.reconcile()
	p := e.policy()
	var dns, server bool
	for _, rule := range p.Spec.Egress {
		for _, port := range rule.Ports {
			dns = dns || port.Port.IntVal == 53
			server = server || (port.Port.IntVal == 3000 && len(rule.To) == 1 && rule.To[0].PodSelector != nil)
		}
	}
	if !dns || !server {
		t.Errorf("DNS and the in-cluster server's pods on its port are allowed: dns=%v server=%v", dns, server)
	}
}

func TestGatewayFrontingNoExternalServerGetsNoEgressPolicy(t *testing.T) {
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), true, gwEgressServer("search", ""))
	e.reconcile()
	if e.policy() != nil {
		t.Error("an in-cluster only gateway is not restricted")
	}
	if c := meta.FindStatusCondition(e.gw.Status.Conditions, netpol.ConditionEgressEnforced); c != nil {
		t.Errorf("no egress condition without egress: %+v", c)
	}
}

func TestGatewayEgressOffGeneratesNothing(t *testing.T) {
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressOff), false, gwEgressServer("oreilly-mcp", "https://"+gwOReilly))
	e.reconcile()
	if e.policy() != nil {
		t.Error("egress mode Off generates no egress policy")
	}
}

func TestGatewayEgressFollowsAnEndpointChange(t *testing.T) {
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), true, gwEgressServer("oreilly-mcp", "https://"+gwOReilly+"/mcp"))
	e.reconcile()

	server := &aiv1alpha1.MCPServer{}
	key := types.NamespacedName{Namespace: gwEgressNS, Name: "oreilly-mcp"}
	if err := e.cli.Get(context.Background(), key, server); err != nil {
		t.Fatal(err)
	}
	server.Spec.ExternalEndpoint = "https://mcp.example.net:8443/mcp"
	if err := e.cli.Update(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	e.reconcile()

	hosts := fqdnHosts(fqdnRule(t, e.cli))
	if len(hosts) != 1 || hosts[0] != "mcp.example.net" {
		t.Errorf("only the new host is allowed: %v", hosts)
	}
}

func TestGatewayEgressReportsAnInvalidEndpointAndAllowsNothingForIt(t *testing.T) {
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), false,
		gwEgressServer("bad", "ftp://evil.example.com"), gwEgressServer("worse", "https://*.example.com"))
	e.reconcile()
	p := e.policy()
	if p == nil {
		t.Fatal("the gateway is still restricted")
	}
	if got := worldPorts(p); len(got) != 0 {
		t.Errorf("an invalid endpoint allows nothing: %v", got)
	}
	c := meta.FindStatusCondition(e.gw.Status.Conditions, netpol.ConditionEgressEnforced)
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != netpol.ReasonEndpointInvalid {
		t.Fatalf("the invalid endpoint is reported: %+v", c)
	}
}

func TestGatewayEgressWithoutNetPolDoesNothing(t *testing.T) {
	r := &MCPGatewayReconciler{}
	if err := r.reconcileEgress(context.Background(), gwEgressGateway()); err != nil {
		t.Errorf("no network policy setting, no work: %v", err)
	}
}

func (e *gwEgressEnv) condition() *metav1.Condition {
	return meta.FindStatusCondition(e.gw.Status.Conditions, netpol.ConditionEgressEnforced)
}

func TestGatewayEgressReadsEveryKindOfEndpoint(t *testing.T) {
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), true,
		gwEgressServer("a", "https://example.com"), gwEgressServer("b", "https://x.kube-system/mcp"),
		gwEgressServer("c", "http://plain.example.net/mcp"), gwEgressServer("d", "https://user:pw@auth.example.org/mcp"),
		gwEgressServer("e", "https://trailing.example.io./mcp"))
	e.reconcile()
	assertHostPorts(t, fqdnRule(t, e.cli), map[string]string{
		"example.com": "443", "x.kube-system": "443", "plain.example.net": "80",
		"auth.example.org": "443", "trailing.example.io": "443",
	})
}

func TestGatewayEgressAllowsAnAddressLiteralAsASingleAddress(t *testing.T) {
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), false, gwEgressServer("ip", "https://203.0.113.7/mcp"))
	e.reconcile()
	found := false
	for _, rule := range e.policy().Spec.Egress {
		if len(rule.To) == 1 && rule.To[0].IPBlock != nil && rule.To[0].IPBlock.CIDR == "203.0.113.7/32" {
			found = len(rule.Ports) == 1 && rule.Ports[0].Port.IntVal == 443
		}
	}
	if !found {
		t.Errorf("the address on 443 only: %+v", e.policy().Spec.Egress)
	}
	if got := worldPorts(e.policy()); len(got) != 1 {
		t.Errorf("no internet fallback for an address: %v", got)
	}
}

func TestGatewayEgressDropsTheRuleOfADeletedServer(t *testing.T) {
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), true,
		gwEgressServer("one", "https://one.example.com"), gwEgressServer("two", "https://two.example.com"))
	e.reconcile()
	if err := e.cli.Delete(context.Background(), gwEgressServer("two", "")); err != nil {
		t.Fatal(err)
	}
	e.reconcile()
	assertHostPorts(t, fqdnRule(t, e.cli), map[string]string{"one.example.com": "443"})

	if err := e.cli.Delete(context.Background(), gwEgressServer("one", "")); err != nil {
		t.Fatal(err)
	}
	e.reconcile()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(netpol.CiliumPolicyGVK)
	err := e.cli.Get(context.Background(), types.NamespacedName{Namespace: gwEgressNS, Name: gwEgressPolicy + "-fqdn"}, u)
	if err == nil || e.policy() != nil {
		t.Error("with no external server left the gateway is not restricted and its policies are removed")
	}
}

func TestGatewayEgressAllowsAnInClusterServerInAnotherNamespace(t *testing.T) {
	core := gwEgressServer("core", "")
	core.Namespace = npOperatorNS
	core.Labels = map[string]string{labelCoreMCP: valueTrue}
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), false, gwEgressServer("oreilly-mcp", "https://"+gwOReilly), core)
	servers := []aiv1alpha1.MCPServer{*gwEgressServer("oreilly-mcp", "https://"+gwOReilly), *core}
	w := netpol.Workload{Name: "gateway-gw", Namespace: gwEgressNS, Selector: gatewayLabels(e.gw)}
	if err := e.r.NetPol.attachGatewayDestinations(context.Background(), e.gw, servers, &w); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range w.Defaults {
		found = found || (d.Namespace == npOperatorNS && !d.SameNamespace && d.Port == 3000 && d.PodLabels[labelName] == "core")
	}
	if !found {
		t.Errorf("the core server's pods in its own namespace: %+v", w.Defaults)
	}
}

func TestGatewayEgressKeepsTheContextForgeDatabaseRedisAndToolIndex(t *testing.T) {
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), false, gwEgressServer("oreilly-mcp", "https://"+gwOReilly))
	e.gw.Spec.ContextForge = &aiv1alpha1.ContextForgeConfig{
		DatabaseURL: "postgresql://db.example.com:5432/forge", RedisURL: "redis://cache.example.com:6379",
	}
	e.gw.Spec.ToolIndex = &aiv1alpha1.ToolIndexConfig{}
	e.gw.Status.ToolIndexEndpoint = "http://tools-query.example.com:8000"
	e.reconcile()
	got := worldPorts(e.policy())
	want := map[int32]bool{443: false, 5432: false, 6379: false, 8000: false}
	for _, p := range got {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for p, seen := range want {
		if !seen {
			t.Errorf("port %d is allowed: %v", p, got)
		}
	}
}

func TestGatewayEgressReportsAServiceEndpointThatDoesNotExist(t *testing.T) {
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), false, gwEgressServer("svc", "http://missing.other.svc:9000"))
	scheme := e.r.Scheme
	if err := discoveryv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	e.reconcile()
	if c := e.condition(); c == nil || c.Reason != netpol.ReasonEndpointInvalid {
		t.Errorf("a Service that does not exist is reported: %+v", c)
	}
}
