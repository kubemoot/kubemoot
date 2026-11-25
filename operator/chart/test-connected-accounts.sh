#!/usr/bin/env bash
# Render tests for the operator's list of registered connected accounts
# (templates/deployment.yaml): none by default, the account names of liaison.connectedAccounts.accounts
# sorted and comma separated otherwise. Needs helm and yq. Run from anywhere:
# operator/chart/test-connected-accounts.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
render() { helm template kubemoot-operator "$chart" --namespace km "$@"; }
# registered <rendered>: the operator's KUBEMOOT_CONNECTED_ACCOUNTS value, empty when unset.
registered() {
  yq -N 'select(.kind == "Deployment" and .metadata.name == "kubemoot-operator") | .spec.template.spec.containers[0].env[] | select(.name == "KUBEMOOT_CONNECTED_ACCOUNTS") | .value' <<<"$1"
}

[ -z "$(registered "$(render)")" ] || fail "the default render registers accounts"
[ -z "$(registered "$(render --set-json 'liaison.connectedAccounts.accounts={}')")" ] || fail "an empty map registers accounts"

out="$(render --set-json 'liaison.connectedAccounts.accounts={"library":{},"archive":{"clientId":"abc"}}')"
[ "$(registered "$out")" = "archive,library" ] || fail "registered accounts are '$(registered "$out")'"

# An account name that is not a DNS label refuses to render.
for bad in 'a,b' 'Library' 'has space' '-x'; do
  if render --set-json "liaison.connectedAccounts.accounts={\"$bad\":{}}" >/dev/null 2>&1; then
    fail "account name '$bad' rendered"
  fi
done

# No client id, secret or other setting of an account reaches the operator's environment.
if grep -q 'abc' <<<"$(yq -N 'select(.kind == "Deployment" and .metadata.name == "kubemoot-operator") | .spec.template.spec.containers[0].env' <<<"$out")"; then
  fail "an account setting leaked into the operator environment"
fi
echo "ok: connected accounts render tests"
