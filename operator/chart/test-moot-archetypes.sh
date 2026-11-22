#!/usr/bin/env bash
# Render test for the MootArchetypes the operator chart ships: consent-3-unratified is
# consent-3 with ratification off and nothing else different. Needs helm and yq.
# Run from anywhere: operator/chart/test-moot-archetypes.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
out="$(helm template kubemoot-operator "$chart" --namespace kubemoot)"
spec() { yq -N -o=json -I=0 "select(.kind == \"MootArchetype\" and .metadata.name == \"$1\") | .spec | $2" <<<"$out"; }

[ "$(spec consent-3 '.ratify')" = "null" ] || fail "consent-3 declares a ratify block; absent means ratify"
[ "$(spec consent-3-unratified '.ratify')" = '{"enabled":false}' ] || fail "consent-3-unratified must set ratify.enabled false"
a="$(spec consent-3 'del(.description)')"
b="$(spec consent-3-unratified 'del(.description) | del(.ratify)')"
[ -n "$a" ] || fail "consent-3 did not render"
[ "$a" = "$b" ] || fail "the two archetypes differ beyond ratify"
echo "PASS: consent-3-unratified is consent-3 with ratification off"
