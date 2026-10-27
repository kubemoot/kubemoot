/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package netpol

import (
	"reflect"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	testNS           = "crew-a"
	testOperator     = "kubemoot"
	testDNS          = "kube-system"
	testScraperNS    = "obs"
	testCIDR         = "203.0.113.0/24"
	testModelsNS     = "models"
	testMailName     = "mail"
	testPilot        = "pilot"
	testWildcard     = "*.example.org"
	testSMTPHost     = "smtp.example.com"
	testControlPlane = "10.0.0.0/24"
)

func testConfig() Config {
	return Config{
		Enabled: true, Ingress: true, Egress: EgressDeclared, Cilium: CiliumAuto,
		OperatorNamespace: testOperator,
		DNS:               PodRef{Namespace: testDNS, Labels: map[string]string{"k8s-app": "kube-dns"}},
	}
}

func eq(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %#v, want %#v", what, got, want)
	}
}

func asPolicy(t *testing.T, obj client.Object) *networkingv1.NetworkPolicy {
	t.Helper()
	p, ok := obj.(*networkingv1.NetworkPolicy)
	eq(t, "object is a NetworkPolicy", ok, true)
	return p
}

func asCilium(t *testing.T, obj client.Object) *unstructured.Unstructured {
	t.Helper()
	u, ok := obj.(*unstructured.Unstructured)
	eq(t, "object is unstructured", ok, true)
	eq(t, "kind", u.GroupVersionKind(), CiliumPolicyGVK)
	return u
}

func TestConfigFromEnvDefaults(t *testing.T) {
	cfg, warnings := ConfigFromEnv(func(string) string { return "" })
	eq(t, "enabled", cfg.Enabled, false)
	eq(t, "ingress", cfg.Ingress, true)
	eq(t, "egress", cfg.Egress, EgressOff)
	eq(t, "cilium", cfg.Cilium, CiliumAuto)
	eq(t, "dns", cfg.DNS, PodRef{Namespace: testDNS, Labels: map[string]string{"k8s-app": "kube-dns"}})
	eq(t, "operator namespace", cfg.OperatorNamespace, testOperator)
	eq(t, "metrics scraper", cfg.Metrics, PodRef{})
	eq(t, "warnings", len(warnings), 0)
}

func TestConfigFromEnvEverythingSet(t *testing.T) {
	env := map[string]string{
		EnvEnabled: "true", EnvIngress: "false", EnvEgressMode: "Declared", EnvCilium: "off",
		EnvOperatorNS: "ops", EnvDNSNamespace: "dns", EnvDNSLabels: "a=b, c=d",
		EnvMetricsNS: testScraperNS, EnvMetricsLabels: "x=y", EnvTrustedIngress: "envoy, ,istio",
		EnvControlPlane: testControlPlane, EnvClusterCIDRs: testCIDR,
	}
	cfg, warnings := ConfigFromEnv(func(k string) string { return env[k] })
	eq(t, "enabled", cfg.Enabled, true)
	eq(t, "ingress", cfg.Ingress, false)
	eq(t, "egress", cfg.Egress, EgressDeclared)
	eq(t, "cilium", cfg.Cilium, CiliumOff)
	eq(t, "operator namespace", cfg.OperatorNamespace, "ops")
	eq(t, "dns labels", cfg.DNS.Labels, map[string]string{"a": "b", "c": "d"})
	eq(t, "metrics", cfg.Metrics, PodRef{Namespace: testScraperNS, Labels: map[string]string{"x": "y"}})
	eq(t, "trusted namespaces", cfg.TrustedIngressNamespaces, []string{"envoy", "istio"})
	eq(t, "control plane", cfg.ControlPlaneCIDRs, []string{testControlPlane})
	eq(t, "cluster ranges", cfg.ClusterCIDRs, []string{testCIDR})
	eq(t, "warnings", len(warnings), 0)
}

