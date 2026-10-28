/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package netpol

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	testPromName = "prometheus"
	testPromNS   = "observability"
	testHTTPName = "http"
	testAppKey   = "app"
	testNATS     = "nats"
)

func testService(ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: testPromName, Namespace: testPromNS},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{testAppKey: testPromName}, Ports: ports},
	}
}

func int32Ptr(v int32) *int32 { return &v }

func strPtr(v string) *string { return &v }

func TestServiceDestsUsesTheTargetPortAndTheSelector(t *testing.T) {
	svc := testService(corev1.ServicePort{Port: 9090, TargetPort: intstr.FromInt32(9091)})
	got, err := ServiceDests(svc, nil, nil)
	eq(t, "error", err, nil)
	eq(t, "dests", got, []Dest{{Namespace: testPromNS, PodLabels: map[string]string{testAppKey: testPromName}, Port: 9091}})
}

func TestServiceDestsTargetPortDefaultsToThePort(t *testing.T) {
	got, err := ServiceDests(testService(corev1.ServicePort{Port: 8000}), nil, []int32{8000})
	eq(t, "error", err, nil)
	eq(t, "port", got[0].Port, int32(8000))
}

func TestServiceDestsFiltersToTheWantedPortsOnly(t *testing.T) {
	svc := testService(
		corev1.ServicePort{Port: 80, TargetPort: intstr.FromInt32(8080)},
		corev1.ServicePort{Port: 443, TargetPort: intstr.FromInt32(8443)},
		corev1.ServicePort{Port: 9000, TargetPort: intstr.FromInt32(9001)},
	)
	got, err := ServiceDests(svc, nil, []int32{80, 443})
	eq(t, "error", err, nil)
	eq(t, "ports", []int32{got[0].Port, got[1].Port}, []int32{8080, 8443})
	eq(t, "count", len(got), 2)

	all, err := ServiceDests(svc, nil, nil)
	eq(t, "error", err, nil)
	eq(t, "no ports named means every port", len(all), 3)
}

func TestServiceDestsSkipsNonTCPPorts(t *testing.T) {
	svc := testService(
		corev1.ServicePort{Port: 53, Protocol: corev1.ProtocolUDP},
		corev1.ServicePort{Port: 9090, Protocol: corev1.ProtocolTCP},
	)
	got, err := ServiceDests(svc, nil, nil)
	eq(t, "error", err, nil)
	eq(t, "count", len(got), 1)
	_, err = ServiceDests(svc, nil, []int32{53})
	eq(t, "a UDP port is not offered", err != nil, true)
}

func TestServiceDestsResolvesANamedTargetPortFromEndpointSlices(t *testing.T) {
	svc := testService(corev1.ServicePort{Name: testHTTPName, Port: 80, TargetPort: intstr.FromString("web")})
	slices := []discoveryv1.EndpointSlice{{Ports: []discoveryv1.EndpointPort{{Name: strPtr(testHTTPName), Port: int32Ptr(8081)}}}}
	got, err := ServiceDests(svc, slices, nil)
	eq(t, "error", err, nil)
	eq(t, "port", got[0].Port, int32(8081))
}

func TestServiceDestsFailsOnANamedTargetPortWithNoEndpoint(t *testing.T) {
	svc := testService(corev1.ServicePort{Name: testHTTPName, Port: 80, TargetPort: intstr.FromString("web")})
	_, err := ServiceDests(svc, nil, nil)
	eq(t, "error", err != nil && strings.Contains(err.Error(), "named port"), true)
}

func TestServiceDestsFailures(t *testing.T) {
	selectorless := testService(corev1.ServicePort{Port: 80})
	selectorless.Spec.Selector = nil
	tests := map[string]struct {
		svc   *corev1.Service
		ports []int32
		want  string
	}{
		"no pod selector":      {selectorless, nil, "no pod selector"},
		"port not exposed":     {testService(corev1.ServicePort{Port: 80}), []int32{81}, "does not expose"},
		"service with no port": {testService(), nil, "exposes no TCP port"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := ServiceDests(tt.svc, nil, tt.ports)
			eq(t, "no destinations", got, []Dest(nil))
			eq(t, "error mentions "+tt.want, err != nil && strings.Contains(err.Error(), tt.want), true)
		})
	}
}

func TestServiceDestsDoesNotAliasTheServiceSelector(t *testing.T) {
	svc := testService(corev1.ServicePort{Port: 80})
	got, _ := ServiceDests(svc, nil, nil)
	got[0].PodLabels[testAppKey] = "changed"
	eq(t, "service selector", svc.Spec.Selector[testAppKey], testPromName)
}

