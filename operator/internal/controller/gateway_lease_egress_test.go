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
	"errors"
	"reflect"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/netpol"
)

const (
	gwLeaseCrew     = "oguide"
	gwLeaseFallback = "fallback.example.com"
)

// gwLeaseServer is an external server that takes the asking person's own token.
func gwLeaseServer(mode aiv1alpha1.CallerCredentialMode, onForbidden ...string) *aiv1alpha1.MCPServer {
	s := gwEgressServer("oreilly-mcp", "https://"+gwOReilly)
	s.Spec.Registry = &aiv1alpha1.RegistryConfig{CallerCredential: &aiv1alpha1.CallerCredential{
		Mode: mode, Verifier: credVerifier, OnForbidden: onForbidden,
	}}
	return s
}

// gwLeaseCrewOf is a crew in the gateway's namespace whose discussion gateway keeps tokens,
// or does not.
func gwLeaseCrewOf(name string, keepsTokens bool) *aiv1alpha1.Crew {
	return &aiv1alpha1.Crew{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gwEgressNS},
		Spec: aiv1alpha1.CrewSpec{Discussion: &aiv1alpha1.DiscussionConfig{Access: &aiv1alpha1.DiscussionAccess{
			ServiceTokens: []aiv1alpha1.ServiceTokenVerifier{tokenVerifier(keepsTokens)},
		}}},
	}
}

// leaseRules are the egress rules that allow the lease port.
func leaseRules(p *networkingv1.NetworkPolicy) []networkingv1.NetworkPolicyEgressRule {
	var out []networkingv1.NetworkPolicyEgressRule
	for _, rule := range p.Spec.Egress {
		for _, port := range rule.Ports {
			if port.Port.IntVal == netpol.DiscussionGatewayLeasePort {
				out = append(out, rule)
			}
		}
	}
	return out
}

func assertNoLeaseRule(t *testing.T, e *gwEgressEnv) {
	t.Helper()
	e.reconcile()
	p := e.policy()
	if p == nil {
		t.Fatal("the gateway fronts an external server and is restricted")
	}
	if got := leaseRules(p); len(got) != 0 {
		t.Errorf("no rule to the lease port: %+v", got)
	}
}

func TestGatewayEgressAllowsTheLeasePortOfTheCrewThatKeepsTokens(t *testing.T) {
	for _, cilium := range []bool{false, true} {
		e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), cilium,
			gwLeaseServer(aiv1alpha1.CallerCredentialRequired), gwLeaseCrewOf(gwLeaseCrew, true))
		e.reconcile()
		rules := leaseRules(e.policy())
		if len(rules) != 1 {
			t.Fatalf("cilium=%v: one rule to the lease port: %+v", cilium, rules)
		}
		want := []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
			labelComponent: netpol.ComponentDiscussionGateway, labelCrew: gwLeaseCrew,
			netpol.LabelCredentialLease: valueTrue,
		}}}}
		if !reflect.DeepEqual(rules[0].To, want) {
			t.Errorf("cilium=%v: only the crew's token-keeping gateway pods, in the namespace: %+v", cilium, rules[0].To)
		}
		if len(rules[0].Ports) != 1 {
			t.Errorf("cilium=%v: the lease port and no other: %+v", cilium, rules[0].Ports)
		}
		// Host names are enforced only on Cilium; the lease rule does not change that.
		if c := e.condition(); c == nil || (c.Status == metav1.ConditionTrue) != cilium {
			t.Errorf("cilium=%v: the lease rule leaves the condition as the hosts decide: %+v", cilium, c)
		}
	}
}

func TestGatewayEgressHasNoLeaseRuleWithoutAServerInModeRequired(t *testing.T) {
	for name, server := range map[string]*aiv1alpha1.MCPServer{
		"no callerCredential": gwEgressServer("oreilly-mcp", "https://"+gwOReilly),
		"mode Off":            gwLeaseServer(aiv1alpha1.CallerCredentialOff, "https://"+gwLeaseFallback),
	} {
		t.Run(name, func(t *testing.T) {
			e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), true, server, gwLeaseCrewOf(gwLeaseCrew, true))
			assertNoLeaseRule(t, e)
			if hosts := fqdnHosts(fqdnRule(t, e.cli)); len(hosts) != 1 || hosts[0] != gwOReilly {
				t.Errorf("only the server's own host: %v", hosts)
			}
		})
	}
}

func TestGatewayEgressHasNoLeaseRuleWithoutOneCrewThatKeepsTokens(t *testing.T) {
	for name, crews := range map[string][]client.Object{
		"no crew at all":             nil,
		"a crew that keeps no token": {gwLeaseCrewOf(gwLeaseCrew, false)},
		"two crews that keep tokens": {gwLeaseCrewOf(gwLeaseCrew, true), gwLeaseCrewOf("second", true)},
	} {
		t.Run(name, func(t *testing.T) {
			objs := append([]client.Object{gwLeaseServer(aiv1alpha1.CallerCredentialRequired)}, crews...)
			assertNoLeaseRule(t, newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), false, objs...))
		})
	}
}

func TestGatewayEgressHasNoLeaseRuleForAGatewayThatIsNotKubemoots(t *testing.T) {
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), false,
		gwLeaseServer(aiv1alpha1.CallerCredentialRequired, "https://"+gwLeaseFallback), gwLeaseCrewOf(gwLeaseCrew, true))
	e.gw.Spec.Implementation = aiv1alpha1.ImplementationContextForge
	assertNoLeaseRule(t, e)
}

