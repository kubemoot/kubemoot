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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/netpol"
)

func TestReconcileAdminAccessWithoutAdminPortNarrowsTheToolPortWhateverTheFlag(t *testing.T) {
	ctx := context.Background()
	scheme := gatewayScheme(t)
	gw := &aiv1alpha1.MCPGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: npCrewA, UID: "gw-uid"},
		Spec:       aiv1alpha1.MCPGatewaySpec{Implementation: aiv1alpha1.ImplementationDocker, Port: 8080},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gw).Build()
	key := types.NamespacedName{Namespace: npCrewA, Name: adminTestPolicy}

	for name, r := range map[string]*MCPGatewayReconciler{
		"no network policy setting": {Client: cli, Scheme: scheme},
		"flag off": {Client: cli, Scheme: scheme, NetPol: &NetPolicySync{
			Config: netpol.Config{OperatorNamespace: defaultOperatorNamespace}}},
		"flag on": {Client: cli, Scheme: scheme, NetPol: &NetPolicySync{
			Config: netpol.Config{Enabled: true, Ingress: true, OperatorNamespace: defaultOperatorNamespace}}},
	} {
		if err := r.reconcileAdminAccess(ctx, gw); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		p := &networkingv1.NetworkPolicy{}
		if err := cli.Get(ctx, key, p); err != nil {
			t.Fatalf("%s: the tool port needs its policy: %v", name, err)
		}
		if len(p.Spec.Ingress) != 1 || len(p.Spec.Ingress[0].From) != 2 {
			t.Errorf("%s: only the tool port rule, narrowed to two peers: %+v", name, p.Spec.Ingress)
		}
	}
}

func TestAttachAPIServerWithoutEndpointsFails(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := discoveryv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	s := &NetPolicySync{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	w := netpol.Workload{Rules: &aiv1alpha1.EgressRules{APIServer: true}}
	cfg := netpol.Config{Enabled: true, Egress: netpol.EgressDeclared}
	if err := s.attachAPIServer(context.Background(), cfg, &w); err == nil {
		t.Error("an API server rule with no address to allow must be an error, not a silent gap")
	}
}

func TestAPIServerEndpointsReadAnIPv4MappedAddressAsIPv4(t *testing.T) {
	port := int32(443)
	slice := &discoveryv1.EndpointSlice{
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"::ffff:10.0.0.1"}}},
		Ports:     []discoveryv1.EndpointPort{{Port: &port}},
	}
	if got := apiServerEndpoints(slice)[0].CIDR; got != "10.0.0.1/32" {
		t.Errorf("an IPv4-mapped address is an IPv4 address: %s", got)
	}
}
