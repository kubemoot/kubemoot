#!/usr/bin/env bash
# Render tests for the install CA settings: the operator is told where the CA Secret and the
# published ConfigMap are, and the chart makes no CA of its own, so rendering twice (helm
# template, an offline renderer) gives the same output and never rotates a CA. Needs helm and
# yq. Run from anywhere: operator/chart/test-install-ca.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
render() { helm template k "$chart" --namespace km "$@"; }
env_of() { yq -N 'select(.kind == "Deployment" and .metadata.name == "k-kubemoot-operator") | .spec.template.spec.containers[0].env[] | .name + "=" + (.value // "(from secret)")' <<<"$1"; }

out="$(render)"
env="$(env_of "$out")"
grep -q '^INSTALL_CA_SECRET=kubemoot-ca$' <<<"$env" || fail "operator CA Secret setting"
grep -q '^INSTALL_CA_CONFIGMAP=kubemoot-ca$' <<<"$env" || fail "operator CA ConfigMap setting"
[ -z "$(yq -N 'select(.kind == "Secret" and .metadata.name == "kubemoot-ca") | .kind' <<<"$out")" ] || fail "the chart makes the CA Secret"
[ -z "$(yq -N 'select(.kind == "Certificate" or .kind == "Issuer") | select(.metadata.name == "kubemoot-ca*") | .kind' <<<"$out")" ] || fail "the chart makes a CA with cert-manager"
[ "$out" = "$(render)" ] || fail "two renders differ, so a render could rotate something"

out="$(render --set global.installCA.secret=my-ca --set global.installCA.configMap=my-ca-pub)"
env="$(env_of "$out")"
grep -q '^INSTALL_CA_SECRET=my-ca$' <<<"$env" || fail "global.installCA.secret not passed on"
grep -q '^INSTALL_CA_CONFIGMAP=my-ca-pub$' <<<"$env" || fail "global.installCA.configMap not passed on"
# The report service serves TLS from the install CA, and the pods that send it a token verify it.
render() { helm template k "$chart" --namespace km --set dashboard.enabled=true "$@"; }
out="$(render)"
grep -q '^REPORT_SERVICE_NAME=k-kubemoot-operator-report$' <<<"$(env_of "$out")" || fail "operator does not know its report Service name"
liaison="$(yq -N 'select(.kind == "Deployment" and .metadata.name == "k-kubemoot-operator-liaison") | .spec.template.spec.containers[0].env[] | .name + "=" + (.value // "(from secret)")' <<<"$out")"
grep -q '^LIAISON_OPERATOR_REPORT_URL=https://k-kubemoot-operator-report.km.svc:8082$' <<<"$liaison" || fail "liaison report URL is not https"
grep -q '^LIAISON_CA_FILE=/etc/kubemoot/ca/ca.crt$' <<<"$liaison" || fail "liaison CA file"
grep -q 'LIAISON_ALLOW_INSECURE' <<<"$liaison" && fail "liaison accepts plain http"
[ "$(yq -N 'select(.kind == "Deployment" and .metadata.name == "k-kubemoot-operator-liaison") | .spec.template.spec.volumes[] | select(.name == "install-ca") | .configMap.name' <<<"$out")" = kubemoot-ca ] || fail "liaison does not mount the CA ConfigMap"
dash="$(yq -N 'select(.kind == "Deployment" and .metadata.name == "k-dashboard") | .spec.template.spec.containers[0].env[] | .name + "=" + (.value // "(from secret)")' <<<"$out")"
grep -q '^KUBEMOOT_DASHBOARD_CA_FILE=/etc/kubemoot/ca/ca.crt$' <<<"$dash" || fail "dashboard CA file"
[ "$(yq -N 'select(.kind == "Deployment" and .metadata.name == "k-dashboard") | .spec.template.spec.volumes[] | select(.name == "install-ca") | .configMap.optional' <<<"$out")" = true ] || fail "dashboard CA mount is not optional"
# No Deployment mounts the CA Secret, which holds the key.
[ -z "$(yq -N 'select(.kind == "Deployment") | .spec.template.spec.volumes[] | select(.secret.secretName == "kubemoot-ca") | .name' <<<"$out")" ] || fail "a Deployment mounts the CA key"
# The dashboard follows the one CA ConfigMap name, so the two cannot drift apart.
out="$(helm template k "$chart" --namespace km --set dashboard.enabled=true --set global.installCA.configMap=my-ca-pub)"
[ "$(yq -N 'select(.kind == "Deployment" and .metadata.name == "k-dashboard") | .spec.template.spec.volumes[] | select(.name == "install-ca") | .configMap.name' <<<"$out")" = my-ca-pub ] || fail "the dashboard does not follow global.installCA.configMap"
[ "$(yq -N 'select(.kind == "Deployment" and .metadata.name == "k-kubemoot-operator-liaison") | .spec.template.spec.volumes[] | select(.name == "install-ca") | .configMap.name' <<<"$out")" = my-ca-pub ] || fail "the liaison does not follow global.installCA.configMap"
echo "ok"
