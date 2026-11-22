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

# The dashboard never writes to Kubernetes: every ClusterRole and Role it renders holds get, list
# and watch and nothing else, none grants a wildcard verb, and the one exception is the right to
# create TokenReviews (checking a pasted bearer token at sign-in). A create, update, patch,
# delete or "*" anywhere else fails here.
TOKEN_REVIEW_RULE='authentication.k8s.io/tokenreviews:create'
only_read_verbs() {
  local label="$1" manifests="$2" rules line verbs verb n=0
  rules="$(yq -N 'select(.kind == "ClusterRole" or .kind == "Role") | .rules[] | (.apiGroups | join(",")) + "/" + (.resources | join(",")) + ":" + (.verbs | join(","))' <<<"$manifests")"
  [ -n "$rules" ] || fail "$label renders no RBAC rules to check"
  while IFS= read -r line; do
    n=$((n+1))
    [ "$line" = "$TOKEN_REVIEW_RULE" ] && continue
    verbs="${line##*:}"
    for verb in ${verbs//,/ }; do
      case "$verb" in get|list|watch) ;; *) fail "$label RBAC rule '$line' grants '$verb'; the dashboard only reads (get, list, watch) and creates TokenReviews" ;; esac
    done
  done <<<"$rules"
}
dash_only() { yq -N 'select((.kind == "ClusterRole" or .kind == "Role") and .metadata.name == "*dashboard*")' <<<"$1"; }
only_read_verbs "operator chart dashboard" "$(dash_only "$out")"
only_read_verbs "standalone dashboard chart" "$(helm template x "$here/../../dashboard/charts/kubemoot-dashboard")"
only_read_verbs "standalone dashboard chart, anonymous read-only" "$(helm template x "$here/../../dashboard/charts/kubemoot-dashboard" --set auth.anonymousReadOnly=true)"
only_read_verbs "workshop dashboard" "$(dash_only "$(render kubemoot-operator --set dashboard.enabled=true --set dashboard.workshop.enabled=true)")"
# By default the dashboard's own ServiceAccount can only create TokenReviews: people read with their own token.
default_rules="$(helm template x "$here/../../dashboard/charts/kubemoot-dashboard" | yq -N 'select(.kind == "ClusterRole") | .rules[] | (.apiGroups | join(",")) + "/" + (.resources | join(",")) + ":" + (.verbs | join(","))')"
[ "$default_rules" = "$TOKEN_REVIEW_RULE" ] || fail "the default dashboard ServiceAccount holds more than TokenReview create: $default_rules"
anon_rules="$(helm template x "$here/../../dashboard/charts/kubemoot-dashboard" --set auth.anonymousReadOnly=true | yq -N 'select(.kind == "ClusterRole") | .rules[] | (.resources | join(","))')"
grep -q 'kubemoot.ai' <<<"$(helm template x "$here/../../dashboard/charts/kubemoot-dashboard" --set auth.anonymousReadOnly=true | yq -N 'select(.kind == "ClusterRole") | .rules[].apiGroups[]')" \
  || fail "anonymous read-only mode renders no read rule for Kubemoot kinds ($anon_rules)"
# The check itself must be able to fail: a write rule added through values is caught.
if (only_read_verbs "injected" "$(helm template x "$here/../../dashboard/charts/kubemoot-dashboard" --set auth.anonymousReadOnly=true \
  --set 'rbac.clusterRole.rules[0].apiGroups[0]=kubemoot.ai' --set 'rbac.clusterRole.rules[0].resources[0]=crews' \
  --set 'rbac.clusterRole.rules[0].verbs[0]=delete')") >/dev/null 2>&1; then
  fail "the read-only RBAC check accepted a delete verb"
