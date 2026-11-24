#!/usr/bin/env bash
# Render tests for the aggregated report API: nothing by default (no APIService, no
# listener flag); enabled, an APIService for reports.kubemoot.ai that verifies TLS, a
# Service, the operator's read of one named ConfigMap in kube-system, and the door
# ClusterRole; both certificate modes.
# Needs helm and yq. Run from anywhere: operator/chart/test-reports-apiservice.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
render() { helm template k "$chart" --namespace kubemoot "$@"; }
sel() { yq -N "select(.kind == \"$1\" and .metadata.name == \"$2\") | $3" <<<"$4"; }

out="$(render)"
[ -z "$(yq -N 'select(.kind == "APIService") | .kind' <<<"$out")" ] || fail "an APIService is rendered by default"
grep -q 'reports-api' <<<"$out" && fail "the report API is rendered by default"

out="$(render --set reportsAPI.enabled=true)"
svc=k-kubemoot-operator-reports-api
[ "$(sel APIService v1alpha1.reports.kubemoot.ai .spec.group "$out")" = reports.kubemoot.ai ] || fail "APIService group"
[ "$(sel APIService v1alpha1.reports.kubemoot.ai .spec.insecureSkipTLSVerify "$out")" = false ] || fail "APIService skips TLS verification"
[ "$(sel APIService v1alpha1.reports.kubemoot.ai .spec.service.name "$out")" = "$svc" ] || fail "APIService service"
[ "$(sel APIService v1alpha1.reports.kubemoot.ai '.metadata.annotations."cert-manager.io/inject-ca-from"' "$out")" = "kubemoot/$svc" ] || fail "CA injection"
[ "$(sel Role "$svc-front-proxy" '.metadata.namespace' "$out")" = kube-system ] || fail "front proxy Role namespace"
[ "$(sel Role "$svc-front-proxy" '.rules[0].resourceNames[0]' "$out")" = extension-apiserver-authentication ] || fail "front proxy Role is not one named ConfigMap"
[ "$(sel Role "$svc-front-proxy" '.rules[0].verbs | join(",")' "$out")" = get ] || fail "front proxy Role verbs"
[ "$(sel RoleBinding "$svc-front-proxy" '.subjects[0].namespace' "$out")" = kubemoot ] || fail "front proxy RoleBinding subject"
[ "$(sel ClusterRole "$svc" '.rules[0].resources | join(",")' "$out")" = "crews/memory-export,crews/conversation-export,crewfitnesssuites/report" ] || fail "door ClusterRole"
[ "$(sel ClusterRoleBinding "$svc" '.subjects[0].name' "$out")" = system:authenticated ] || fail "door binding"
args="$(yq -N 'select(.kind == "Deployment" and .metadata.name == "k-kubemoot-operator") | .spec.template.spec.containers[0].args[]' <<<"$out")"
grep -q -- '--reports-api-bind-address=:8444' <<<"$args" || fail "listener flag"
grep -q -- '--reports-api-cert-path=' <<<"$args" || fail "certificate flag"
[ -n "$(sel Certificate "$svc" .kind "$out")" ] || fail "no cert-manager Certificate"

out="$(render --set reportsAPI.enabled=true --set reportsAPI.rbac.bindAuthenticated=false)"
[ -z "$(yq -N 'select(.kind == "ClusterRoleBinding" and .metadata.name == "k-kubemoot-operator-reports-api") | .kind' <<<"$out")" ] || fail "door bound although bindAuthenticated is false"

out="$(render --set reportsAPI.enabled=true --set reportsAPI.tls.certManager.enabled=false)"
[ -z "$(sel Certificate "$svc" .kind "$out")" ] || fail "Certificate rendered without cert-manager"
[ "$(sel APIService v1alpha1.reports.kubemoot.ai '.spec.caBundle | length > 100' "$out")" = true ] || fail "no caBundle in chart-made mode"
[ "$(sel Secret "$svc-cert" '.data | keys | sort | join(",")' "$out")" = "ca.crt,tls.crt,tls.key" ] || fail "chart-made Secret keys"
echo "ok"