func TestConfigFromEnvBadValues(t *testing.T) {
	deny, w1 := ConfigFromEnv(func(k string) string { return map[string]string{EnvEgressMode: "Deny"}[k] })
	eq(t, "Deny turns egress off", deny.Egress, EgressOff)
	eq(t, "Deny warns", len(w1), 1)

	odd, w2 := ConfigFromEnv(func(k string) string { return map[string]string{EnvCilium: "maybe"}[k] })
	eq(t, "unknown cilium means auto", odd.Cilium, CiliumAuto)
	eq(t, "unknown cilium warns", len(w2), 1)

	labels, _ := ConfigFromEnv(func(k string) string { return map[string]string{EnvDNSLabels: "novalue,k=v"}[k] })
	eq(t, "a malformed label is dropped", labels.DNS.Labels, map[string]string{"k": "v"})
}

func TestConfigSwitches(t *testing.T) {
	c := testConfig()
	eq(t, "ingress on", c.IngressOn(), true)
	eq(t, "egress on", c.EgressOn(), true)
	c.Enabled = false
	eq(t, "disabled: ingress", c.IngressOn(), false)
	eq(t, "disabled: egress", c.EgressOn(), false)
	eq(t, "zero value ingress", Config{}.IngressOn(), false)
	eq(t, "zero value egress", Config{}.EgressOn(), false)
}

func TestDestsFromURL(t *testing.T) {
	tests := map[string]struct {
		in   string
		want []Dest
	}{
		"service with namespace and port":         {"http://vllm.models:8000", []Dest{{Namespace: testModelsNS, Port: 8000}}},
		"service in the workload's own namespace": {"http://ollama:11434", []Dest{{SameNamespace: true, Port: 11434}}},
		"fully qualified service":                 {"http://ollama.ollama.svc.cluster.local:11434", []Dest{{Namespace: "ollama", Port: 11434}}},
		"service with svc suffix":                 {"nats://nats.nats.svc:4222", []Dest{{Namespace: "nats", Port: 4222}}},
		"external host":                           {"https://api.example.com/v1", []Dest{{Host: "api.example.com", Port: 443}}},
		"nats default port":                       {"nats://nats.example.org", []Dest{{Host: "nats.example.org", Port: 4222}}},
		"ip address":                              {"http://10.1.2.3:9000", []Dest{{CIDR: "10.1.2.3/32", Port: 9000}}},
		"ipv6 address":                            {"http://[2001:db8::1]:9000", []Dest{{CIDR: "2001:db8::1/128", Port: 9000}}},
		"list":                                    {"nats://a.b.c:1, nats://d.e.f:2", []Dest{{Host: "a.b.c", Port: 1}, {Host: "d.e.f", Port: 2}}},
		"empty":                                   {"", []Dest{}},
		"no port and unknown":                     {"foo://bar.baz.qux", []Dest{}},
		"no host":                                 {"http://:80", []Dest{}},
		"port out of range":                       {"http://a.b.c:70000", []Dest{}},
		"unparsable":                              {"http://a b.c:80", []Dest{}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) { eq(t, "dests of "+tt.in, DestsFromURL(tt.in), tt.want) })
	}
}

func TestBaseline(t *testing.T) {
	cfg := testConfig()
	cfg.Metrics = PodRef{Namespace: testScraperNS, Labels: map[string]string{"app": "prom"}}
	objs := Baseline(cfg, testNS)
	eq(t, "object count", len(objs), 2)
	deny, internal := asPolicy(t, objs[0]), asPolicy(t, objs[1])
	eq(t, "deny name", deny.Name, "km-default-deny")
	eq(t, "deny selects every pod", deny.Spec.PodSelector, metav1.LabelSelector{})
	eq(t, "deny has no rule", len(deny.Spec.Ingress), 0)
	eq(t, "deny types", deny.Spec.PolicyTypes, []networkingv1.PolicyType{networkingv1.PolicyTypeIngress})
	eq(t, "internal name", internal.Name, "km-crew-internal")
	eq(t, "internal peers: the namespace, the operator namespace, the scraper", len(internal.Spec.Ingress[0].From), 3)
	eq(t, "internal leaves tool servers and gateways to their own policies", internal.Spec.PodSelector.MatchExpressions,
		[]metav1.LabelSelectorRequirement{{
			Key: labelComponent, Operator: metav1.LabelSelectorOpNotIn,
			Values: []string{ComponentMCPServer, ComponentMCPGateway},
		}})
}

