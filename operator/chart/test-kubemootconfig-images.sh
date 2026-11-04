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

# The coordinator decision log is on unless the chart turns it off, and the KubemootConfig
# CRD has the field the chart sets and defaults it, also when spec.coordinator is absent.
crd_file="$chart/crds/kubemoot.ai_kubemootconfigs.yaml"
crd_coordinator=".spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.coordinator"
coordinator_log() { helm template kubemoot-operator "$chart" --namespace kubemoot "$@" \
  | yq -N 'select(.kind == "KubemootConfig") | .spec.coordinator.eventLog'; }
[ "$(coordinator_log)" = "true" ] || fail "coordinator.eventLog should default to true"
[ "$(coordinator_log --set coordinator.eventLog=false --set coordinator.resume=false)" = "false" ] \
  || fail "coordinator.eventLog=false does not reach KubemootConfig"
[ "$(coordinator_log --set coordinator.eventLog=true)" = "true" ] \
  || fail "coordinator.eventLog=true does not reach KubemootConfig"
[ "$(yq -N "$crd_coordinator.properties.eventLog.type" "$crd_file")" = "boolean" ] \
  || fail "the KubemootConfig CRD has no boolean spec.coordinator.eventLog"
[ "$(yq -N "$crd_coordinator.properties.eventLog.default" "$crd_file")" = "true" ] \
  || fail "CRD coordinator.eventLog default is not true"
[ "$(yq -N "$crd_coordinator.default | tag" "$crd_file")" = "!!map" ] \
  || fail "CRD coordinator has no object default, so an absent coordinator would skip the eventLog and resume defaults"

# Resume is on unless the chart turns it off, reaches the KubemootConfig, needs the log (the
# refusal tells a user who turns the log off to turn resume off too), and the CRD has the
# field and defaults it.
coordinator_resume() { helm template kubemoot-operator "$chart" --namespace kubemoot "$@" \
  | yq -N 'select(.kind == "KubemootConfig") | .spec.coordinator.resume'; }
[ "$(coordinator_resume)" = "true" ] || fail "coordinator.resume should default to true"
[ "$(coordinator_resume --set coordinator.resume=false)" = "false" ] \
  || fail "coordinator.resume=false does not reach KubemootConfig"
[ "$(coordinator_resume --set coordinator.eventLog=false --set coordinator.resume=false)" = "false" ] \
  || fail "the log and resume off together should render"
refused_sets=("--set coordinator.eventLog=false" "--set coordinator.eventLog=false --set coordinator.resume=true")
for refused in "${refused_sets[@]}"; do
  # shellcheck disable=SC2086
  if err="$(helm template kubemoot-operator "$chart" --namespace kubemoot $refused 2>&1 >/dev/null)"; then
    fail "the log off with resume on ($refused) should be refused"
  fi
  case "$err" in
    *"must also set coordinator.resume to false"*) ;;
    *) fail "the refusal does not tell the user to turn resume off too: $err" ;;
  esac
done
[ "$(yq -N "$crd_coordinator.properties.resume.type" "$crd_file")" = "boolean" ] \
  || fail "the KubemootConfig CRD has no boolean spec.coordinator.resume"
[ "$(yq -N "$crd_coordinator.properties.resume.default" "$crd_file")" = "true" ] \
  || fail "CRD coordinator.resume default is not true"

# Leases are off unless the chart turns them on (they pay off only with more than one coordinator
# replica), reach the KubemootConfig, need resume (and so the log), create the lease bucket only
# when on, and the CRD has the field and does not default it on.
coordinator_lease() { helm template kubemoot-operator "$chart" --namespace kubemoot "$@" \
  | yq -N 'select(.kind == "KubemootConfig") | .spec.coordinator.lease'; }
[ "$(coordinator_lease)" = "false" ] || fail "coordinator.lease should default to false"
[ "$(coordinator_lease --set coordinator.lease=true)" = "true" ] \
  || fail "coordinator.lease=true does not reach KubemootConfig"
if err="$(helm template kubemoot-operator "$chart" --namespace kubemoot --set coordinator.resume=false --set coordinator.lease=true 2>&1 >/dev/null)"; then
  fail "coordinator.lease=true without coordinator.resume should be refused"
fi
case "$err" in
  *"coordinator.lease needs coordinator.resume"*) ;;
  *) fail "the lease refusal does not name resume: $err" ;;
esac
if helm template kubemoot-operator "$chart" --namespace kubemoot | grep -q kubemoot_coordinator_leases; then
  fail "the lease bucket should not be created while coordinator.lease is off"
