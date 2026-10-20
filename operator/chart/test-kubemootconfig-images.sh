#!/usr/bin/env bash
# Render tests for the images the operator chart puts in KubemootConfig.
# Needs helm and yq. Run from anywhere: operator/chart/test-kubemootconfig-images.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
config_images() { helm template kubemoot-operator "$chart" --namespace kubemoot "$@" \
  | yq -N 'select(.kind == "KubemootConfig") | .spec.images'; }

out="$(config_images)"
[ -n "$out" ] || fail "no KubemootConfig rendered"

# Every Kubemoot-built image is pinned to an explicit tag (0.0.0 in git, stamped at
# release), never latest. artifactAccess is how a crew's read-ops MCPServer runs the
# artifact-access image without naming a version.
for key in indexer queryService agentRuntime mcpGateway mcpBridge discussionGateway fitnessRunner artifactAccess; do
  value="$(yq -N ".$key" <<<"$out")"
  [ -n "$value" ] && [ "$value" != null ] || fail "KubemootConfig spec.images.$key is not rendered"
  case "$value" in *:latest) fail "spec.images.$key floats: $value" ;; esac
done

[ "$(yq -N .artifactAccess <<<"$out")" = "ghcr.io/kubemoot/artifact-access:0.0.0" ] \
  || fail "artifactAccess should be the registry-prefixed artifact-access image"

# A private mirror prefixes it like every other Kubemoot image.
mirrored="$(config_images --set global.imageRegistry=harbor.example/kubemoot | yq -N .artifactAccess)"
[ "$mirrored" = "harbor.example/kubemoot/artifact-access:0.0.0" ] \
  || fail "artifactAccess ignores global.imageRegistry: $mirrored"

echo "all KubemootConfig image render tests passed"