func TestGeneratedObjectsCarryTheLabelAndNamespace(t *testing.T) {
	cfg := testConfig().WithCilium(true)
	all := make([]client.Object, 0, 8)
	all = append(all, Baseline(cfg, testNS)...)
	all = append(all, MCPServerIngress(cfg, MCPServer{PodName: testMailName, Namespace: testNS, Port: 1})...)
	all = append(all, DiscussionGatewayIngress(cfg, GatewayProxy{Crew: testPilot, Namespace: testNS})...)
	all = append(all, Egress(cfg, egressWorkload(&kubemootv1alpha1.EgressRules{AllowedHosts: []string{testSMTPHost}})).Objects...)
	eq(t, "object count", len(all), 7)
	for _, o := range all {
		eq(t, o.GetName()+" label", o.GetLabels()[LabelGeneratedBy], GeneratedByValue)
		eq(t, o.GetName()+" namespace", o.GetNamespace(), testNS)
		eq(t, o.GetName()+" prefix", o.GetName()[:len(NamePrefix)], NamePrefix)
	}
}

func TestIngressFlagsOff(t *testing.T) {
	disabled, noIngress := testConfig(), testConfig()
	disabled.Enabled = false
	noIngress.Ingress = false
	for name, cfg := range map[string]Config{"disabled": disabled, "ingress off": noIngress, "zero value": {}} {
		t.Run(name, func(t *testing.T) {
			eq(t, "baseline", len(Baseline(cfg, testNS)), 0)
			eq(t, "tool server", len(MCPServerIngress(cfg, MCPServer{PodName: "a", Namespace: testNS, Port: 1})), 0)
			eq(t, "gateway", len(DiscussionGatewayIngress(cfg, GatewayProxy{Crew: "c", Namespace: testNS})), 0)
		})
	}
}

func TestMCPServerIngress(t *testing.T) {
	p := asPolicy(t, MCPServerIngress(testConfig(), MCPServer{PodName: testMailName, Namespace: testNS, Port: 3000})[0])
	eq(t, "name", p.Name, "km-mcp-mail-ingress")
	eq(t, "selector", p.Spec.PodSelector.MatchLabels, map[string]string{
		labelName: testMailName, labelInstance: testMailName, labelComponent: ComponentMCPServer,
	})
	eq(t, "types", p.Spec.PolicyTypes, []networkingv1.PolicyType{networkingv1.PolicyTypeIngress})
	rule := p.Spec.Ingress[0]
	eq(t, "peer count", len(rule.From), 2)
	eq(t, "the crew's own gateway first, same namespace only", rule.From[0],
		networkingv1.NetworkPolicyPeer{PodSelector: podsWith(map[string]string{labelComponent: ComponentMCPGateway})})
	eq(t, "the operator second", rule.From[1].NamespaceSelector.MatchLabels, map[string]string{namespaceNameLabel: testOperator})
	eq(t, "the operator pod", rule.From[1].PodSelector.MatchLabels, map[string]string{controlPlaneLabel: controllerManager})
	eq(t, "port", rule.Ports[0].Port.IntVal, int32(3000))
}

func TestMCPServerIngressSharedServerAdmitsEveryCrew(t *testing.T) {
	p := asPolicy(t, MCPServerIngress(testConfig(), MCPServer{PodName: "github", Namespace: testOperator, Port: 8080})[0])
	gw := p.Spec.Ingress[0].From[0]
	eq(t, "crew namespaces", gw.NamespaceSelector.MatchExpressions, []metav1.LabelSelectorRequirement{{
		Key: LabelCrew, Operator: metav1.LabelSelectorOpExists,
	}})
	eq(t, "gateway pods", gw.PodSelector.MatchLabels, map[string]string{labelComponent: ComponentMCPGateway})
}

