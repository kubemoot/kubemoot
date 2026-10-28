/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package netpol

import (
	"fmt"
	"net"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// Condition values for the EgressEnforced status condition.
const (
	// ConditionEgressEnforced is the condition type on an MCPServer or Agent.
	ConditionEgressEnforced = "EgressEnforced"
	// ReasonEnforced means every declared rule is enforced.
	ReasonEnforced = "Enforced"
	// ReasonServiceNotResolved means a declared Service could not be turned into a rule.
	ReasonServiceNotResolved = "ServiceNotResolved"
	// ReasonFQDNNotSupported means the cluster's CNI cannot enforce host names, so the
	// declared hosts are allowed as the internet outside the cluster.
	ReasonFQDNNotSupported = "FQDNNotSupported"
)

// APIServerEndpoint is one address of the Kubernetes API server.
type APIServerEndpoint struct {
	CIDR string
	Port int32
}

// Workload is a pod set whose outbound traffic is restricted.
type Workload struct {
	// Name is the name the policy takes: km-<Name>-egress.
	Name      string
	Namespace string
	// Selector selects the workload's pods.
	Selector map[string]string
	// Rules is the declared egress; nil means the workload is not restricted.
	Rules *kubemootv1alpha1.EgressRules
	// Defaults are the destinations always allowed, beyond DNS.
	Defaults []Dest
	// APIServer is where the API server listens, for a cluster without Cilium.
	APIServer []APIServerEndpoint
	// Services are the resolved destinations of spec.egress.allowedServices.
	Services []Dest
	// Unresolved describes each allowed Service that could not be resolved; the status
	// reports it and the workload gets no rule for that Service.
	Unresolved []string
}

// EgressStatus is what the egress policy enforces, for the EgressEnforced condition.
type EgressStatus struct {
	// Applicable is false when the workload is not restricted.
	Applicable bool
	Enforced   bool
	Reason     string
	Message    string
}

// EgressResult is the policy objects and their status.
type EgressResult struct {
	Objects []client.Object
	Status  EgressStatus
}

// Egress builds the egress policy of a workload that declares egress. It returns nothing
// when egress policies are off or the workload declares none. The workload may reach DNS,
// its defaults, and what it declares. Declared hosts are enforced through a
// CiliumNetworkPolicy when the cluster runs Cilium; otherwise the policy lets the workload
// reach the internet outside the private and cluster ranges, and the status says so.
func Egress(c Config, w Workload) EgressResult {
	if !c.EgressOn() || w.Rules == nil {
		return EgressResult{}
	}
	plan := newEgressPlan(w)
	policy := newPolicy(NamePrefix+w.Name+"-egress", w.Namespace,
		metav1.LabelSelector{MatchLabels: w.Selector}, networkingv1.PolicyTypeEgress)
	policy.Spec.Egress = append([]networkingv1.NetworkPolicyEgressRule{c.dnsRule()}, plan.clusterRules()...)
	policy.Spec.Egress = append(policy.Spec.Egress, plan.cidrRules(w.Rules)...)

	out := []client.Object{policy}
	if w.Rules.APIServer && !c.UseCilium {
		policy.Spec.Egress = append(policy.Spec.Egress, apiServerRules(w.APIServer)...)
	}
	status := EgressStatus{Applicable: true, Enforced: true, Reason: ReasonEnforced,
		Message: "every declared egress rule is enforced"}
	hosts := plan.hostPorts()
	switch {
	case len(hosts) > 0 && c.UseCilium:
		out = append(out, ciliumEgress(c, w, hosts))
	case len(hosts) > 0:
		policy.Spec.Egress = append(policy.Spec.Egress, c.internetRules(hosts)...)
		status = EgressStatus{Applicable: true, Reason: ReasonFQDNNotSupported,
			Message: "the cluster's network plugin cannot enforce host names; the declared hosts are " +
				"allowed as the internet outside the cluster's private ranges"}
	case w.Rules.APIServer && c.UseCilium:
		out = append(out, ciliumEgress(c, w, nil))
	}
	return EgressResult{Objects: out, Status: withUnresolved(status, w.Unresolved)}
}

// withUnresolved folds the Services that could not be resolved into the status.
func withUnresolved(st EgressStatus, unresolved []string) EgressStatus {
	if len(unresolved) == 0 {
		return st
	}
	msg := "not enforced: " + strings.Join(unresolved, "; ")
	if !st.Enforced {
		msg += "; " + st.Message
	}
	return EgressStatus{Applicable: true, Reason: ReasonServiceNotResolved, Message: msg}
}

// egressPlan collects the destinations of one workload before they become rules.
type egressPlan struct {
	cluster []*clusterDest
	index   map[string]*clusterDest
	hosts   map[string]map[int32]bool
	cidrs   []Dest
	ports   []int32
}

type clusterDest struct {
	peer  networkingv1.NetworkPolicyPeer
	ports map[int32]bool
}

func newEgressPlan(w Workload) *egressPlan {
	p := &egressPlan{index: map[string]*clusterDest{}, hosts: map[string]map[int32]bool{}}
	for _, d := range append(append([]Dest(nil), w.Defaults...), w.Services...) {
		p.add(d)
	}
	p.ports = declaredPorts(w.Rules)
	for _, h := range w.Rules.AllowedHosts {
		for _, port := range p.ports {
			p.addHost(h, port)
		}
	}
	return p
}

func declaredPorts(r *kubemootv1alpha1.EgressRules) []int32 {
	if len(r.Ports) == 0 {
		return []int32{DefaultEgressPort}
	}
	return sortedUnique(r.Ports)
}

func sortedUnique(in []int32) []int32 {
	seen := map[int32]bool{}
	var out []int32
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (p *egressPlan) addHost(host string, port int32) {
	if p.hosts[host] == nil {
		p.hosts[host] = map[int32]bool{}
	}
	p.hosts[host][port] = true
}

func (p *egressPlan) add(d Dest) {
	switch {
	case d.Host != "":
		p.addHost(d.Host, d.Port)
	case d.CIDR != "":
		p.cidrs = append(p.cidrs, d)
	default:
		p.addCluster(d)
	}
}

func (p *egressPlan) addCluster(d Dest) {
	peer := networkingv1.NetworkPolicyPeer{}
	if d.Namespace != "" {
		peer.NamespaceSelector = namespaceNamed(d.Namespace)
	}
	if len(d.PodLabels) > 0 {
		peer.PodSelector = podsWith(d.PodLabels)
	} else if d.SameNamespace {
		// A peer needs a selector; an empty pod selector is every pod of the policy's namespace.
		peer.PodSelector = &metav1.LabelSelector{}
	}
	key := fmt.Sprintf("%s|%v|%v", d.Namespace, d.SameNamespace, d.PodLabels)
	cd := p.index[key]
	if cd == nil {
		cd = &clusterDest{peer: peer, ports: map[int32]bool{}}
		p.index[key] = cd
		p.cluster = append(p.cluster, cd)
	}
	cd.ports[d.Port] = true
}

func (p *egressPlan) clusterRules() []networkingv1.NetworkPolicyEgressRule {
	out := make([]networkingv1.NetworkPolicyEgressRule, 0, len(p.cluster))
	for _, cd := range p.cluster {
		out = append(out, networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{cd.peer}, Ports: tcpPorts(portList(cd.ports)...),
		})
	}
	return out
}

// cidrRules are the declared ranges and the derived address destinations.
func (p *egressPlan) cidrRules(r *kubemootv1alpha1.EgressRules) []networkingv1.NetworkPolicyEgressRule {
	out := make([]networkingv1.NetworkPolicyEgressRule, 0, len(p.cidrs)+len(r.AllowedCIDRs))
	for _, d := range p.cidrs {
		out = append(out, networkingv1.NetworkPolicyEgressRule{
			To:    []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: d.CIDR}}},
			Ports: tcpPorts(d.Port),
		})
	}
	for _, c := range r.AllowedCIDRs {
		out = append(out, networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{
				CIDR: c.CIDR, Except: append([]string(nil), c.Except...),
			}}},
			Ports: tcpPorts(p.ports...),
		})
	}
	return out
}

