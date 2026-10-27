/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package netpol builds the NetworkPolicy and CiliumNetworkPolicy objects the operator
// generates for crews, tool servers, and agents. The builders are pure functions from a
// spec to objects; the controllers apply them.
package netpol

import (
	"strings"
)

// Labels and names shared by every generated object.
const (
	// LabelGeneratedBy marks every object the operator generates, so turning the flag off
	// deletes exactly those objects and nothing hand-written.
	LabelGeneratedBy = "kubemoot.ai/generated-by"
	// GeneratedByValue is the value of LabelGeneratedBy.
	GeneratedByValue = "network-policy"
	// NamePrefix starts the name of every generated object.
	NamePrefix = "km-"

	labelManagedBy = "app.kubernetes.io/managed-by"
	managedByValue = "kubemoot-operator"
	labelComponent = "app.kubernetes.io/component"
	labelName      = "app.kubernetes.io/name"
	labelInstance  = "app.kubernetes.io/instance"

	// LabelCrew is on every namespace that holds a crew.
	LabelCrew = "kubemoot.ai/crew"
	// LabelAgent is on every agent pod.
	LabelAgent = "kubemoot.ai/agent"

	namespaceNameLabel = "kubernetes.io/metadata.name"
	controlPlaneLabel  = "control-plane"
	controllerManager  = "controller-manager"
)

// Component label values of the pods the policies name.
const (
	ComponentMCPServer         = "mcp-server"
	ComponentMCPGateway        = "mcp-gateway"
	ComponentDiscussionGateway = "discussion-gateway"
	ComponentQueryService      = "query-service"
)

// Ports of the discussion gateway and the defaults of the egress rules.
const (
	// DiscussionGatewayPort is the port the discussion gateway serves on.
	DiscussionGatewayPort = 8080
	// DefaultEgressPort is the port allowed to declared hosts and ranges when none is listed.
	DefaultEgressPort = 443
	dnsPort           = 53
)

// EgressMode is the chart's egress setting.
type EgressMode string

const (
	// EgressOff generates no egress policy.
	EgressOff EgressMode = "Off"
	// EgressDeclared restricts only the workloads that set spec.egress.
	EgressDeclared EgressMode = "Declared"
)

// CiliumMode says whether the operator emits CiliumNetworkPolicy objects.
type CiliumMode string

const (
	// CiliumAuto emits them when the CiliumNetworkPolicy CRD is installed.
	CiliumAuto CiliumMode = "auto"
	// CiliumOn always emits them.
	CiliumOn CiliumMode = "on"
	// CiliumOff never emits them.
	CiliumOff CiliumMode = "off"
)

// PodRef names pods in a namespace by label.
type PodRef struct {
	Namespace string
	Labels    map[string]string
}

// Config is the cluster-wide network policy setting, read from the operator's environment.
type Config struct {
	// Enabled is the master switch. When false the operator creates nothing and deletes
	// only the objects it generated.
	Enabled bool
	// Ingress generates the ingress set.
	Ingress bool
	// Egress is the egress mode.
	Egress EgressMode
	// Cilium is the Cilium setting as configured.
	Cilium CiliumMode
	// UseCilium is the resolved decision: emit Cilium objects.
	UseCilium bool
	// OperatorNamespace is where the operator runs.
	OperatorNamespace string
	// DNS is where name resolution runs.
	DNS PodRef
	// Metrics is the metrics scraper; an empty namespace means none.
	Metrics PodRef
	// TrustedIngressNamespaces are the namespaces of the Gateway API proxy pods, for a
	// cluster without Cilium.
	TrustedIngressNamespaces []string
	// ControlPlaneCIDRs are the source ranges of the API server's service proxy, for a
	// cluster without Cilium.
	ControlPlaneCIDRs []string
	// ClusterCIDRs are pod and service ranges outside the private ranges; the egress
	// fallback keeps them blocked.
	ClusterCIDRs []string
}

// IngressOn reports whether the ingress set is generated.
func (c Config) IngressOn() bool { return c.Enabled && c.Ingress }

// EgressOn reports whether declared egress rules are enforced.
func (c Config) EgressOn() bool { return c.Enabled && c.Egress == EgressDeclared }

