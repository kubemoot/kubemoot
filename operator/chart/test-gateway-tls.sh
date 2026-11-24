#!/usr/bin/env bash
# Render tests for the discussion gateways' TLS: the operator is told to serve it and whether the
# plain listener stays, the liaison verifies the gateways (and accepts plain http only when TLS
# is off), the dashboard notes warn when it would call gateways that serve none. Needs helm and
# yq. Run from anywhere: operator/chart/test-gateway-tls.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
render() { helm template k "$chart" --namespace km --set dashboard.enabled=true "$@"; }
env_of() { yq -N "select(.kind == \"Deployment\" and .metadata.name == \"$1\") | .spec.template.spec.containers[0].env[] | .name + \"=\" + (.value // \"(from secret)\")" <<<"$2"; }
notes() { helm install k "$chart" --namespace km --dry-run=client --set dashboard.enabled=true "$@" 2>/dev/null; }

out="$(render)"
op="$(env_of k-kubemoot-operator "$out")"
grep -q '^GATEWAY_TLS_ENABLED=true$' <<<"$op" || fail "gateway TLS is not on by default"
grep -q '^GATEWAY_TLS_PLAIN_LISTENER=true$' <<<"$op" || fail "the plain listener should stay on by default"
grep -q 'LIAISON_ALLOW_INSECURE_GATEWAY' <<<"$(env_of k-kubemoot-operator-liaison "$out")" && fail "the liaison accepts plain http to the gateways with TLS on"
grep -q '^KUBEMOOT_DASHBOARD_GATEWAY_URL=' <<<"$(env_of k-dashboard "$out")" && fail "the dashboard gets a gateway URL, so it would not default to https"
notes | grep -q 'calls each crew' && fail "the dashboard note appears with TLS on"

out="$(render --set discussionGateway.tls.plainListener=false)"
grep -q '^GATEWAY_TLS_PLAIN_LISTENER=false$' <<<"$(env_of k-kubemoot-operator "$out")" || fail "plainListener=false is not passed on"

out="$(render --set discussionGateway.tls.enabled=false)"
grep -q '^GATEWAY_TLS_ENABLED=false$' <<<"$(env_of k-kubemoot-operator "$out")" || fail "TLS off is not passed on"
grep -q '^LIAISON_ALLOW_INSECURE_GATEWAY=true$' <<<"$(env_of k-kubemoot-operator-liaison "$out")" || fail "the liaison is not told gateways are plain with TLS off"
notes --set discussionGateway.tls.enabled=false | grep -q 'calls each crew' || fail "no note that the dashboard cannot reach gateways with TLS off"
notes --set discussionGateway.tls.enabled=false --set dashboard.auth.gatewayUrl=http://x | grep -q 'calls each crew' && fail "the note appears although a gateway URL is set"
echo "ok"
