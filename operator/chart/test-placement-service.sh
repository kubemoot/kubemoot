#!/usr/bin/env bash
# Render tests for the placement service: nothing by default; a ServiceAccount, a
# two-replica Deployment, a Service and a PodDisruptionBudget when enabled; no API
# token and no RBAC; agents are pointed at it only when placementService.agentsUse.
# Needs helm and yq. Run from anywhere: operator/chart/test-placement-service.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
render() { helm template kubemoot-operator "$chart" --namespace kubemoot "$@"; }
name=kubemoot-operator-placement-service
pick() { yq -N "select(.kind == \"$1\" and .metadata.name == \"$name\") | $2" <<<"$3"; }

# Off by default: nothing is rendered, and KubemootConfig has no placementService.
out="$(render)"
[ -z "$(yq -N "select(.metadata.name == \"$name\") | .kind" <<<"$out")" ] || fail "rendered by default"
cfg="$(yq -N 'select(.kind == "KubemootConfig") | .spec.defaults.scheduler.placementService' <<<"$out")"
case "$cfg" in ""|null) ;; *) fail "KubemootConfig carries placementService by default: $cfg" ;; esac

# Enabled.
out="$(render --set placementService.enabled=true)"
[ "$(pick ServiceAccount .automountServiceAccountToken "$out")" = false ] || fail "ServiceAccount automounts a token"
[ "$(pick Deployment .spec.replicas "$out")" = 2 ] || fail "replicas is not 2"
[ "$(pick Deployment .spec.template.spec.automountServiceAccountToken "$out")" = false ] || fail "pod automounts the API token"
[ "$(pick Deployment .spec.template.spec.serviceAccountName "$out")" = "$name" ] || fail "wrong serviceAccountName"
[ "$(pick Deployment .spec.strategy.rollingUpdate.maxUnavailable "$out")" = 1 ] || fail "rollout maxUnavailable is not 1"
c='.spec.template.spec.containers[0]'
[ "$(pick Deployment "$c.image" "$out")" = "ghcr.io/kubemoot/placement-service:0.0.0" ] || fail "image ignores the global registry or tag"
[ "$(pick Deployment "$c.securityContext.readOnlyRootFilesystem" "$out")" = true ] || fail "root filesystem is writable"
[ "$(pick Deployment "$c.securityContext.capabilities.drop[0]" "$out")" = ALL ] || fail "capabilities not dropped"
[ "$(pick Deployment ".spec.template.spec.securityContext.seccompProfile.type" "$out")" = RuntimeDefault ] || fail "no seccomp profile"
[ "$(pick Deployment "$c.readinessProbe.httpGet.path" "$out")" = /q/health/ready ] || fail "readiness path"
[ "$(pick Deployment "$c.livenessProbe.httpGet.path" "$out")" = /q/health/live ] || fail "liveness path"
[ "$(pick Deployment "$c.env[] | select(.name == \"NATS_URL\") | .value" "$out")" = "nats://nats.nats.svc.cluster.local:4222" ] || fail "NATS_URL"
[ "$(pick Deployment "$c.env[] | select(.name == \"KUBEMOOT_PLACEMENT_LEASE_SECONDS\") | .value" "$out")" = 15 ] || fail "lease seconds"
[ "$(pick Deployment "$c.env[] | select(.name == \"KUBEMOOT_PLACEMENT_TICKET_TTL_SECONDS\") | .value" "$out")" = 600 ] || fail "ticket ttl"
[ "$(pick Deployment "$c.env[] | select(.name == \"HOSTNAME\") | .valueFrom.fieldRef.fieldPath" "$out")" = metadata.name ] || fail "HOSTNAME is not the pod name"
[ "$(pick Deployment "$c.env[] | select(.name == \"NATS_USER\") | .name" "$out")" = "" ] || fail "NATS_USER set without a secret"
[ "$(pick Service .spec.type "$out")" = ClusterIP ] || fail "Service is not ClusterIP"
[ "$(pick Service .spec.ports[0].port "$out")" = 8080 ] || fail "Service port"
[ "$(pick Service .spec.ports[0].name "$out")" = http ] || fail "Service port name"
[ "$(pick PodDisruptionBudget .spec.maxUnavailable "$out")" = 1 ] || fail "PDB maxUnavailable is not 1"
[ "$(pick PodDisruptionBudget .spec.minAvailable "$out")" = null ] || fail "PDB sets minAvailable"
# The service needs no Kubernetes API access: no Role or binding names it.
[ -z "$(yq -N "select((.kind == \"Role\" or .kind == \"ClusterRole\" or .kind == \"RoleBinding\" or .kind == \"ClusterRoleBinding\") and .metadata.name == \"$name\") | .kind" <<<"$out")" ] \
  || fail "RBAC rendered for the placement service"
cfg="$(yq -N 'select(.kind == "KubemootConfig") | .spec.defaults.scheduler.placementService' <<<"$out")"
case "$cfg" in ""|null) ;; *) fail "agents pointed at the service without agentsUse: $cfg" ;; esac

# NATS credentials and a custom replica count.
out="$(render --set placementService.enabled=true --set placementService.replicaCount=3 --set placementService.nats.existingSecret=nats-creds)"
[ "$(pick Deployment .spec.replicas "$out")" = 3 ] || fail "replicaCount ignored"
[ "$(pick Deployment "$c.env[] | select(.name == \"NATS_USER\") | .valueFrom.secretKeyRef.name" "$out")" = nats-creds ] || fail "NATS_USER secret"
[ "$(pick Deployment "$c.env[] | select(.name == \"NATS_PASSWORD\") | .valueFrom.secretKeyRef.key" "$out")" = password ] || fail "NATS_PASSWORD key"

# agentsUse turns agents onto the service; the URL defaults to the in-cluster Service.
out="$(render --set placementService.enabled=true --set placementService.agentsUse=true)"
[ "$(yq -N 'select(.kind == "KubemootConfig") | .spec.defaults.scheduler.placementService.enabled' <<<"$out")" = true ] || fail "agentsUse did not enable it"
[ "$(yq -N 'select(.kind == "KubemootConfig") | .spec.defaults.scheduler.placementService.url' <<<"$out")" = "http://$name.kubemoot.svc:8080" ] || fail "default URL"
# agentsUse alone, without the service, points agents nowhere.
out="$(render --set placementService.agentsUse=true)"
cfg="$(yq -N 'select(.kind == "KubemootConfig") | .spec.defaults.scheduler.placementService' <<<"$out")"
case "$cfg" in ""|null) ;; *) fail "agentsUse without enabled still points agents at the service: $cfg" ;; esac
out="$(render --set placementService.enabled=true --set placementService.agentsUse=true --set placementService.url=http://example:9)"
[ "$(yq -N 'select(.kind == "KubemootConfig") | .spec.defaults.scheduler.placementService.url' <<<"$out")" = "http://example:9" ] || fail "url override"

# The leader bucket's TTL follows placementService.leaseSeconds.
out="$(helm template t "$chart" --show-only templates/nats-streams-job.yaml --set placementService.leaseSeconds=30)"
grep -q 'kv add kubemoot_placement_leader --ttl=30s ' <<<"$out" || fail "leader bucket TTL ignores leaseSeconds"

# A private mirror prefixes the image like every other Kubemoot image.
out="$(render --set placementService.enabled=true --set global.imageRegistry=harbor.example/kubemoot)"
[ "$(pick Deployment "$c.image" "$out")" = "harbor.example/kubemoot/placement-service:0.0.0" ] || fail "image ignores global.imageRegistry"

# The scheduler settings of the existing KubemootConfig are kept.
out="$(render --set kubemootConfig.defaults.scheduler.enabled=true)"
[ "$(yq -N 'select(.kind == "KubemootConfig") | .spec.defaults.scheduler.enabled' <<<"$out")" = true ] || fail "scheduler settings lost"

echo "all placement service render tests passed"
