#!/usr/bin/env bash
# Render tests for the Kubernetes MCP server's read access: no cluster-wide binding to the
# read rules, a read ClusterRole with namespaced reads only, a namespaces ClusterRole with
# nothing else, and an operator that may bind exactly those two roles. Needs helm and yq.
# Run from anywhere: operator/chart/test-kubernetes-mcp-read.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
render() { helm template kubemoot-operator "$chart" --namespace kubemoot "$@"; }
role() { yq -N "select(.kind == \"ClusterRole\" and .metadata.name == \"$2\") | $3" <<<"$1"; }

for args in "" "--set internalAgents.enabled=true --set internalAgents.mcpServers.enabled=true"; do
  # shellcheck disable=SC2086
  out="$(render $args)"

  # The legacy cluster-wide binding is gone, and so is every binding to the creator role.
  [ -z "$(yq -N 'select(.kind == "ClusterRoleBinding" and .metadata.name == "kubemoot-kubernetes-mcp-binding") | .metadata.name' <<<"$out")" ] \
    || fail "the cluster-wide kubemoot-kubernetes-mcp-binding is still rendered"
  [ -z "$(yq -N 'select(.kind == "ClusterRoleBinding" and .roleRef.name == "kubemoot-mcpserver-creator") | .metadata.name' <<<"$out")" ] \
    || fail "a ClusterRoleBinding still grants the creator role"
  # Only the operator may make the per-namespace and namespaces bindings: the chart renders none.
  [ -z "$(yq -N 'select(.kind == "ClusterRoleBinding" and (.roleRef.name | test("^kubemoot-kubernetes-mcp-"))) | .metadata.name' <<<"$out")" ] \
    || fail "the chart renders a binding the operator should keep"

  # The read role: namespaced reads only, never Secrets, never a write verb.
  [ -n "$(role "$out" kubemoot-kubernetes-mcp-read .metadata.name)" ] || fail "no read ClusterRole"
  [ -z "$(role "$out" kubemoot-kubernetes-mcp-read '.rules[].resources[]' | grep -x -e secrets -e namespaces -e nodes -e 'pods/exec' || true)" ] \
    || fail "the read role names secrets, namespaces, nodes or pods/exec"
  verbs="$(role "$out" kubemoot-kubernetes-mcp-read '.rules[].verbs[]' | sort -u | paste -sd, -)"
  [ "$verbs" = "get,list,watch" ] || fail "the read role verbs are '$verbs', want get,list,watch"

  # The namespaces role: exactly get, list, watch on namespaces.
  [ "$(role "$out" kubemoot-kubernetes-mcp-namespaces '.rules | length')" = 1 ] || fail "the namespaces role has more than one rule"
  [ "$(role "$out" kubemoot-kubernetes-mcp-namespaces '.rules[0].resources[0]')" = namespaces ] || fail "the namespaces role names another kind"
  [ "$(role "$out" kubemoot-kubernetes-mcp-namespaces '.rules[0].verbs | join(",")')" = "get,list,watch" ] || fail "the namespaces role verbs changed"

  # The operator may bind those two roles and no other by name.
  bound="$(role "$out" kubemoot-operator-manager '.rules[] | select(.verbs[] == "bind") | .resourceNames[]' | sort | paste -sd, -)"
  [ "$bound" = "kubemoot-kubernetes-mcp-namespaces,kubemoot-kubernetes-mcp-read" ] || fail "the operator binds '$bound'"
done

# The creator role is bound in the release namespace only.
out="$(render --set internalAgents.enabled=true --set internalAgents.mcpServers.enabled=true)"
[ "$(yq -N 'select(.kind == "RoleBinding" and .metadata.name == "kubemoot-mcpserver-creator") | .metadata.namespace' <<<"$out")" = kubemoot ] \
  || fail "the creator RoleBinding is not in the release namespace"
[ -z "$(role "$out" kubemoot-mcpserver-creator '.rules[].resources[]' | grep -x -e pods -e namespaces -e secrets || true)" ] \
  || fail "the creator role still reads core resources"

echo "PASS: Kubernetes MCP read RBAC render tests"