func TestMCPServerIngressUsesTheBoundedPodName(t *testing.T) {
	bounded := "a-very-long-server-name-that-the-operator-bounds-to-sixty-3-ab12cd34"
	p := asPolicy(t, MCPServerIngress(testConfig(), MCPServer{PodName: bounded, Namespace: testNS, Port: 1})[0])
	eq(t, "instance label", p.Spec.PodSelector.MatchLabels[labelInstance], bounded)
	eq(t, "name", p.Name, NamePrefix+"mcp-"+bounded+"-ingress")
}

func TestDiscussionGatewayIngress(t *testing.T) {
	cfg := testConfig()
	cfg.TrustedIngressNamespaces = []string{"envoy"}
	cfg.ControlPlaneCIDRs = []string{testControlPlane}
	allowed := []metav1.LabelSelector{{MatchLabels: map[string]string{"team": testPilot}}}
	objs := DiscussionGatewayIngress(cfg, GatewayProxy{Crew: testPilot, Namespace: testNS, Allowed: allowed})
	eq(t, "without Cilium only the NetworkPolicy", len(objs), 1)
	p := asPolicy(t, objs[0])
	eq(t, "name", p.Name, "km-crew-pilot-gateway-ingress")
	eq(t, "selector", p.Spec.PodSelector.MatchLabels, map[string]string{labelComponent: ComponentDiscussionGateway, LabelCrew: testPilot})
	from := p.Spec.Ingress[0].From
	eq(t, "peers: listed namespace, trusted namespace, control plane range", len(from), 3)
	eq(t, "listed namespace", from[0].NamespaceSelector, &allowed[0])
	eq(t, "control plane", from[2].IPBlock, &networkingv1.IPBlock{CIDR: testControlPlane})
	eq(t, "port", p.Spec.Ingress[0].Ports[0].Port.IntVal, int32(DiscussionGatewayPort))
}

func TestDiscussionGatewayIngressWithCiliumAddsEntities(t *testing.T) {
	objs := DiscussionGatewayIngress(testConfig().WithCilium(true), GatewayProxy{Crew: testPilot, Namespace: testNS})
	eq(t, "object count", len(objs), 2)
	u := asCilium(t, objs[1])
	eq(t, "name", u.GetName(), "km-crew-pilot-gateway-entities")
	rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "ingress")
	eq(t, "entities", rules[0].(map[string]any)["fromEntities"], []any{"ingress", "kube-apiserver", "remote-node"})
}

func TestDiscussionGatewayIngressWithoutPeersAdmitsNoOutsider(t *testing.T) {
	p := asPolicy(t, DiscussionGatewayIngress(testConfig(), GatewayProxy{Crew: "c", Namespace: testNS})[0])
	eq(t, "rules", len(p.Spec.Ingress), 0)
}

func TestGatewayToolPort(t *testing.T) {
	cfg := testConfig()
	cfg.Metrics = PodRef{Namespace: testScraperNS}
	rule := GatewayToolPort(cfg, 8080)
	eq(t, "peers", len(rule.From), 3)
	eq(t, "port", rule.Ports[0].Port.IntVal, int32(8080))
	eq(t, "the policy's own namespace first", rule.From[0], networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}})
}

func egressWorkload(rules *kubemootv1alpha1.EgressRules) Workload {
	return Workload{Name: "mcp-" + testMailName, Namespace: testNS, Selector: map[string]string{labelInstance: testMailName}, Rules: rules}
}

func TestEgressNothingWhenNotDeclared(t *testing.T) {
	r := Egress(testConfig(), egressWorkload(nil))
	eq(t, "objects", len(r.Objects), 0)
	eq(t, "applicable", r.Status.Applicable, false)
}

