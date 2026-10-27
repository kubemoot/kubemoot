/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package netpol

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// generatedLabels are the labels on every generated object.
func generatedLabels() map[string]string {
	return map[string]string{
		LabelGeneratedBy: GeneratedByValue,
		labelManagedBy:   managedByValue,
	}
}

func newPolicy(name, namespace string, selector metav1.LabelSelector, types ...networkingv1.PolicyType) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: generatedLabels()},
		Spec:       networkingv1.NetworkPolicySpec{PodSelector: selector, PolicyTypes: types},
	}
}

func tcpPort(port int32) networkingv1.NetworkPolicyPort {
	proto := corev1.ProtocolTCP
	p := intstr.FromInt32(port)
	return networkingv1.NetworkPolicyPort{Protocol: &proto, Port: &p}
}

func portsOf(protocol corev1.Protocol, ports ...int32) []networkingv1.NetworkPolicyPort {
	out := make([]networkingv1.NetworkPolicyPort, 0, len(ports))
	for _, port := range ports {
		proto := protocol
		p := intstr.FromInt32(port)
		out = append(out, networkingv1.NetworkPolicyPort{Protocol: &proto, Port: &p})
	}
	return out
}

func tcpPorts(ports ...int32) []networkingv1.NetworkPolicyPort {
	return portsOf(corev1.ProtocolTCP, ports...)
}

func podsWith(labels map[string]string) *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchLabels: labels}
}

func namespaceNamed(name string) *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchLabels: map[string]string{namespaceNameLabel: name}}
}

// operatorPeer selects the operator pod.
func (c Config) operatorPeer() networkingv1.NetworkPolicyPeer {
	return networkingv1.NetworkPolicyPeer{
		NamespaceSelector: namespaceNamed(c.OperatorNamespace),
		PodSelector:       podsWith(map[string]string{controlPlaneLabel: controllerManager}),
	}
}

// sameNamespacePeer selects every pod of the policy's own namespace.
func sameNamespacePeer() networkingv1.NetworkPolicyPeer {
	return networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}}
}

// operatorNamespacePeer selects every pod of the operator's namespace.
func (c Config) operatorNamespacePeer() networkingv1.NetworkPolicyPeer {
	return networkingv1.NetworkPolicyPeer{NamespaceSelector: namespaceNamed(c.OperatorNamespace)}
}

// metricsPeers is the metrics scraper, or nothing when none is configured.
func (c Config) metricsPeers() []networkingv1.NetworkPolicyPeer {
	if c.Metrics.Namespace == "" {
		return nil
	}
	peer := networkingv1.NetworkPolicyPeer{NamespaceSelector: namespaceNamed(c.Metrics.Namespace)}
	if len(c.Metrics.Labels) > 0 {
		peer.PodSelector = podsWith(c.Metrics.Labels)
	}
	return []networkingv1.NetworkPolicyPeer{peer}
}

// ToolPortPeers are the callers of a crew gateway's tool port: the crew's own namespace,
// the operator namespace, and the metrics scraper.
func ToolPortPeers(c Config) []networkingv1.NetworkPolicyPeer {
	metrics := c.metricsPeers()
	peers := make([]networkingv1.NetworkPolicyPeer, 0, 2+len(metrics))
	peers = append(peers, sameNamespacePeer(), c.operatorNamespacePeer())
	return append(peers, metrics...)
}

// Baseline is the default-deny ingress policy of a crew namespace plus the policy that lets
// the namespace's own pods, the operator, and the metrics scraper reach every pod that is
// neither a tool server nor a tool gateway (those have their own policies).
func Baseline(c Config, namespace string) []client.Object {
	if !c.IngressOn() {
		return nil
	}
	deny := newPolicy(NamePrefix+"default-deny", namespace, metav1.LabelSelector{}, networkingv1.PolicyTypeIngress)

	internal := newPolicy(NamePrefix+"crew-internal", namespace, metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key:      labelComponent,
			Operator: metav1.LabelSelectorOpNotIn,
			Values:   []string{ComponentMCPServer, ComponentMCPGateway},
		}},
	}, networkingv1.PolicyTypeIngress)
	internal.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{From: ToolPortPeers(c)}}
	return []client.Object{deny, internal}
}

// MCPServer describes a tool server pod for its ingress policy.
type MCPServer struct {
	// PodName is the bounded name that the pod labels carry.
	PodName   string
	Namespace string
	// Port is the port the server (or the bridge, for stdio) listens on.
	Port int32
	// CrewNamespaces are the namespaces that hold a Crew. Only a server in the operator
	// namespace uses them: it admits the gateway of each of these namespaces.
	CrewNamespaces []string
}

