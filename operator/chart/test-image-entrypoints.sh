#!/usr/bin/env bash
# Render tests: the chart's Kubemoot images start through their own entrypoint. The
# buildpacks images keep their binary under /workspace and start it with the launcher
# at /cnb/process/web, so a `command` naming a path such as /manager would not resolve.
# Needs helm and yq. Run from anywhere: operator/chart/test-image-entrypoints.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

failures=0
fail() { echo "FAIL: $*" >&2; failures=$((failures + 1)); }

# container_field RENDER DEPLOYMENT CONTAINER FIELD: one field of a Deployment's container.
container_field() {
  yq -N "select(.kind == \"Deployment\" and .metadata.name == \"$2\") | .spec.template.spec.containers[] | select(.name == \"$3\") | .$4" <<<"$1"
}

out="$(helm template kubemoot-operator "$chart")"

[ "$(container_field "$out" kubemoot-operator manager name)" = manager ] \
  || fail "no manager container in the operator Deployment"
[ "$(container_field "$out" kubemoot-operator manager command)" = null ] \
  || fail "the operator container sets a command; the image entrypoint must start the manager"
container_field "$out" kubemoot-operator manager 'args[]' | grep -qx -- '--health-probe-bind-address=:8081' \
  || fail "the operator container lost its args"

[ "$(container_field "$out" kubemoot-operator-liaison crew-liaison name)" = crew-liaison ] \
  || fail "no crew-liaison container in the liaison Deployment"
[ "$(container_field "$out" kubemoot-operator-liaison crew-liaison command)" = null ] \
  || fail "the crew-liaison container sets a command; the image entrypoint must start it"

# No Kubemoot-built image in the chart is started by a root-level binary path.
kubemoot_cmds="$(yq -N '.. | select(has("image") and has("command")) | select(.image | test("kubemoot|/(crew-liaison|kubemoot-operator|mcp-bridge|discussion-gateway|fitness-runner|scheduling-mcp|artifact-access):")) | .command[]' <<<"$out")"
[ -z "$kubemoot_cmds" ] || fail "a Kubemoot image is started by an explicit command: ${kubemoot_cmds}"

if [ "$failures" -gt 0 ]; then
  echo "${failures} check(s) failed"
  exit 1
fi
echo "all image entrypoint render tests passed"