func TestEgressNothingWhenOffOrDisabled(t *testing.T) {
	off, disabled := testConfig(), testConfig()
	off.Egress = EgressOff
	disabled.Enabled = false
	eq(t, "mode Off", len(Egress(off, egressWorkload(&kubemootv1alpha1.EgressRules{})).Objects), 0)
	eq(t, "flag off", len(Egress(disabled, egressWorkload(&kubemootv1alpha1.EgressRules{})).Objects), 0)
}

func TestEgressCIDRsAreEnforcedOnAnyCNI(t *testing.T) {
	rules := &kubemootv1alpha1.EgressRules{
		AllowedCIDRs: []kubemootv1alpha1.EgressCIDR{{CIDR: testCIDR, Except: []string{"203.0.113.128/25"}}},
		Ports:        []int32{587, 587, 465},
	}
	r := Egress(testConfig(), egressWorkload(rules))
	eq(t, "status", r.Status, EgressStatus{Applicable: true, Enforced: true, Reason: ReasonEnforced, Message: r.Status.Message})
	eq(t, "object count", len(r.Objects), 1)
	p := asPolicy(t, r.Objects[0])
	eq(t, "name", p.Name, "km-mcp-mail-egress")
	eq(t, "types", p.Spec.PolicyTypes, []networkingv1.PolicyType{networkingv1.PolicyTypeEgress})
	eq(t, "rules: DNS and the range", len(p.Spec.Egress), 2)
	eq(t, "DNS ports", len(p.Spec.Egress[0].Ports), 2)
	eq(t, "DNS namespace", p.Spec.Egress[0].To[0].NamespaceSelector.MatchLabels, map[string]string{namespaceNameLabel: testDNS})
	eq(t, "range", p.Spec.Egress[1].To[0].IPBlock, &networkingv1.IPBlock{CIDR: testCIDR, Except: []string{"203.0.113.128/25"}})
	eq(t, "ports are sorted and unique", []int32{p.Spec.Egress[1].Ports[0].Port.IntVal, p.Spec.Egress[1].Ports[1].Port.IntVal}, []int32{465, 587})
	eq(t, "no third port", len(p.Spec.Egress[1].Ports), 2)
}

func TestEgressHostsWithoutCiliumFallBackToTheInternet(t *testing.T) {
	cfg := testConfig()
	cfg.ClusterCIDRs = []string{testCIDR, "2001:db8::/32", "not-a-cidr"}
	r := Egress(cfg, egressWorkload(&kubemootv1alpha1.EgressRules{AllowedHosts: []string{testSMTPHost, testWildcard}}))
	eq(t, "enforced", r.Status.Enforced, false)
	eq(t, "applicable", r.Status.Applicable, true)
	eq(t, "reason", r.Status.Reason, ReasonFQDNNotSupported)
	eq(t, "only the NetworkPolicy", len(r.Objects), 1)
	rs := asPolicy(t, r.Objects[0]).Spec.Egress
	v4, v6 := rs[len(rs)-2], rs[len(rs)-1]
	eq(t, "v4 range", v4.To[0].IPBlock.CIDR, "0.0.0.0/0")
	eq(t, "v6 range", v6.To[0].IPBlock.CIDR, "::/0")
	eq(t, "v4 keeps private ranges and the cluster range blocked", v4.To[0].IPBlock.Except,
		append(append([]string(nil), privateV4...), testCIDR))
	eq(t, "v6 keeps private ranges and the cluster range blocked", v6.To[0].IPBlock.Except,
		append(append([]string(nil), privateV6...), "2001:db8::/32"))
	eq(t, "default port", v4.Ports[0].Port.IntVal, int32(DefaultEgressPort))
}

