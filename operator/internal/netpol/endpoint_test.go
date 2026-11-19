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

func TestCheckHTTPEndpoint(t *testing.T) {
	good := []string{
		"https://api.oreilly.com/mcp", "http://tools.example.com:8080/mcp", "https://203.0.113.7/mcp",
		"http://tools.ns.svc:9000",
	}
	for _, raw := range good {
		if err := CheckHTTPEndpoint(raw); err != nil {
			t.Errorf("%s should be usable: %v", raw, err)
		}
	}
	bad := []string{
		"", "api.oreilly.com", "ftp://api.oreilly.com", "nats://nats:4222", "https://", "https:///mcp",
		"https://api.oreilly.com:99999", "https://*.example.com", "https://bad host.example.com", "://x",
	}
	for _, raw := range bad {
		if err := CheckHTTPEndpoint(raw); err == nil {
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
		Rules: &kubemootv1alpha1.EgressRules{}, Defaults: []Dest{{Host: "api.oreilly.com", Port: 443}},
	})
	eq(t, "enforced", res.Status.Enforced, true)
	eq(t, "policy and Cilium policy", len(res.Objects), 2)
}
