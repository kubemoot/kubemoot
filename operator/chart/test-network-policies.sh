#!/usr/bin/env bash
# Render tests for the network policy flag (security.networkPolicies in values.yaml, read by
# templates/deployment.yaml). Needs helm and yq. Run from anywhere:
# operator/chart/test-network-policies.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
render() { helm template "$@" "$chart"; }
# env <rendered> <name>: the value of one operator environment variable ("<unset>" when absent).
env_of() {
  yq -N "select(.kind == \"Deployment\" and .metadata.labels[\"control-plane\"] == \"controller-manager\")
    | .spec.template.spec.containers[0].env[] | select(.name == \"$2\") | .value" <<<"$1" | head -n1
}
want() { # want <rendered> <name> <value>
  local got
  got="$(env_of "$1" "$2")"
  [ "$got" = "$3" ] || fail "$2 is '$got', want '$3'"
}

# Flag off (the default): the operator is told nothing, so it creates nothing.
out="$(render kubemoot-operator)"
grep -q 'KUBEMOOT_NETPOL' <<<"$out" && fail "default render sets network policy variables"

# Flag on, defaults: ingress on, egress off, Cilium auto, kube-dns.
out="$(render kubemoot-operator --set security.networkPolicies.enabled=true)"
want "$out" KUBEMOOT_NETPOL_ENABLED true
want "$out" KUBEMOOT_NETPOL_INGRESS true
want "$out" KUBEMOOT_NETPOL_EGRESS_MODE Off
want "$out" KUBEMOOT_NETPOL_CILIUM auto
want "$out" KUBEMOOT_NETPOL_DNS_NAMESPACE kube-system
want "$out" KUBEMOOT_NETPOL_DNS_LABELS k8s-app=kube-dns
want "$out" KUBEMOOT_NETPOL_METRICS_NAMESPACE ""

# Every setting.
out="$(render kubemoot-operator \
  --set security.networkPolicies.enabled=true \
  --set security.networkPolicies.ingress=false \
  --set security.networkPolicies.egress.mode=Declared \
  --set security.networkPolicies.cilium=off \
  --set security.networkPolicies.dns.namespace=dns \
  --set security.networkPolicies.dns.podLabels.app=coredns \
  --set security.networkPolicies.metricsScraper.namespace=obs \
  --set security.networkPolicies.metricsScraper.podLabels.b=2 \
  --set security.networkPolicies.metricsScraper.podLabels.a=1 \
  --set 'security.networkPolicies.trustedIngressNamespaces={envoy,istio}' \
  --set 'security.networkPolicies.controlPlaneCIDRs={10.1.0.0/24}' \
  --set 'security.networkPolicies.clusterCIDRs={203.0.113.0/24,2001:db8::/32}')"
want "$out" KUBEMOOT_NETPOL_INGRESS false
want "$out" KUBEMOOT_NETPOL_EGRESS_MODE Declared
want "$out" KUBEMOOT_NETPOL_CILIUM off
want "$out" KUBEMOOT_NETPOL_DNS_NAMESPACE dns
want "$out" KUBEMOOT_NETPOL_METRICS_NAMESPACE obs
want "$out" KUBEMOOT_NETPOL_METRICS_LABELS a=1,b=2
want "$out" KUBEMOOT_NETPOL_TRUSTED_INGRESS_NAMESPACES envoy,istio
want "$out" KUBEMOOT_NETPOL_CONTROL_PLANE_CIDRS 10.1.0.0/24
want "$out" KUBEMOOT_NETPOL_CLUSTER_CIDRS 203.0.113.0/24,2001:db8::/32

# The operator ClusterRole can manage network policies and Cilium policies.
rbac="$(yq -N 'select(.kind == "ClusterRole") | .rules[] | select(.resources[] == "networkpolicies" or .resources[] == "ciliumnetworkpolicies") | .resources[0]' <<<"$out")"
grep -q networkpolicies <<<"$rbac" || fail "the operator role has no networkpolicies rule"
grep -q ciliumnetworkpolicies <<<"$rbac" || fail "the operator role has no ciliumnetworkpolicies rule"

# The chart renders no NetworkPolicy of its own, under either setting.
grep -q 'kind: NetworkPolicy' <<<"$out" && fail "the chart renders a NetworkPolicy"

echo "network policy render tests passed"
