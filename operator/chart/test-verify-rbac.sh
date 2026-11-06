#!/usr/bin/env bash
# Render tests for the verify runner's RBAC (verify.enabled): Secret reads are a Role in the
# release namespace, never part of the ClusterRole. Needs helm and yq.
# Run from anywhere: operator/chart/test-verify-rbac.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
render() { helm template kubemoot-operator "$chart" --namespace "${NS:-kubemoot}" "$@"; }
# names <rendered> <kind>: the names of all documents of a kind, one per line.
names() { yq -N "select(.kind == \"$2\") | .metadata.name" <<<"$1"; }
# rules_with <rendered> <kind> <name> <resource>: how many rules of the object name the resource.
rules_with() {
  yq -N "select(.kind == \"$2\" and .metadata.name == \"$3\") | .rules[] | select(.resources[] == \"$4\") | .resources[0]" <<<"$1" | grep -c . || true
}

# Off by default: no verify objects at all.
off="$(render)"
grep -q 'kubemoot-verify-runner' <<<"$off" && fail "the default render contains verify runner objects"

out="$(render --set verify.enabled=true)"

# The ClusterRole reads no Secrets.
[ "$(rules_with "$out" ClusterRole kubemoot-verify-runner secrets)" = 0 ] || fail "the ClusterRole still names secrets"
[ "$(rules_with "$out" ClusterRole kubemoot-verify-runner pods)" = 1 ] || fail "the ClusterRole lost its pod rule"
[ "$(rules_with "$out" ClusterRole kubemoot-verify-runner configmaps)" = 1 ] || fail "the ClusterRole lost its ConfigMap rule"

# A Role in the release namespace carries the Secret read, read-only.
role=kubemoot-verify-runner-secrets
[ "$(names "$out" Role | grep -c "^kubemoot-verify-runner" || true)" = 1 ] || fail "want exactly one verify runner Role"
[ "$(yq -N "select(.kind == \"Role\" and .metadata.name == \"$role\") | .metadata.namespace" <<<"$out")" = kubemoot ] \
  || fail "the Role is not in the release namespace"
[ "$(rules_with "$out" Role "$role" secrets)" = 1 ] || fail "the Role has no Secret rule"
verbs="$(yq -N "select(.kind == \"Role\" and .metadata.name == \"$role\") | .rules[].verbs[]" <<<"$out" | sort | paste -sd, -)"
[ "$verbs" = "get,list,watch" ] || fail "the Role verbs are '$verbs', want read-only"
[ "$(yq -N "select(.kind == \"Role\" and .metadata.name == \"$role\") | .rules | length" <<<"$out")" = 1 ] \
  || fail "the Role holds more than the Secret rule"

# The RoleBinding joins the runner's ServiceAccount to that Role, in the same namespace.
bind() { yq -N "select(.kind == \"RoleBinding\" and .metadata.name == \"$role\") | $1" <<<"$out"; }
[ "$(bind .roleRef.kind)/$(bind .roleRef.name)" = "Role/$role" ] || fail "the RoleBinding names the wrong role"
[ "$(bind .subjects[0].name)@$(bind .subjects[0].namespace)" = "kubemoot-verify-runner@kubemoot" ] \
  || fail "the RoleBinding names the wrong subject"
[ "$(bind .metadata.namespace)" = kubemoot ] || fail "the RoleBinding is not in the release namespace"

# No ClusterRoleBinding reaches a Secret rule: the only ClusterRole bound to the runner has none.
[ "$(yq -N 'select(.kind == "ClusterRoleBinding" and .subjects[0].name == "kubemoot-verify-runner") | .roleRef.name' <<<"$out")" \
  = kubemoot-verify-runner ] || fail "the runner's ClusterRoleBinding changed"

# The Role follows the release namespace.
other="$(NS=tests render --set verify.enabled=true)"
[ "$(yq -N "select(.kind == \"Role\" and .metadata.name == \"$role\") | .metadata.namespace" <<<"$other")" = tests ] \
  || fail "the Role does not follow the release namespace"

echo "PASS: verify runner RBAC render tests"