fi
helm template kubemoot-operator "$chart" --namespace kubemoot --set coordinator.lease=true \
  | grep -q "nats kv add kubemoot_coordinator_leases --ttl=15s" || fail "coordinator.lease=true should create the lease bucket with a 15s TTL"
[ "$(yq -N "$crd_coordinator.properties.lease.type" "$crd_file")" = "boolean" ] \
  || fail "the KubemootConfig CRD has no boolean spec.coordinator.lease"
[ "$(yq -N "$crd_coordinator.properties.lease.default" "$crd_file")" = "null" ] \
  || fail "CRD coordinator.lease must not default on"

# The resume age limit defaults to 30m, takes durations, renders 0 or empty as 0 (no limit),
# refuses anything else, and the CRD has the field.
resume_age() { helm template kubemoot-operator "$chart" --namespace kubemoot "$@" \
  | yq -N 'select(.kind == "KubemootConfig") | .spec.coordinator.resumeMaxAge'; }
[ "$(resume_age)" = "30m" ] || fail "coordinator.resumeMaxAge should default to 30m"
[ "$(resume_age --set coordinator.resumeMaxAge=1h30m)" = "1h30m" ] || fail "coordinator.resumeMaxAge=1h30m does not reach KubemootConfig"
[ "$(resume_age --set coordinator.resumeMaxAge=1.5h)" = "1.5h" ] || fail "coordinator.resumeMaxAge=1.5h should be accepted"
[ "$(resume_age --set coordinator.resumeMaxAge=0s)" = "0s" ] || fail "coordinator.resumeMaxAge=0s should be accepted"
[ "$(resume_age --set coordinator.resumeMaxAge=1.5ns)" = "1.5ns" ] || fail "coordinator.resumeMaxAge=1.5ns should be accepted"
[ "$(resume_age --set coordinator.resumeMaxAge=0)" = "0" ] || fail "coordinator.resumeMaxAge=0 should render 0"
[ "$(resume_age --set coordinator.resumeMaxAge=)" = "0" ] || fail "an empty coordinator.resumeMaxAge should render 0"
# The runtime accepts and refuses the same strings (ResumeAge, CoordinatorResumeAgeTest).
not_durations=(soon 30 -5m 5d "30 m" .5s 1.s $'\xc2\xb5s' 1h-30m)
for bad in "${not_durations[@]}"; do
  if helm template kubemoot-operator "$chart" --namespace kubemoot --set "coordinator.resumeMaxAge=$bad" >/dev/null 2>&1; then
    fail "coordinator.resumeMaxAge=$bad should be refused"
  fi
done
# A KubemootConfig applied directly gets the same answer from the CRD pattern as the chart gives.
age_pattern="$(yq -N '.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.coordinator.properties.resumeMaxAge.pattern' \
  "$chart/crds/kubemoot.ai_kubemootconfigs.yaml")"
for good in "" 0 0s 30m 1h30m 1.5h 250ms 1.5ns; do
  printf '%s\n' "$good" | grep -Eq "$age_pattern" || fail "the CRD pattern refuses '$good'"
done
for bad in "${not_durations[@]}"; do
  if printf '%s\n' "$bad" | grep -Eq "$age_pattern"; then fail "the CRD pattern accepts '$bad'"; fi
done
[ "$(yq -N '.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.coordinator.properties.resumeMaxAge.type' \
  "$chart/crds/kubemoot.ai_kubemootconfigs.yaml")" = "string" ] \
  || fail "the KubemootConfig CRD has no string spec.coordinator.resumeMaxAge"

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

# The removed artifacts.perCallArtifacts value: a values file that still sets it renders, and the
# KubemootConfig and its CRD no longer carry the field (every tool result is always stored).
has_per_call() { helm template kubemoot-operator "$chart" --namespace kubemoot "$@" \
  | yq -N 'select(.kind == "KubemootConfig") | .spec.artifacts | has("perCallArtifacts")'; }
[ "$(has_per_call)" = "false" ] || fail "KubemootConfig still carries artifacts.perCallArtifacts"
[ "$(has_per_call --set artifacts.perCallArtifacts=true)" = "false" ] \
  || fail "a values file that sets the removed artifacts.perCallArtifacts must render without the field"
[ "$(yq -N "$crd_artifacts.properties | has(\"perCallArtifacts\")" "$chart/crds/kubemoot.ai_kubemootconfigs.yaml")" = "false" ] \
  || fail "CRD still has perCallArtifacts"

echo "all KubemootConfig image render tests passed"
