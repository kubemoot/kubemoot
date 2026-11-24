#!/usr/bin/env bash
# Render tests for the images the operator chart puts in KubemootConfig.
# Needs helm and yq. Run from anywhere: operator/chart/test-kubemootconfig-images.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
fail() { echo "FAIL: $*" >&2; exit 1; }

# Checked before any render, which would fail to parse on a new-syntax chart.
# Template syntax must parse on the oldest Helm 3 users and the release workflow run;
# "else with" needs a newer Go template engine and breaks them.
if grep -rn "else with" "$chart/templates"; then
  fail "a chart template uses 'else with'; use 'default' or nested 'if' so older Helm parses it"
fi

helm dependency build "$chart" >/dev/null

config_images() { helm template kubemoot-operator "$chart" --namespace kubemoot "$@" \
  | yq -N 'select(.kind == "KubemootConfig") | .spec.images'; }

out="$(config_images)"
[ -n "$out" ] || fail "no KubemootConfig rendered"

# Every Kubemoot-built image is pinned to an explicit tag (0.0.0 in git, stamped at
# release), never latest. workspaceAccess is how a crew's workspace MCPServer runs the
# workspace-access image without naming a version.
for key in indexer queryService queryServiceGo agentRuntime agentSidecar harnessGoose mcpGateway mcpBridge discussionGateway fitnessRunner workspaceAccess; do
  value="$(yq -N ".$key" <<<"$out")"
  [ -n "$value" ] && [ "$value" != null ] || fail "KubemootConfig spec.images.$key is not rendered"
  case "$value" in *:latest) fail "spec.images.$key floats: $value" ;; esac
done

[ "$(yq -N .workspaceAccess <<<"$out")" = "ghcr.io/kubemoot/workspace-access:0.0.0" ] \
  || fail "workspaceAccess should be the registry-prefixed workspace-access image"

[ "$(yq -N .queryServiceGo <<<"$out")" = "ghcr.io/kubemoot/query-service-go:0.0.0" ] \
  || fail "queryServiceGo should be the registry-prefixed query-service-go image"

# The old artifactAccess value is never rendered under its old name. kubemootConfig.images.workspaceAccess
# wins over it; the old value is the workspace image only when workspaceAccess is empty.
[ "$(yq -N '.artifactAccess // "absent"' <<<"$out")" = "absent" ] \
  || fail "spec.images.artifactAccess is rendered"
both="$(config_images --set kubemootConfig.images.artifactAccess=workspace-access:9.9.9)"
[ "$(yq -N .workspaceAccess <<<"$both")" = "ghcr.io/kubemoot/workspace-access:0.0.0" ] \
  || fail "workspaceAccess must win over the old artifactAccess value"
old_only="$(config_images --set kubemootConfig.images.workspaceAccess= --set kubemootConfig.images.artifactAccess=workspace-access:9.9.9)"
[ "$(yq -N .workspaceAccess <<<"$old_only")" = "ghcr.io/kubemoot/workspace-access:9.9.9" ] \
  || fail "the old artifactAccess value must be the workspace image when workspaceAccess is empty"
new_pin="$(config_images --set kubemootConfig.images.workspaceAccess=workspace-access:8.8.8 --set kubemootConfig.images.artifactAccess=workspace-access:9.9.9)"
[ "$(yq -N .workspaceAccess <<<"$new_pin")" = "ghcr.io/kubemoot/workspace-access:8.8.8" ] \
  || fail "a workspaceAccess pin must win over the old value"
neither="$(config_images --set kubemootConfig.images.workspaceAccess= --set kubemootConfig.images.artifactAccess=)"
[ "$(yq -N '.workspaceAccess // "absent"' <<<"$neither")" = "absent" ] \
  || fail "workspaceAccess is rendered when neither workspaceAccess nor artifactAccess is set"

for rendered in "$both" "$old_only" "$new_pin"; do
  [ "$(yq -N '.artifactAccess // "absent"' <<<"$rendered")" = "absent" ] || fail "spec.images.artifactAccess is rendered for an old value"
done

# A private mirror prefixes it like every other Kubemoot image.
mirrored="$(config_images --set global.imageRegistry=harbor.example/kubemoot | yq -N .workspaceAccess)"
[ "$mirrored" = "harbor.example/kubemoot/workspace-access:0.0.0" ] \
  || fail "workspaceAccess ignores global.imageRegistry: $mirrored"

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
  | grep -q "kv_bucket kubemoot_coordinator_leases --ttl=15s" || fail "coordinator.lease=true should create the lease bucket with a 15s TTL"
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

# The workspace query engine defaults to sqlite, takes duckdb, takes none (no query tool),
# and refuses any other value at render time.
query_engine() { helm template kubemoot-operator "$chart" --namespace kubemoot "$@" \
  | yq -N 'select(.kind == "KubemootConfig") | .spec.workspace.queryEngine'; }
[ "$(query_engine)" = "sqlite" ] || fail "workspace.queryEngine should default to sqlite"
for engine in sqlite duckdb none; do
  [ "$(query_engine --set workspace.queryEngine=$engine)" = "$engine" ] \
    || fail "workspace.queryEngine=$engine does not reach KubemootConfig"
done
# The old artifacts.queryEngine is read when workspace.queryEngine is not set, and the new value
# wins when both are set. The old block is no longer rendered into the KubemootConfig.
[ "$(query_engine --set artifacts.queryEngine=duckdb)" = "duckdb" ] \
  || fail "a values file that sets the old artifacts.queryEngine must still reach spec.workspace.queryEngine"
