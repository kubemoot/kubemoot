#!/usr/bin/env bash
# Render tests for the dashboard subchart inside the operator chart. Needs helm and yq.
# Run from anywhere: operator/chart/test-dashboard-subchart.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
render() { helm template "$@" "$chart"; }

# Default: no dashboard objects.
out="$(render kubemoot-operator)"
grep -q 'component: dashboard' <<<"$out" && fail "default render contains dashboard objects"
grep -q 'kubemoot-dashboard-reader' <<<"$out" && fail "default render contains the dashboard ClusterRole"

# Enabled: dashboard Deployment, Service, ServiceAccount, ClusterRole, no route.
out="$(render kubemoot-operator --set dashboard.enabled=true)"
for kind in Deployment Service ServiceAccount ClusterRole ClusterRoleBinding; do
  n="$(yq -N "select(.kind == \"$kind\" and .metadata.name == \"kubemoot-operator-dashboard*\")" <<<"$out" | grep -c '^kind:' || true)"
  [ "$n" -ge 1 ] || fail "enabled render has no dashboard $kind"
done
grep -q 'kind: HTTPRoute' <<<"$out" && fail "route must stay off by default"

# Names are unique across kinds and unique between two releases (cluster-scoped names).
dups="$(yq -N '.kind + "/" + .metadata.name' <<<"$out" | sort | uniq -d)"
[ -z "$dups" ] || fail "duplicate objects: $dups"
a="$(render rel-a --set dashboard.enabled=true | yq -N 'select(.kind == "ClusterRole") | .metadata.name' | grep dashboard)"
b="$(render rel-b --set dashboard.enabled=true | yq -N 'select(.kind == "ClusterRole") | .metadata.name' | grep dashboard)"
[ "$a" != "$b" ] || fail "two releases share the dashboard ClusterRole name $a"

# global.* flows to the subchart.
out="$(render kubemoot-operator --set dashboard.enabled=true --set global.imageRegistry=mirror.example/km \
  --set 'global.imagePullSecrets[0].name=pull')"
grep -q 'image: mirror.example/km/dashboard:' <<<"$out" || fail "global.imageRegistry did not reach the dashboard image"
n="$(yq -N 'select(.kind == "Deployment") | .spec.template.spec.imagePullSecrets[0].name' <<<"$out" | grep -c '^pull$')"
[ "$n" -ge 2 ] || fail "global.imagePullSecrets did not reach the operator and dashboard Deployments"

# Standalone chart keeps its own defaults.
helm template x "$here/../../dashboard/charts/kubemoot-dashboard" | grep -q 'image: ghcr.io/kubemoot/dashboard:' \
  || fail "standalone dashboard image changed"
echo "dashboard subchart tests: PASS"