fi
# The public origin reaches the Deployment as adapter-node's ORIGIN, and forwarded headers can be trusted instead.
env_of() { yq -N "select(.kind == \"Deployment\") | .spec.template.spec.containers[0].env[] | select(.name == \"$2\") | .value" <<<"$1"; }
[ "$(env_of "$(helm template x "$here/../../dashboard/charts/kubemoot-dashboard" --set auth.origin=https://dash.example.test)" ORIGIN)" = "https://dash.example.test" ] || fail "auth.origin did not reach the Deployment as ORIGIN"
[ -z "$(env_of "$(helm template x "$here/../../dashboard/charts/kubemoot-dashboard")" ORIGIN)" ] || fail "ORIGIN is set by default"
[ "$(env_of "$(helm template x "$here/../../dashboard/charts/kubemoot-dashboard" --set auth.trustForwardedHeaders=true)" HOST_HEADER)" = "x-forwarded-host" ] || fail "auth.trustForwardedHeaders did not set HOST_HEADER"
# Sign-in settings reach the Deployment, and anonymous viewing is off unless asked for.
anon_env() { yq -N 'select(.kind == "Deployment") | .spec.template.spec.containers[0].env[] | select(.name == "KUBEMOOT_DASHBOARD_ANONYMOUS_READ_ONLY") | .value' <<<"$1"; }
[ "$(anon_env "$(helm template x "$here/../../dashboard/charts/kubemoot-dashboard")")" = "false" ] || fail "anonymous read-only is not off by default"
[ "$(anon_env "$(helm template x "$here/../../dashboard/charts/kubemoot-dashboard" --set auth.anonymousReadOnly=true)")" = "true" ] || fail "auth.anonymousReadOnly did not reach the Deployment"
# The dashboard Deployments carry no variable that turns writes back on.
if grep -q 'KUBEMOOT_DASHBOARD_READ_ONLY' <<<"$out"; then fail "a dashboard Deployment still sets the removed read-only switch"; fi

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
# same image as the main dashboard, scoped by environment, read verbs only.
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
[ "$(ws_env KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR)" = "team=on" ] || fail "workshop selector not set"
[ "$(ws_env KUBEMOOT_DASHBOARD_INFRA_NAMESPACE)" = "moot" ] || fail "workshop infra namespace not set"
main_sel="$(yq -N 'select(.kind == "Service" and .metadata.name == "kubemoot-operator-dashboard") | .spec.selector["app.kubernetes.io/name"]' <<<"$out")"
ws_sel="$(yq -N 'select(.kind == "Service" and .metadata.name == "kubemoot-operator-dashboard-workshop") | .spec.selector["app.kubernetes.io/name"]' <<<"$out")"
[ "$main_sel" != "$ws_sel" ] || fail "workshop and dashboard Services select the same pods"
verbs="$(yq -N 'select(.kind == "ClusterRole" and .metadata.name == "kubemoot-operator-dashboard-workshop-reader") | .rules[].verbs[]' <<<"$out" | sort -u | tr '\n' ' ')"
case "$verbs" in "get list watch ") ;; *) fail "workshop ClusterRole verbs are '$verbs', want only get list watch" ;; esac
# Captured first: a pipe into grep -q can fail with SIGPIPE under pipefail, which would
# make these "must not match" checks pass when they should fail.
resources="$(yq -N 'select(.kind == "ClusterRole" and .metadata.name == "kubemoot-operator-dashboard-workshop-reader") | .rules[].resources[]' <<<"$out")"
if grep -qx 'secrets' <<<"$resources"; then fail "workshop ClusterRole can read Secrets"; fi
if grep -qx 'pods' <<<"$resources"; then fail "workshop ClusterRole reads pods cluster-wide"; fi
[ "$(yq -N 'select(.kind == "Role" and .metadata.name == "kubemoot-operator-dashboard-workshop-pods") | .metadata.namespace' <<<"$out")" = "moot" ] \
  || fail "workshop pod Role is not in the infra namespace"
# The main dashboard is untouched by the option.
a="$(render kubemoot-operator --set dashboard.enabled=true | yq -N 'select(.metadata.name == "kubemoot-operator-dashboard*" and .metadata.name != "*-workshop*")')"
b="$(yq -N 'select(.metadata.name == "kubemoot-operator-dashboard*" and .metadata.name != "*-workshop*")' <<<"$out")"
[ "$a" = "$b" ] || fail "enabling the workshop changed the main dashboard objects"
# An empty selector would show every namespace: the chart refuses it.
render kubemoot-operator --set dashboard.enabled=true --set dashboard.workshop.enabled=true \
  --set dashboard.workshop.namespaceSelector= >/dev/null 2>&1 && fail "empty workshop selector was accepted"

