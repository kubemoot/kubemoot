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
echo "ok"
