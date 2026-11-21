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

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	hostOReilly = "api.oreilly.com"
	hostTools   = "tools.example.com"
	svcTools    = "tools"
	hostDB      = "db.example.com"
)

func TestParseExternalEndpointReadsHostsAddressesAndServices(t *testing.T) {
	cases := []struct {
		raw     string
		host    string
		cidr    string
		port    int32
		service *ServiceRef
	}{
		{raw: "https://api.oreilly.com/mcp", host: hostOReilly, port: 443},
		{raw: "http://tools.example.com:8080/mcp", host: hostTools, port: 8080},
		{raw: "http://tools.example.com/mcp", host: hostTools, port: 80},
		{raw: "https://example.com", host: "example.com", port: 443},
		{raw: "https://github.com/mcp", host: "github.com", port: 443},
		{raw: "https://x.kube-system", host: "x.kube-system", port: 443},
		{raw: "https://API.Example.COM./mcp", host: "api.example.com", port: 443},
		{raw: "https://user:pass@tools.example.com/mcp", host: hostTools, port: 443},
		{raw: "https://203.0.113.7/mcp", cidr: "203.0.113.7/32", port: 443},
		{raw: "https://[2001:db8::1]:8443/mcp", cidr: "2001:db8::1/128", port: 8443},
		{raw: "http://tools:9000", service: &ServiceRef{Namespace: "crew-a", Name: svcTools, Port: 9000}},
		{raw: "http://tools.ns.svc:9000", service: &ServiceRef{Namespace: "ns", Name: svcTools, Port: 9000}},
		{raw: "http://tools.ns.svc.cluster.local", service: &ServiceRef{Namespace: "ns", Name: svcTools, Port: 80}},
	}
	for _, c := range cases {
		ep, err := ParseExternalEndpoint(c.raw, testNS)
		if err != nil {
			t.Errorf("%s should be usable: %v", c.raw, err)
			continue
		}
		if c.service != nil {
			eq(t, c.raw+" service", ep.Service, c.service)
			continue
		}
		eq(t, c.raw+" is not a service", ep.Service == nil, true)
		eq(t, c.raw+" destination", ep.Dest, Dest{Host: c.host, CIDR: c.cidr, Port: c.port})
	}
}

func TestParseExternalEndpointReportsWhatIsNotUsable(t *testing.T) {
	bad := []string{
		"", hostOReilly, "ftp://api.oreilly.com", "nats://nats:4222", "https://", "https:///mcp",
		"https://api.oreilly.com:99999", "https://*.example.com", "https://bad host.example.com", "://x",
	}
	for _, raw := range bad {
		if _, err := ParseExternalEndpoint(raw, testNS); err == nil {
			t.Errorf("%q must be reported, not allowed", raw)
		}
	}
}

func TestEgressReportsInvalidEndpointsAndGrantsNothingForThem(t *testing.T) {
	w := Workload{
		Name: "gateway-gw", Namespace: testNS, Selector: map[string]string{testAppKey: "gw"},
		Rules:   &kubemootv1alpha1.EgressRules{},
		Invalid: []string{"MCPServer crew-a/bad externalEndpoint: \"x\" must be an http or https URL"},
	}
	res := Egress(testConfig(), w)
	if res.Status.Enforced || res.Status.Reason != ReasonEndpointInvalid || !res.Status.Applicable {
		t.Fatalf("an invalid endpoint must be reported: %+v", res.Status)
	}
	if !strings.Contains(res.Status.Message, "crew-a/bad") {
		t.Errorf("the message names the server: %s", res.Status.Message)
	}
	policy := asPolicy(t, res.Objects[0])
	eq(t, "only the DNS rule", len(policy.Spec.Egress), 1)
}

func TestEgressWithNoInvalidEndpointsStaysEnforced(t *testing.T) {
	res := Egress(testConfig().WithCilium(true), Workload{
		Name: "gateway-gw", Namespace: testNS, Selector: map[string]string{testAppKey: "gw"},
		Rules: &kubemootv1alpha1.EgressRules{}, Defaults: []Dest{{Host: hostOReilly, Port: 443}},
	})
	eq(t, "enforced", res.Status.Enforced, true)
	eq(t, "policy and Cilium policy", len(res.Objects), 2)
}

func TestParseEndpointUsesTheSameHostRuleAndSchemeDefaults(t *testing.T) {
	cases := []struct {
		raw  string
		want ExternalEndpoint
	}{
		{"redis://cache.example.com", ExternalEndpoint{Dest: Dest{Host: "cache.example.com", Port: 6379}}},
		{"rediss://cache.example.com", ExternalEndpoint{Dest: Dest{Host: "cache.example.com", Port: 6379}}},
		{"postgres://u:p@db.example.com/forge", ExternalEndpoint{Dest: Dest{Host: hostDB, Port: 5432}}},
		{"postgresql://db.example.com:6432/forge", ExternalEndpoint{Dest: Dest{Host: hostDB, Port: 6432}}},
		{"postgresql://example.com/forge", ExternalEndpoint{Dest: Dest{Host: "example.com", Port: 5432},
			MaybeService: &ServiceRef{Namespace: "com", Name: "example", Port: 5432}}},
		{"redis://x.kube-system:6379", ExternalEndpoint{Dest: Dest{Host: "x.kube-system", Port: 6379},
			MaybeService: &ServiceRef{Namespace: "kube-system", Name: "x", Port: 6379}}},
		{"postgres://10.1.2.3/forge", ExternalEndpoint{Dest: Dest{CIDR: "10.1.2.3/32", Port: 5432}}},
		{"redis://cache.ns.svc", ExternalEndpoint{Service: &ServiceRef{Namespace: "ns", Name: "cache", Port: 6379}}},
		{"redis://cache", ExternalEndpoint{Service: &ServiceRef{Namespace: testNS, Name: "cache", Port: 6379}}},
	}
	for _, c := range cases {
		got, err := ParseEndpoint(c.raw, testNS)
		if err != nil {
			t.Errorf("%s: %v", c.raw, err)
			continue
		}
		eq(t, c.raw, got, c.want)
	}
}

func TestParseEndpointReportsAPortItCannotDetermine(t *testing.T) {
	for _, raw := range []string{"mysql://db.example.com/forge", "amqp://mq.example.com", "db.example.com", "redis://"} {
		if _, err := ParseEndpoint(raw, testNS); err == nil {
			t.Errorf("%q has no determinable port or host and must be reported", raw)
		}
	}
}

func TestParseExternalEndpointStillRefusesOtherSchemes(t *testing.T) {
	if _, err := ParseExternalEndpoint("redis://cache.example.com", testNS); err == nil {
		t.Error("an external tool server endpoint is http or https")
	}
}