[ "$(query_engine --set artifacts.queryEngine=duckdb --set workspace.queryEngine=none)" = "none" ] \
  || fail "workspace.queryEngine must win over artifacts.queryEngine"
[ "$(helm template kubemoot-operator "$chart" --namespace kubemoot --set artifacts.queryEngine=duckdb \
  | yq -N 'select(.kind == "KubemootConfig") | .spec | has("artifacts")')" = "false" ] \
  || fail "KubemootConfig spec.artifacts is rendered"
# The CRD has no default for either engine field, so an unset engine is told from a chosen one.
crd_spec=".spec.versions[0].schema.openAPIV3Schema.properties.spec.properties"
crd_file_config="$chart/crds/kubemoot.ai_kubemootconfigs.yaml"
[ "$(yq -N "$crd_spec.workspace.properties.queryEngine | has(\"default\")" "$crd_file_config")" = "false" ] \
  || fail "CRD workspace.queryEngine has a default"
[ "$(yq -N "$crd_spec.artifacts.properties.queryEngine | has(\"default\")" "$crd_file_config")" = "false" ] \
  || fail "CRD artifacts.queryEngine has a default"
# The engines the chart accepts are the engines the KubemootConfig CRD accepts.
crd_engines="$(yq -N "$crd_spec.workspace.properties.queryEngine.enum | join(\",\")" "$crd_file_config")"
[ "$crd_engines" = "none,sqlite,duckdb" ] || fail "CRD queryEngine enum is $crd_engines, the chart accepts none,sqlite,duckdb"
if helm template kubemoot-operator "$chart" --namespace kubemoot --set workspace.queryEngine=oracle >/dev/null 2>&1; then
  fail "workspace.queryEngine=oracle should fail the render"
fi
if helm template kubemoot-operator "$chart" --namespace kubemoot --set artifacts.queryEngine=oracle >/dev/null 2>&1; then
  fail "artifacts.queryEngine=oracle should fail the render"
fi

# The RAG query service engine defaults to python, takes go, reaches the KubemootConfig, refuses
# any other value at render time, and the CRD accepts the same engines.
query_service_engine() { helm template kubemoot-operator "$chart" --namespace kubemoot "$@" \
  | yq -N 'select(.kind == "KubemootConfig") | .spec.queryService.engine'; }
[ "$(query_service_engine)" = "python" ] || fail "queryService.engine should default to python"
for engine in python go; do
  [ "$(query_service_engine --set queryService.engine=$engine)" = "$engine" ] \
    || fail "queryService.engine=$engine does not reach KubemootConfig"
done
if helm template kubemoot-operator "$chart" --namespace kubemoot --set queryService.engine=rust >/dev/null 2>&1; then
  fail "queryService.engine=rust should fail the render"
fi
qs_engines="$(yq -N "$crd_spec.queryService.properties.engine.enum | join(\",\")" "$crd_file_config")"
[ "$qs_engines" = "python,go" ] || fail "CRD queryService.engine enum is $qs_engines, the chart accepts python,go"

# The cluster-wide default workspace access is rendered only when the chart sets it, takes the four
# access values, and refuses any other.
default_access() { helm template kubemoot-operator "$chart" --namespace kubemoot "$@" \
  | yq -N 'select(.kind == "KubemootConfig") | .spec.workspace.defaultAccess // "unset"'; }
[ "$(default_access)" = "unset" ] || fail "workspace.defaultAccess is rendered without a value"
for access in none read write readWrite; do
  [ "$(default_access --set workspace.defaultAccess=$access)" = "$access" ] \
    || fail "workspace.defaultAccess=$access does not reach KubemootConfig"
done
if helm template kubemoot-operator "$chart" --namespace kubemoot --set workspace.defaultAccess=all >/dev/null 2>&1; then
  fail "workspace.defaultAccess=all should fail the render"
fi

# The agent runtime class reaches KubemootConfig only when set.
rc="$(helm template kubemoot-operator "$chart" --namespace kubemoot | yq -N 'select(.kind == "KubemootConfig") | .spec.defaults.agentRuntimeClassName')"
case "$rc" in ""|null) ;; *) fail "KubemootConfig sets agentRuntimeClassName by default: $rc" ;; esac
rc="$(helm template kubemoot-operator "$chart" --namespace kubemoot --set kubemootConfig.defaults.agentRuntimeClassName=gvisor | yq -N 'select(.kind == "KubemootConfig") | .spec.defaults.agentRuntimeClassName')"
[ "$rc" = gvisor ] || fail "kubemootConfig.defaults.agentRuntimeClassName=gvisor does not reach KubemootConfig: $rc"

# The install notes name an old value a values file still sets, and stay quiet without one.
notes() { helm install --dry-run --generate-name "$chart" "$@" 2>&1; }
notes --set artifacts.queryEngine=duckdb | grep -q "artifacts.queryEngine is workspace.queryEngine" \
  || fail "the install notes do not name the old artifacts.queryEngine"
notes --set kubemootConfig.images.artifactAccess=workspace-access:1 | grep -q "images.artifactAccess is kubemootConfig.images.workspaceAccess (it is ignored while workspaceAccess is set" \
  || fail "the install notes do not say the old images.artifactAccess is ignored while workspaceAccess is set"
if notes | grep -q "Old workspace names"; then fail "the install notes name old workspace names without any"; fi

echo "all KubemootConfig image render tests passed"
