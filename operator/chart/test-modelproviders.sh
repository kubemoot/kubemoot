#!/usr/bin/env bash
# Render tests for the ModelProviders the operator chart declares from its modelProviders
# values. Needs helm and yq. Run from anywhere: operator/chart/test-modelproviders.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
providers() { helm template kubemoot-operator "$chart" "$@" | yq 'select(.kind == "ModelProvider")'; }
# field <rendered> <provider name> <yq path>
field() { yq -N "select(.metadata.name == \"$2\") | $3" <<<"$1"; }

# Default: no ModelProviders.
out="$(providers)"
[ -z "$out" ] || fail "default render contains ModelProviders"

out="$(providers \
  --set 'modelProviders[0].name=gpu' --set 'modelProviders[0].type=ollama' \
  --set 'modelProviders[0].endpoint=http://ollama.gpu:11434' --set 'modelProviders[0].schedulingWeight=25' \
  --set 'modelProviders[1].name=cpu' --set 'modelProviders[1].type=ollama' \
  --set 'modelProviders[1].endpoint=http://ollama.cpu:11434' --set 'modelProviders[1].memoryMiB=20480' \
  --set 'modelProviders[2].name=both' --set 'modelProviders[2].type=ollama' \
  --set 'modelProviders[2].endpoint=http://ollama.both:11434' --set 'modelProviders[2].schedulingWeight=50' \
  --set 'modelProviders[2].memoryMiB=6144' \
  --set 'modelProviders[3].name=bare' --set 'modelProviders[3].type=ollama' \
  --set 'modelProviders[3].endpoint=http://ollama.bare:11434' --namespace km)"

# Weight only: scheduling.weight, no memoryMiB.
[ "$(field "$out" gpu .spec.scheduling.weight)" = "25" ] || fail "schedulingWeight did not render as scheduling.weight"
[ "$(field "$out" gpu .spec.scheduling.memoryMiB)" = "null" ] || fail "memoryMiB rendered without being set"

# memoryMiB only: the scheduling block renders with memoryMiB and no weight.
[ "$(field "$out" cpu .spec.scheduling.memoryMiB)" = "20480" ] || fail "memoryMiB did not render as scheduling.memoryMiB"
[ "$(field "$out" cpu .spec.scheduling.weight)" = "null" ] || fail "weight rendered without schedulingWeight"
[ "$(field "$out" cpu '.spec.scheduling.memoryMiB | tag')" = "!!int" ] || fail "memoryMiB is not an integer"

# Both set.
[ "$(field "$out" both .spec.scheduling.weight)" = "50" ] || fail "weight missing when both are set"
[ "$(field "$out" both .spec.scheduling.memoryMiB)" = "6144" ] || fail "memoryMiB missing when both are set"

# Neither set: no scheduling block at all.
[ "$(field "$out" bare .spec.scheduling)" = "null" ] || fail "scheduling block rendered with neither value set"
[ "$(field "$out" bare .spec.endpoint)" = "http://ollama.bare:11434" ] || fail "endpoint not rendered"
[ "$(field "$out" bare .metadata.namespace)" = "km" ] || fail "namespace does not default to the release namespace"

echo "PASS: modelProviders render tests"
