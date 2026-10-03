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

# Workshop instance: off by default, and the default render has none of its objects.
out="$(render kubemoot-operator --set dashboard.enabled=true)"
grep -q 'dashboard-workshop' <<<"$out" && fail "workshop objects rendered without dashboard.workshop.enabled"

# Workshop on: its own Deployment, Service, ServiceAccount, ClusterRole and binding, the
# same image as the main dashboard, read-only and scoped by environment, read verbs only.
out="$(render kubemoot-operator --set dashboard.enabled=true --set dashboard.workshop.enabled=true \
  --set dashboard.workshop.namespaceSelector=team=on --set dashboard.workshop.infraNamespace=moot)"
for kind in Deployment Service ServiceAccount ClusterRole ClusterRoleBinding; do
  n="$(yq -N "select(.kind == \"$kind\" and .metadata.name == \"kubemoot-operator-dashboard-workshop*\")" <<<"$out" | grep -c '^kind:' || true)"
  [ "$n" -ge 1 ] || fail "workshop render has no $kind"
done
dups="$(yq -N '.kind + "/" + .metadata.name' <<<"$out" | sort | uniq -d)"
[ -z "$dups" ] || fail "workshop render has duplicate objects: $dups"
main_image="$(yq -N 'select(.kind == "Deployment" and .metadata.name == "kubemoot-operator-dashboard") | .spec.template.spec.containers[0].image' <<<"$out")"
ws_image="$(yq -N 'select(.kind == "Deployment" and .metadata.name == "kubemoot-operator-dashboard-workshop") | .spec.template.spec.containers[0].image' <<<"$out")"
[ -n "$main_image" ] && [ "$main_image" = "$ws_image" ] || fail "workshop image '$ws_image' differs from the dashboard image '$main_image'"
ws_env() { yq -N "select(.kind == \"Deployment\" and .metadata.name == \"kubemoot-operator-dashboard-workshop\") | .spec.template.spec.containers[0].env[] | select(.name == \"$1\") | .value" <<<"$out"; }
[ "$(ws_env KUBEMOOT_DASHBOARD_READ_ONLY)" = "true" ] || fail "workshop is not read-only"
[ "$(ws_env KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR)" = "team=on" ] || fail "workshop selector not set"
[ "$(ws_env KUBEMOOT_DASHBOARD_INFRA_NAMESPACE)" = "moot" ] || fail "workshop infra namespace not set"
main_sel="$(yq -N 'select(.kind == "Service" and .metadata.name == "kubemoot-operator-dashboard") | .spec.selector["app.kubernetes.io/name"]' <<<"$out")"
ws_sel="$(yq -N 'select(.kind == "Service" and .metadata.name == "kubemoot-operator-dashboard-workshop") | .spec.selector["app.kubernetes.io/name"]' <<<"$out")"
[ "$main_sel" != "$ws_sel" ] || fail "workshop and dashboard Services select the same pods"
verbs="$(yq -N 'select(.kind == "ClusterRole" and .metadata.name == "kubemoot-operator-dashboard-workshop-reader") | .rules[].verbs[]' <<<"$out" | sort -u | tr '\n' ' ')"
case "$verbs" in "get list watch ") ;; *) fail "workshop ClusterRole verbs are '$verbs', want only get list watch" ;; esac
yq -N 'select(.kind == "ClusterRole" and .metadata.name == "kubemoot-operator-dashboard-workshop-reader") | .rules[].resources[]' <<<"$out" \
  | grep -qx 'secrets' && fail "workshop ClusterRole can read Secrets"
yq -N 'select(.kind == "ClusterRole" and .metadata.name == "kubemoot-operator-dashboard-workshop-reader") | .rules[].resources[]' <<<"$out" \
  | grep -qx 'pods' && fail "workshop ClusterRole reads pods cluster-wide"
[ "$(yq -N 'select(.kind == "Role" and .metadata.name == "kubemoot-operator-dashboard-workshop-pods") | .metadata.namespace' <<<"$out")" = "moot" ] \
  || fail "workshop pod Role is not in the infra namespace"
# The main dashboard is untouched by the option.
a="$(render kubemoot-operator --set dashboard.enabled=true | yq -N 'select(.metadata.name == "kubemoot-operator-dashboard*" and .metadata.name != "*-workshop*")')"
b="$(yq -N 'select(.metadata.name == "kubemoot-operator-dashboard*" and .metadata.name != "*-workshop*")' <<<"$out")"
[ "$a" = "$b" ] || fail "enabling the workshop changed the main dashboard objects"
# An empty selector would show every namespace: the chart refuses it.
render kubemoot-operator --set dashboard.enabled=true --set dashboard.workshop.enabled=true \
  --set dashboard.workshop.namespaceSelector= >/dev/null 2>&1 && fail "empty workshop selector was accepted"

# Standalone chart keeps its own defaults.
helm template x "$here/../../dashboard/charts/kubemoot-dashboard" | grep -q 'image: ghcr.io/kubemoot/dashboard:' \
  || fail "standalone dashboard image changed"
echo "dashboard subchart tests: PASS"