func TestEgressHostsWithCiliumUseFQDNRules(t *testing.T) {
	rules := &kubemootv1alpha1.EgressRules{
		AllowedHosts: []string{testSMTPHost, testWildcard}, Ports: []int32{587}, APIServer: true,
	}
	r := Egress(testConfig().WithCilium(true), egressWorkload(rules))
	eq(t, "enforced", r.Status.Enforced, true)
	eq(t, "object count", len(r.Objects), 2)
	eq(t, "the NetworkPolicy carries only DNS", len(asPolicy(t, r.Objects[0]).Spec.Egress), 1)
	u := asCilium(t, r.Objects[1])
	eq(t, "name", u.GetName(), "km-mcp-mail-egress-fqdn")
	egress, _, _ := unstructured.NestedSlice(u.Object, "spec", "egress")
	eq(t, "rules: DNS, hosts, API server entity", len(egress), 3)
	eq(t, "hosts", egress[1].(map[string]any)["toFQDNs"], []any{
		map[string]any{"matchPattern": testWildcard}, map[string]any{"matchName": testSMTPHost},
	})
	eq(t, "API server entity", egress[2], map[string]any{"toEntities": []any{"kube-apiserver"}})
	eq(t, "deep copy works on the document", u.DeepCopy().Object, u.Object)
}

func TestEgressAPIServerOnlyOnCiliumUsesAnEntity(t *testing.T) {
	r := Egress(testConfig().WithCilium(true), egressWorkload(&kubemootv1alpha1.EgressRules{APIServer: true}))
	eq(t, "object count", len(r.Objects), 2)
	eq(t, "enforced", r.Status.Enforced, true)
}

func TestEgressAPIServerWithoutCiliumUsesEndpoints(t *testing.T) {
	w := egressWorkload(&kubemootv1alpha1.EgressRules{APIServer: true})
	w.APIServer = []APIServerEndpoint{{CIDR: "10.0.0.1/32", Port: 6443}}
	rs := asPolicy(t, Egress(testConfig(), w).Objects[0]).Spec.Egress
	last := rs[len(rs)-1]
	eq(t, "address", last.To[0].IPBlock.CIDR, "10.0.0.1/32")
	eq(t, "port", last.Ports[0].Port.IntVal, int32(6443))
}

func TestEgressDefaultsAreMergedAndDeduplicated(t *testing.T) {
	w := egressWorkload(&kubemootv1alpha1.EgressRules{})
	w.Defaults = []Dest{
		{Namespace: "nats", Port: 4222},
		{Namespace: testModelsNS, Port: 8000},
		{Namespace: testModelsNS, Port: 8001},
		{Namespace: testModelsNS, Port: 8000},
		{SameNamespace: true, PodLabels: map[string]string{labelComponent: ComponentMCPGateway}, Port: 8080},
		{CIDR: "10.9.9.9/32", Port: 11434},
		{Host: "models.example.com", Port: 443},
	}
	r := Egress(testConfig().WithCilium(true), w)
	rs := asPolicy(t, r.Objects[0]).Spec.Egress
	eq(t, "rules: DNS, nats, models, gateway, the address", len(rs), 5)
	eq(t, "models ports", len(rs[2].Ports), 2)
	eq(t, "the gateway rule selects pods of the workload's namespace", rs[3].To[0],
		networkingv1.NetworkPolicyPeer{PodSelector: podsWith(map[string]string{labelComponent: ComponentMCPGateway})})
	eq(t, "a derived external host is enforced through Cilium", len(r.Objects), 2)
}

func TestEgressIsDeterministic(t *testing.T) {
	rules := &kubemootv1alpha1.EgressRules{AllowedHosts: []string{"b.example.com", "a.example.com", "c.example.org"}, Ports: []int32{443, 8443}}
	first := Egress(testConfig().WithCilium(true), egressWorkload(rules))
	for i := 0; i < 20; i++ {
		again := Egress(testConfig().WithCilium(true), egressWorkload(rules))
		eq(t, "build", again.Objects[1].(*unstructured.Unstructured).Object, first.Objects[1].(*unstructured.Unstructured).Object)
	}
}