// hostPorts maps each host to its sorted ports.
func (p *egressPlan) hostPorts() map[string][]int32 {
	out := make(map[string][]int32, len(p.hosts))
	for h, ports := range p.hosts {
		out[h] = portList(ports)
	}
	return out
}

func portList(set map[int32]bool) []int32 {
	out := make([]int32, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (c Config) dnsRule() networkingv1.NetworkPolicyEgressRule {
	peer := networkingv1.NetworkPolicyPeer{NamespaceSelector: namespaceNamed(c.DNS.Namespace)}
	if len(c.DNS.Labels) > 0 {
		peer.PodSelector = podsWith(c.DNS.Labels)
	}
	ports := append(portsOf(corev1.ProtocolUDP, dnsPort), portsOf(corev1.ProtocolTCP, dnsPort)...)
	return networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{peer}, Ports: ports}
}

func apiServerRules(eps []APIServerEndpoint) []networkingv1.NetworkPolicyEgressRule {
	out := make([]networkingv1.NetworkPolicyEgressRule, 0, len(eps))
	for _, ep := range eps {
		out = append(out, networkingv1.NetworkPolicyEgressRule{
			To:    []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: ep.CIDR}}},
			Ports: tcpPorts(ep.Port),
		})
	}
	return out
}

// Ranges the internet fallback keeps blocked.
var (
	privateV4 = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16", "127.0.0.0/8"}
	privateV6 = []string{"fc00::/7", "fe80::/10", "::1/128"}
)

