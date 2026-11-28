#!/usr/bin/env bash
# Render test for the MootArchetypes the operator chart ships: consent-3 turns ratification
# off (it is opt-in), declares the signal kinds the runtime understands, each with a meaning,
# one clarifying-question round (started by a question or a concern that names the agent it doubts) and the generalists-first review order; the deprecated consent-3-unratified stays one release
# and is identical.
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
[ "$(spec consent-3-unratified '.ratify')" = '{"enabled":false}' ] || fail "consent-3-unratified must set ratify.enabled false"
case "$(spec consent-3-unratified '.description')" in *DEPRECATED*) ;; *) fail "consent-3-unratified must be marked DEPRECATED";; esac
signals="$(spec consent-3 '.signals')"
of_signals() { yq -p=json -o=json -I=0 "$1" <<<"$signals"; }
want_kinds='["agree","concern","question","stand_aside","tool_gap","no_data","block"]'
[ "$(of_signals '[.[].name]')" = "$want_kinds" ] || fail "consent-3 must declare the kinds $want_kinds"
[ "$(of_signals '[.[] | select((.meaning // "") == "")] | length')" = "0" ] || fail "every consent-3 signal needs a meaning"
[ "$(of_signals '.[] | select(.name == "question") | [.terminal, .addressed]')" = '[false,true]' ] \
  || fail "question must be addressed and not terminal"
[ "$(of_signals '.[] | select(.name == "concern") | [.terminal, .addressed, .startsRecheck]')" = '[true,true,true]' ] \
  || fail "concern must be addressed and start the named agent's recheck"
[ "$(of_signals '[.[] | select(.startsRecheck == true) | .name]')" = '["concern"]' ] \
  || fail "only concern starts a recheck besides the question round itself"
[ "$(spec consent-3 '.recheck.maxRounds')" = "1" ] || fail "consent-3 must allow one recheck round"
[ "$(spec consent-3 '.review.order')" = '"generalistsFirst"' ] || fail "consent-3 must review generalistsFirst"
[ "$(spec consent-3-unratified '.review.order')" = '"generalistsFirst"' ] || fail "consent-3-unratified must review generalistsFirst"
a="$(spec consent-3 'del(.description)')"
b="$(spec consent-3-unratified 'del(.description)')"
[ "$a" = "$b" ] || fail "the deprecated archetype must stay identical to consent-3"
echo "PASS: consent-3 ships with ratification off, its signals and one recheck round; consent-3-unratified is deprecated and identical"
