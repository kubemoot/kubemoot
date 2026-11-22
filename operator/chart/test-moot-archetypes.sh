#!/usr/bin/env bash
# Render test for the MootArchetypes the operator chart ships: consent-3 turns ratification
# off (it is opt-in) and the retired consent-3-unratified is no longer rendered.
# Needs helm and yq. Run from anywhere: operator/chart/test-moot-archetypes.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
out="$(helm template kubemoot-operator "$chart" --namespace kubemoot)"
spec() { yq -N -o=json -I=0 "select(.kind == \"MootArchetype\" and .metadata.name == \"$1\") | .spec | $2" <<<"$out"; }

[ -n "$(spec consent-3 '.phases')" ] || fail "consent-3 did not render"
[ "$(spec consent-3 '.ratify')" = '{"enabled":false}' ] || fail "consent-3 must set ratify.enabled false"
[ -z "$(spec consent-3-unratified '.phases')" ] || fail "consent-3-unratified is folded into consent-3 and must not render"
echo "PASS: consent-3 ships with ratification off"