// internetRules allow every address outside the private and cluster ranges on the ports of
// the declared hosts, for a cluster whose network plugin cannot name hosts.
func (c Config) internetRules(hosts map[string][]int32) []networkingv1.NetworkPolicyEgressRule {
	set := map[int32]bool{}
	for _, ports := range hosts {
		for _, p := range ports {
			set[p] = true
		}
	}
	exceptV4, exceptV6 := append([]string(nil), privateV4...), append([]string(nil), privateV6...)
	for _, cidr := range c.ClusterCIDRs {
		if ip, _, err := net.ParseCIDR(cidr); err == nil && ip.To4() != nil {
			exceptV4 = append(exceptV4, cidr)
		} else if err == nil {
			exceptV6 = append(exceptV6, cidr)
		}
	}
	ports := tcpPorts(portList(set)...)
	return []networkingv1.NetworkPolicyEgressRule{
		{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0", Except: exceptV4}}}, Ports: ports},
		{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "::/0", Except: exceptV6}}}, Ports: ports},
	}
}

// ciliumEgress is the Cilium half of a workload's egress: DNS with the proxy that makes
// host rules work, the declared hosts, and the API server entity.
func ciliumEgress(c Config, w Workload, hosts map[string][]int32) *unstructured.Unstructured {
	dns := map[string]any{
		"toEndpoints": []any{selectorOf(ciliumDNSLabels(c))},
		keyToPorts: toPorts("ANY", []int32{dnsPort},
			map[string]any{"dns": []any{map[string]any{"matchPattern": "*"}}}),
	}
	egress := []any{dns}
	egress = append(egress, ciliumFQDNRules(hosts)...)
	if w.Rules.APIServer {
		egress = append(egress, map[string]any{"toEntities": []any{"kube-apiserver"}})
	}
	selector := map[string]any{}
	for k, v := range w.Selector {
		selector[k] = v
	}
	return ciliumPolicy(NamePrefix+w.Name+"-egress-fqdn", w.Namespace, map[string]any{
		"endpointSelector": selectorOf(selector),
		"egress":           egress,
	})
}

func ciliumDNSLabels(c Config) map[string]any {
	out := map[string]any{"io.kubernetes.pod.namespace": c.DNS.Namespace}
	for k, v := range c.DNS.Labels {
		out[k] = v
	}
	return out
}

// ciliumFQDNRules groups hosts that share a port set into one rule each.
func ciliumFQDNRules(hosts map[string][]int32) []any {
	groups := map[string][]string{}
	portsOfKey := map[string][]int32{}
	for h, ports := range hosts {
		key := fmt.Sprint(ports)
		groups[key] = append(groups[key], h)
		portsOfKey[key] = ports
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]any, 0, len(keys))
	for _, k := range keys {
		names := groups[k]
		sort.Strings(names)
		fqdns := make([]any, 0, len(names))
		for _, n := range names {
			if strings.Contains(n, "*") {
				fqdns = append(fqdns, map[string]any{"matchPattern": n})
			} else {
				fqdns = append(fqdns, map[string]any{"matchName": n})
			}
		}
		out = append(out, map[string]any{
			"toFQDNs":  fqdns,
			keyToPorts: toPorts("TCP", portsOfKey[k], nil),
		})
	}
	return out
}