func TestGatewayEgressFollowsTheCrewThatStartsKeepingTokens(t *testing.T) {
	crew := gwLeaseCrewOf(gwLeaseCrew, false)
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), false,
		gwLeaseServer(aiv1alpha1.CallerCredentialRequired), crew)
	assertNoLeaseRule(t, e)

	crew.Spec.Discussion.Access.ServiceTokens[0].PassToTools = true
	if err := e.cli.Update(context.Background(), crew); err != nil {
		t.Fatal(err)
	}
	reqs := e.r.findGatewaysInNamespace(context.Background(), crew)
	if len(reqs) != 1 || reqs[0].Name != gwEgressName {
		t.Fatalf("a crew change reconciles the namespace's gateway: %v", reqs)
	}
	e.reconcile()
	if got := leaseRules(e.policy()); len(got) != 1 {
		t.Errorf("the lease rule appears with the crew's change: %+v", got)
	}
}

func TestGatewayEgressAllowsTheOnForbiddenEndpointsOfAServerInModeRequired(t *testing.T) {
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), true,
		gwLeaseServer(aiv1alpha1.CallerCredentialRequired,
			"https://"+gwLeaseFallback+"/mcp", "https://other.example.com:8443/mcp",
			"http://localhost:9000/mcp", "http://127.0.0.1:9000/mcp", "http://[::1]:9000/mcp"),
		gwLeaseCrewOf(gwLeaseCrew, true))
	e.reconcile()
	assertHostPorts(t, fqdnRule(t, e.cli), map[string]string{
		gwOReilly: "443", gwLeaseFallback: "443", "other.example.com": "8443",
	})
	if got := worldPorts(e.policy()); len(got) != 0 {
		t.Errorf("a loopback endpoint gets no rule: %v", got)
	}
	if c := e.condition(); c == nil || c.Status != metav1.ConditionTrue {
		t.Errorf("every endpoint is enforced: %+v", c)
	}
}

func TestGatewayEgressReportsAnOnForbiddenEndpointItCannotAllow(t *testing.T) {
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), true,
		gwLeaseServer(aiv1alpha1.CallerCredentialRequired, "https://*.example.com/mcp"))
	e.reconcile()
	c := e.condition()
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != netpol.ReasonEndpointInvalid {
		t.Fatalf("the endpoint is reported: %+v", c)
	}
	if hosts := fqdnHosts(fqdnRule(t, e.cli)); len(hosts) != 1 || hosts[0] != gwOReilly {
		t.Errorf("the rest of the list still applies: %v", hosts)
	}
}

func TestGatewayEgressReturnsACrewListErrorAndWritesNoRule(t *testing.T) {
	boom := errors.New("crews unavailable")
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), false, gwLeaseServer(aiv1alpha1.CallerCredentialRequired))
	failing := fake.NewClientBuilder().WithScheme(e.r.Scheme).WithObjects(e.gw, gwLeaseServer(aiv1alpha1.CallerCredentialRequired)).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*aiv1alpha1.CrewList); ok {
					return boom
				}
				return c.List(ctx, list, opts...)
			},
		}).Build()
	e.cli, e.r.Client, e.r.NetPol.Client = failing, failing, failing
	if err := e.r.reconcileEgress(context.Background(), e.gw); !errors.Is(err, boom) {
		t.Fatalf("the list error is returned: %v", err)
	}
	if e.policy() != nil {
		t.Error("no policy is written from a partial list of destinations")
	}
}

func TestGatewayEgressAllowsTheLeaseForAnInClusterServerInModeRequired(t *testing.T) {
	inCluster := gwEgressServer("search", "")
	inCluster.Spec.Registry = &aiv1alpha1.RegistryConfig{CallerCredential: &aiv1alpha1.CallerCredential{
		Mode: aiv1alpha1.CallerCredentialRequired, Verifier: credVerifier,
	}}
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), false,
		gwEgressServer("oreilly-mcp", "https://"+gwOReilly), inCluster, gwLeaseCrewOf(gwLeaseCrew, true))
	e.reconcile()
	if got := leaseRules(e.policy()); len(got) != 1 {
		t.Errorf("the gateway is restricted by its external server and leases for the in-cluster one: %+v", got)
	}
}

func TestGatewayEgressReadsAnOnForbiddenEndpointThatNamesAService(t *testing.T) {
	e := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), false,
		gwLeaseServer(aiv1alpha1.CallerCredentialRequired, "http://helm-reference-query."+gwEgressNS+".svc:8000/mcp"),
		gwQueryService())
	e.reconcile()
	if n := queryPodRules(e.policy()); n != 1 {
		t.Errorf("one rule admits the Service's pods on its port, got %d: %+v", n, e.policy().Spec.Egress)
	}

	missing := newGwEgressEnv(t, gwEgressConfig(netpol.EgressDeclared), false,
		gwLeaseServer(aiv1alpha1.CallerCredentialRequired, "https://missing.other.svc:9000/mcp"))
	missing.reconcile()
	if c := missing.condition(); c == nil || c.Reason != netpol.ReasonEndpointInvalid {
		t.Errorf("an onForbidden Service that does not exist is reported: %+v", c)
	}
}