# kubemoot-viewer: anyone with `view` reads Kubemoot resources. It joins view, edit and admin by
# the standard aggregation labels, names every CRD kind (and only kinds: no wildcard, which would
# also reach the audited crews/conversations subresource), and holds read verbs only.
viewer="$(render kubemoot-operator | yq -N 'select(.kind == "ClusterRole" and .metadata.name == "kubemoot-viewer")')"
[ -n "$viewer" ] || fail "the chart renders no kubemoot-viewer ClusterRole"
for target in view edit admin; do
  [ "$(yq -N ".metadata.labels[\"rbac.authorization.k8s.io/aggregate-to-$target\"]" <<<"$viewer")" = "true" ] || fail "kubemoot-viewer is not aggregated to $target"
done
viewer_verbs="$(yq -N '.rules[].verbs[]' <<<"$viewer" | sort -u | tr '\n' ' ')"
[ "$viewer_verbs" = "get list watch " ] || fail "kubemoot-viewer verbs are '$viewer_verbs', want only get list watch"
viewer_groups="$(yq -N '.rules[].apiGroups[]' <<<"$viewer" | sort -u | tr '\n' ' ')"
[ "$viewer_groups" = "kubemoot.ai " ] || fail "kubemoot-viewer reaches '$viewer_groups', want only kubemoot.ai"
viewer_resources="$(yq -N '.rules[].resources[]' <<<"$viewer")"
if grep -q '[*/]' <<<"$viewer_resources"; then fail "kubemoot-viewer names a wildcard or subresource"; fi
for crd in "$chart"/crds/*.yaml; do
  plural="$(yq -N '.spec.names.plural' "$crd")"
  grep -qx "$plural" <<<"$viewer_resources" || fail "kubemoot-viewer does not read $plural"
done
# The check must be able to fail: a write verb added to the rendered role is caught.
bad="$(yq -N '.rules[0].verbs += ["delete"]' <<<"$viewer" | yq -N '.rules[].verbs[]' | sort -u | tr '\n' ' ')"
[ "$bad" != "get list watch " ] || fail "the viewer verbs check accepted a delete verb"

# kubemoot-operator-reader: what the operator page reads beyond `view` (CRDs and the leader lease).
# Read verbs only, never aggregated into the built-in roles, and no wildcard.
reader="$(render kubemoot-operator | yq -N 'select(.kind == "ClusterRole" and .metadata.name == "kubemoot-operator-reader")')"
[ -n "$reader" ] || fail "the chart renders no kubemoot-operator-reader ClusterRole"
reader_verbs="$(yq -N '.rules[].verbs[]' <<<"$reader" | sort -u | tr '\n' ' ')"
[ "$reader_verbs" = "get list watch " ] || fail "kubemoot-operator-reader verbs are '$reader_verbs', want only get list watch"
reader_rules="$(yq -N '.rules[] | (.apiGroups | join(",")) + "/" + (.resources | join(","))' <<<"$reader" | sort | tr '\n' ' ')"
[ "$reader_rules" = "apiextensions.k8s.io/customresourcedefinitions coordination.k8s.io/leases " ] || fail "kubemoot-operator-reader reads '$reader_rules'"
if yq -N '.metadata.labels | keys | .[]' <<<"$reader" | grep -q 'rbac.authorization.k8s.io/aggregate-to'; then fail "kubemoot-operator-reader is aggregated into a built-in role"; fi
bad="$(yq -N '.rules[0].verbs += ["delete"]' <<<"$reader" | yq -N '.rules[].verbs[]' | sort -u | tr '\n' ' ')"
[ "$bad" != "get list watch " ] || fail "the operator reader verbs check accepted a delete verb"

# Standalone chart keeps its own defaults.
standalone="$(helm template x "$here/../../dashboard/charts/kubemoot-dashboard")"
grep -q 'image: ghcr.io/kubemoot/dashboard:' <<<"$standalone" || fail "standalone dashboard image changed"
echo "dashboard subchart tests: PASS"
