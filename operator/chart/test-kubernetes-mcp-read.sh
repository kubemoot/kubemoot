#!/usr/bin/env bash
# Render tests for the Kubernetes MCP server's access. The operator chart grants a crew's
# Kubernetes MCP servers no read access: the crew chart ships its own ServiceAccount, roles and
# bindings. So the operator chart renders no cluster-wide binding to the old creator role, no
# kubemoot-kubernetes-mcp-* read, Secret, write or namespaces role, no binding of them, and gives
# the operator no right to bind roles it does not hold. The internal server's creator role is
# bound in the release namespace only. Needs helm and yq.
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

  [ -z "$(yq -N 'select(.kind == "ClusterRoleBinding" and .metadata.name == "kubemoot-kubernetes-mcp-binding") | .metadata.name' <<<"$out")" ] \
    || fail "the cluster-wide kubemoot-kubernetes-mcp-binding is rendered"
  [ -z "$(yq -N 'select(.kind == "ClusterRoleBinding" and .roleRef.name == "kubemoot-mcpserver-creator") | .metadata.name' <<<"$out")" ] \
    || fail "a ClusterRoleBinding grants the creator role"

  # The roles the namespace read-consent controller bound are gone, and so is any binding of them.
  stale="$(yq -N 'select(.kind == "ClusterRole" or .kind == "ClusterRoleBinding" or .kind == "RoleBinding") | .metadata.name' <<<"$out" \
    | grep -E '^kubemoot-kubernetes-mcp-(read|secrets|secret-get|write|namespaces)$' || true)"
  [ -z "$stale" ] || fail "the chart still renders: $stale"

  # The operator holds no bind right on named roles.
  [ -z "$(role "$out" kubemoot-operator-manager '.rules[] | select(.verbs[] == "bind") | .resourceNames[]')" ] \
    || fail "the operator may still bind roles"
done

# The creator role is bound in the release namespace only and reads no core resources.
out="$(render --set internalAgents.enabled=true --set internalAgents.mcpServers.enabled=true)"
[ "$(yq -N 'select(.kind == "RoleBinding" and .metadata.name == "kubemoot-mcpserver-creator") | .metadata.namespace' <<<"$out")" = kubemoot ] \
  || fail "the creator RoleBinding is not in the release namespace"
[ -z "$(role "$out" kubemoot-mcpserver-creator '.rules[].resources[]' | grep -x -e pods -e namespaces -e secrets || true)" ] \
  || fail "the creator role reads core resources"

# The operator may list and delete RoleBindings, to remove the ones the old controller made.
for verb in list delete; do
  [ -n "$(role "$out" kubemoot-operator-manager '.rules[] | select(.resources[] == "rolebindings") | .verbs[]' | grep -x "$verb" || true)" ] \
    || fail "the operator cannot $verb rolebindings"
done

echo "PASS: Kubernetes MCP access render tests"
