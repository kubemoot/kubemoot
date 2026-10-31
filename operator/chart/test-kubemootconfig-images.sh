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
for key in indexer queryService agentRuntime agentSidecar harnessGoose mcpGateway mcpBridge discussionGateway fitnessRunner artifactAccess; do
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

# The placement service reaches the operator only when the chart names it.
placement_env() { helm template kubemoot-operator "$chart" --namespace kubemoot "$@" \
  | yq -N 'select(.kind == "Deployment") | .spec.template.spec.containers[0].env[] | select(.name == "PLACEMENT_SERVICE_URL") | .value'; }
[ -z "$(placement_env)" ] || fail "PLACEMENT_SERVICE_URL is set without a placementService.url"
[ "$(placement_env --set placementService.url=http://placement:8080)" = "http://placement:8080" ] \
  || fail "placementService.url does not reach the operator as PLACEMENT_SERVICE_URL"

# The coordinator decision log is off unless the chart turns it on, and the KubemootConfig
# CRD has the field the chart sets.
coordinator_log() { helm template kubemoot-operator "$chart" --namespace kubemoot "$@" \
  | yq -N 'select(.kind == "KubemootConfig") | .spec.coordinator.eventLog'; }
[ "$(coordinator_log)" = "false" ] || fail "coordinator.eventLog should default to false"
[ "$(coordinator_log --set coordinator.eventLog=true)" = "true" ] \
  || fail "coordinator.eventLog=true does not reach KubemootConfig"
[ "$(yq -N '.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.coordinator.properties.eventLog.type' \
  "$chart/crds/kubemoot.ai_kubemootconfigs.yaml")" = "boolean" ] \
  || fail "the KubemootConfig CRD has no boolean spec.coordinator.eventLog"

# The artifact query engine defaults to sqlite, takes duckdb, takes none (no query tool),
# and refuses any other value at render time.
query_engine() { helm template kubemoot-operator "$chart" --namespace kubemoot "$@" \
  | yq -N 'select(.kind == "KubemootConfig") | .spec.artifacts.queryEngine'; }
[ "$(query_engine)" = "sqlite" ] || fail "artifacts.queryEngine should default to sqlite"
for engine in sqlite duckdb none; do
  [ "$(query_engine --set artifacts.queryEngine=$engine)" = "$engine" ] \
    || fail "artifacts.queryEngine=$engine does not reach KubemootConfig"
done
# The CRD defaults the field the same way, also when spec.artifacts is absent.
crd_artifacts=".spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.artifacts"
[ "$(yq -N "$crd_artifacts.properties.queryEngine.default" "$chart/crds/kubemoot.ai_kubemootconfigs.yaml")" = "sqlite" ] \
  || fail "CRD queryEngine default is not sqlite"
[ "$(yq -N "$crd_artifacts.default | tag" "$chart/crds/kubemoot.ai_kubemootconfigs.yaml")" = "!!map" ] \
  || fail "CRD artifacts has no object default, so an absent artifacts would skip the queryEngine default"
# The engines the chart accepts are the engines the KubemootConfig CRD accepts.
crd_engines="$(yq -N '.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.artifacts.properties.queryEngine.enum | join(",")' \
  "$chart/crds/kubemoot.ai_kubemootconfigs.yaml")"
[ "$crd_engines" = "none,sqlite,duckdb" ] || fail "CRD queryEngine enum is $crd_engines, the chart accepts none,sqlite,duckdb"
if helm template kubemoot-operator "$chart" --namespace kubemoot --set artifacts.queryEngine=oracle >/dev/null 2>&1; then
  fail "artifacts.queryEngine=oracle should fail the render"
fi

echo "all KubemootConfig image render tests passed"