// WithCilium returns the config with the Cilium decision resolved.
func (c Config) WithCilium(use bool) Config {
	c.UseCilium = use
	return c
}

// Default values of the chart.
const (
	defaultDNSNamespace = "kube-system"
	defaultDNSLabel     = "k8s-app=kube-dns"
	defaultOperatorNS   = "kubemoot"
)

// Environment variables the chart sets on the operator.
const (
	EnvEnabled        = "KUBEMOOT_NETPOL_ENABLED"
	EnvIngress        = "KUBEMOOT_NETPOL_INGRESS"
	EnvEgressMode     = "KUBEMOOT_NETPOL_EGRESS_MODE"
	EnvCilium         = "KUBEMOOT_NETPOL_CILIUM"
	EnvDNSNamespace   = "KUBEMOOT_NETPOL_DNS_NAMESPACE"
	EnvDNSLabels      = "KUBEMOOT_NETPOL_DNS_LABELS"
	EnvMetricsNS      = "KUBEMOOT_NETPOL_METRICS_NAMESPACE"
	EnvMetricsLabels  = "KUBEMOOT_NETPOL_METRICS_LABELS"
	EnvTrustedIngress = "KUBEMOOT_NETPOL_TRUSTED_INGRESS_NAMESPACES"
	EnvControlPlane   = "KUBEMOOT_NETPOL_CONTROL_PLANE_CIDRS"
	EnvClusterCIDRs   = "KUBEMOOT_NETPOL_CLUSTER_CIDRS"
	EnvOperatorNS     = "OPERATOR_NAMESPACE"
)

// ConfigFromEnv reads the setting from the environment through getenv. An unknown egress
// mode (including Deny, which is not implemented) turns egress off, and an unknown Cilium
// value means auto; Warnings lists what was ignored.
func ConfigFromEnv(getenv func(string) string) (Config, []string) {
	var warnings []string
	cfg := Config{
		Enabled:           isTrue(getenv(EnvEnabled)),
		Ingress:           getenv(EnvIngress) == "" || isTrue(getenv(EnvIngress)),
		Egress:            EgressOff,
		Cilium:            CiliumAuto,
		OperatorNamespace: valueOr(getenv(EnvOperatorNS), defaultOperatorNS),
		DNS: PodRef{
			Namespace: valueOr(getenv(EnvDNSNamespace), defaultDNSNamespace),
			Labels:    parseLabels(valueOr(getenv(EnvDNSLabels), defaultDNSLabel)),
		},
		TrustedIngressNamespaces: splitList(getenv(EnvTrustedIngress)),
		ControlPlaneCIDRs:        splitList(getenv(EnvControlPlane)),
		ClusterCIDRs:             splitList(getenv(EnvClusterCIDRs)),
	}
	if ns := getenv(EnvMetricsNS); ns != "" {
		cfg.Metrics = PodRef{Namespace: ns, Labels: parseLabels(getenv(EnvMetricsLabels))}
	}
	switch m := EgressMode(getenv(EnvEgressMode)); m {
	case "", EgressOff:
	case EgressDeclared:
		cfg.Egress = m
	default:
		warnings = append(warnings, "egress mode "+string(m)+" is not supported; egress policies are off")
	}
	switch m := CiliumMode(getenv(EnvCilium)); m {
	case "", CiliumAuto:
	case CiliumOn, CiliumOff:
		cfg.Cilium = m
	default:
		warnings = append(warnings, "cilium setting "+string(m)+" is not supported; using auto")
	}
	return cfg, warnings
}

func isTrue(v string) bool { return strings.EqualFold(strings.TrimSpace(v), "true") }

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}

// splitList splits a comma separated list, dropping blanks.
func splitList(v string) []string {
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// parseLabels parses "a=b,c=d"; an entry without "=" is dropped.
func parseLabels(v string) map[string]string {
	out := map[string]string{}
	for _, item := range splitList(v) {
		if k, val, ok := strings.Cut(item, "="); ok && k != "" {
			out[strings.TrimSpace(k)] = strings.TrimSpace(val)
		}
	}
	return out
}