func TestServiceFromURL(t *testing.T) {
	tests := map[string]struct {
		in   string
		want ServiceRef
		ok   bool
	}{
		"namespace form":    {"http://docs-query.crew:8000", ServiceRef{Namespace: "crew", Name: "docs-query", Port: 8000}, true},
		"svc form":          {"http://ps.kubemoot.svc:8080", ServiceRef{Namespace: "kubemoot", Name: "ps", Port: 8080}, true},
		"fully qualified":   {"nats://nats.nats.svc.cluster.local:4222", ServiceRef{Namespace: testNATS, Name: testNATS, Port: 4222}, true},
		"own namespace":     {"http://ollama:11434", ServiceRef{Namespace: testNS, Name: "ollama", Port: 11434}, true},
		"external host":     {"https://api.example.com/v1", ServiceRef{}, false},
		"address":           {"http://10.1.2.3:9000", ServiceRef{}, false},
		"no port or scheme": {"foo://a.b", ServiceRef{}, false},
		"unparsable":        {"http://a b.c:80", ServiceRef{}, false},
		"empty":             {"", ServiceRef{}, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, ok := ServiceFromURL(tt.in, testNS)
			eq(t, "ok", ok, tt.ok)
			eq(t, "ref", got, tt.want)
		})
	}
}

func TestEgressRuleForAResolvedServiceNamesTheNamespaceAndPods(t *testing.T) {
	w := egressWorkload(&kubemootv1alpha1.EgressRules{})
	w.Services = []Dest{{Namespace: testPromNS, PodLabels: map[string]string{testAppKey: testPromName}, Port: 9091}}
	r := Egress(testConfig().WithCilium(true), w)
	rs := asPolicy(t, r.Objects[0]).Spec.Egress
	eq(t, "rules: DNS and the Service", len(rs), 2)
	eq(t, "peer", rs[1].To[0], networkingv1.NetworkPolicyPeer{
		NamespaceSelector: namespaceNamed(testPromNS), PodSelector: podsWith(map[string]string{testAppKey: testPromName}),
	})
	eq(t, "port", rs[1].Ports[0].Port.IntVal, int32(9091))
	eq(t, "no host rule is needed, so no Cilium object", len(r.Objects), 1)
	eq(t, "enforced", r.Status.Enforced, true)
}

func TestEgressReportsAnUnresolvedService(t *testing.T) {
	w := egressWorkload(&kubemootv1alpha1.EgressRules{})
	w.Unresolved = []string{"service obs/prom does not exist"}
	st := Egress(testConfig(), w).Status
	eq(t, "enforced", st.Enforced, false)
	eq(t, "reason", st.Reason, ReasonServiceNotResolved)
	eq(t, "message", strings.Contains(st.Message, "obs/prom"), true)
}

func TestEgressUnresolvedServiceKeepsTheFQDNNote(t *testing.T) {
	w := egressWorkload(&kubemootv1alpha1.EgressRules{AllowedHosts: []string{testSMTPHost}})
	w.Unresolved = []string{"service obs/prom does not exist"}
	st := Egress(testConfig(), w).Status
	eq(t, "reason", st.Reason, ReasonServiceNotResolved)
	eq(t, "message keeps the host note", strings.Contains(st.Message, "host names"), true)
}

func TestEgressSameNamespaceDestinationWithoutLabelsGetsASelector(t *testing.T) {
	w := egressWorkload(&kubemootv1alpha1.EgressRules{})
	w.Defaults = DestsFromURL(testNATS + "://nats:4222")
	rs := asPolicy(t, Egress(testConfig(), w).Objects[0]).Spec.Egress
	eq(t, "peer", rs[1].To[0], networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}})
}

func TestServiceDestsAllowsEveryNumberANamedPortHas(t *testing.T) {
	svc := testService(corev1.ServicePort{Name: testHTTPName, Port: 80, TargetPort: intstr.FromString("web")})
	slices := []discoveryv1.EndpointSlice{
		{Ports: []discoveryv1.EndpointPort{{Name: strPtr(testHTTPName), Port: int32Ptr(8081)}}},
		{Ports: []discoveryv1.EndpointPort{{Name: strPtr(testHTTPName), Port: int32Ptr(8082)}, {Name: strPtr("other"), Port: int32Ptr(9)}}},
	}
	got, err := ServiceDests(svc, slices, nil)
	eq(t, "error", err, nil)
	eq(t, "ports", []int32{got[0].Port, got[1].Port}, []int32{8081, 8082})
}