// MCPServerIngress admits the crew's gateway and the operator to a tool server on its
// port. A tool server in the operator namespace is shared: it admits the gateway of every
// namespace that holds a Crew, and of none when no namespace does.
func MCPServerIngress(c Config, s MCPServer) []client.Object {
	if !c.IngressOn() {
		return nil
	}
	policy := newPolicy(NamePrefix+"mcp-"+s.PodName+"-ingress", s.Namespace, metav1.LabelSelector{MatchLabels: map[string]string{
		labelName:      s.PodName,
		labelInstance:  s.PodName,
		labelComponent: ComponentMCPServer,
	}}, networkingv1.PolicyTypeIngress)

	gateway := podsWith(map[string]string{labelComponent: ComponentMCPGateway})
	gatewayPeers := []networkingv1.NetworkPolicyPeer{{PodSelector: gateway}}
	if s.Namespace == c.OperatorNamespace {
		gatewayPeers = crewGatewayPeers(s.CrewNamespaces, gateway)
	}
	policy.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{
		From:  append(gatewayPeers, c.operatorPeer()),
		Ports: tcpPorts(s.Port),
	}}
	return []client.Object{policy}
}

// crewGatewayPeers selects the gateway pods of the named namespaces. With no namespace
// it selects nothing: a selector that lists no value would be invalid.
func crewGatewayPeers(namespaces []string, gateway *metav1.LabelSelector) []networkingv1.NetworkPolicyPeer {
	if len(namespaces) == 0 {
		return nil
	}
	return []networkingv1.NetworkPolicyPeer{{
		NamespaceSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key: namespaceNameLabel, Operator: metav1.LabelSelectorOpIn, Values: slices.Sorted(slices.Values(namespaces)),
		}}},
		PodSelector: gateway,
	}}
}

// GatewayProxy describes a crew's discussion gateway for the policy that lets callers
// from outside the namespace reach it.
type GatewayProxy struct {
	// Crew is the crew that owns the gateway; its pods carry LabelCrew=<Crew>.
	Crew      string
	Namespace string
	// Allowed are the extra namespaces from Crew.spec.network.allowedIngress.
	Allowed []metav1.LabelSelector
}

// DiscussionGatewayIngress admits the callers that come from outside the crew namespace to
// the discussion gateway: the namespaces the crew lists, the Gateway API proxy, and the
// API server's service proxy. With Cilium the last two are entities; otherwise they come
// from the chart's trusted namespaces and control plane ranges.
func DiscussionGatewayIngress(c Config, g GatewayProxy) []client.Object {
	if !c.IngressOn() {
		return nil
	}
	selector := map[string]string{labelComponent: ComponentDiscussionGateway, LabelCrew: g.Crew}
	policy := newPolicy(NamePrefix+"crew-"+g.Crew+"-gateway-ingress", g.Namespace,
		metav1.LabelSelector{MatchLabels: selector}, networkingv1.PolicyTypeIngress)

	var peers []networkingv1.NetworkPolicyPeer
	for i := range g.Allowed {
		peers = append(peers, networkingv1.NetworkPolicyPeer{NamespaceSelector: g.Allowed[i].DeepCopy()})
	}
	for _, ns := range c.TrustedIngressNamespaces {
		peers = append(peers, networkingv1.NetworkPolicyPeer{NamespaceSelector: namespaceNamed(ns)})
	}
	for _, cidr := range c.ControlPlaneCIDRs {
		peers = append(peers, networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: cidr}})
	}
	out := []client.Object{policy}
	if len(peers) > 0 {
		policy.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{
			From: peers, Ports: tcpPorts(DiscussionGatewayPort),
		}}
	}
	if c.UseCilium {
		out = append(out, ciliumGatewayEntities(g))
	}
	return out
}

// ciliumGatewayEntities admits the Gateway API proxy (entity ingress), the API server and
// the node (the API server's service proxy arrives as remote-node), which a NetworkPolicy
// cannot name.
func ciliumGatewayEntities(g GatewayProxy) *unstructured.Unstructured {
	return ciliumPolicy(NamePrefix+"crew-"+g.Crew+"-gateway-entities", g.Namespace, map[string]any{
		"endpointSelector": selectorOf(map[string]any{labelComponent: ComponentDiscussionGateway, LabelCrew: g.Crew}),
		"ingress": []any{map[string]any{
			"fromEntities": []any{"ingress", "kube-apiserver", "remote-node"},
			keyToPorts:     toPorts("TCP", []int32{DiscussionGatewayPort}, nil),
		}},
	})
}

// ciliumPolicy wraps a spec as a generated CiliumNetworkPolicy.
func ciliumPolicy(name, namespace string, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	u.SetGroupVersionKind(CiliumPolicyGVK)
	u.SetName(name)
	u.SetNamespace(namespace)
	u.SetLabels(generatedLabels())
	return u
}

// GatewayToolPort is the ingress rule of a crew gateway's tool port: the crew's own
// namespace, the operator, and the metrics scraper. The caller adds it to the gateway's
// policy in place of the rule that admits every pod.
func GatewayToolPort(c Config, port int32) networkingv1.NetworkPolicyIngressRule {
	return networkingv1.NetworkPolicyIngressRule{From: ToolPortPeers(c), Ports: []networkingv1.NetworkPolicyPort{tcpPort(port)}}
}
