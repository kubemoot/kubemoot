#!/usr/bin/env bash
# Render tests for the sizing of the chart's Go components (operator, crew liaison,
# accelerator-mcp): the defaults are Burstable with a memory limit and no limit on the
# operator's and accelerator-mcp's CPU, and GOMEMLIMIT follows the memory limit. Needs helm and
# yq. Run from anywhere: operator/chart/test-go-resources.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
render() { helm template kubemoot-operator "$chart" --namespace km "$@"; }
op_container() { yq -N 'select(.kind == "Deployment" and .metadata.name == "kubemoot-operator") | .spec.template.spec.containers[0]' <<<"$1"; }
liaison_container() { yq -N 'select(.kind == "Deployment" and .metadata.name == "kubemoot-operator-liaison") | .spec.template.spec.containers[0]' <<<"$1"; }
acc_server() { yq -N 'select(.kind == "MCPServer" and .metadata.name == "accelerator-mcp")' <<<"$1"; }
gomemlimit() { yq '.env[] | select(.name == "GOMEMLIMIT") | .value' <<<"$1"; }
# qos <container yaml>: the QoS class Kubernetes gives a pod of this one container.
qos() {
  local c="$1" r l
  r="$(yq '.resources.requests | length' <<<"$c")"; l="$(yq '.resources.limits | length' <<<"$c")"
  if [ "$r" = 0 ] && [ "$l" = 0 ]; then echo BestEffort; return; fi
  if [ "$(yq '.resources.requests.cpu == .resources.limits.cpu and .resources.requests.memory == .resources.limits.memory' <<<"$c")" = true ]; then echo Guaranteed; return; fi
  echo Burstable
}

out="$(render)"
op="$(op_container "$out")"; lia="$(liaison_container "$out")"
# accelerator-mcp is an MCPServer: its resources sit in spec.resources, its env in spec.env.
acc="$(acc_server "$out" | yq '{"env": .spec.env, "resources": .spec.resources}')"
[ -n "$op" ] && [ -n "$lia" ] && [ -n "$acc" ] || fail "a Go component is missing from the default render"

# Every Go component has a memory limit above its request, so it is Burstable and OOM-kills
# before it can push the node into eviction.
for pair in "operator:$op" "liaison:$lia" "accelerator-mcp:$acc"; do
  name="${pair%%:*}"; c="${pair#*:}"
  [ "$(qos "$c")" = Burstable ] || fail "$name is not Burstable by default"
  [ "$(yq '.resources.limits.memory' <<<"$c")" != null ] || fail "$name has no memory limit"
done

# CPU limits: none on the operator and accelerator-mcp, kept on the liaison.
[ "$(yq '.resources.limits.cpu' <<<"$op")" = null ] || fail "operator has a CPU limit"
[ "$(yq '.resources.limits.cpu' <<<"$acc")" = null ] || fail "accelerator-mcp has a CPU limit"
[ "$(yq '.resources.limits.cpu' <<<"$lia")" = 500m ] || fail "liaison lost its CPU limit"

# The liaison pod runs as a high, non-root UID and GID under the runtime default seccomp profile.
lia_pod="$(yq -N 'select(.kind == "Deployment" and .metadata.name == "kubemoot-operator-liaison") | .spec.template.spec.securityContext' <<<"$out")"
[ "$(yq '.runAsUser' <<<"$lia_pod")" = 65532 ] || fail "liaison does not run as UID 65532"
[ "$(yq '.runAsGroup' <<<"$lia_pod")" = 65532 ] || fail "liaison does not run as GID 65532"
[ "$(yq '.seccompProfile.type' <<<"$lia_pod")" = RuntimeDefault ] || fail "liaison has no RuntimeDefault seccomp profile"

# The placement service pod (rendered only when enabled) runs the same way.
ps_pod="$(render --set placementService.enabled=true | yq -N 'select(.kind == "Deployment" and .metadata.name == "kubemoot-operator-placement-service") | .spec.template.spec.securityContext')"
[ "$(yq '.runAsUser' <<<"$ps_pod")" = 65532 ] || fail "placement service does not run as UID 65532"
[ "$(yq '.runAsGroup' <<<"$ps_pod")" = 65532 ] || fail "placement service does not run as GID 65532"
[ "$(yq '.seccompProfile.type' <<<"$ps_pod")" = RuntimeDefault ] || fail "placement service has no RuntimeDefault seccomp profile"

# GOMEMLIMIT is 90 percent of the memory limit (384Mi -> 345MiB, 128Mi -> 115MiB).
[ "$(gomemlimit "$op")" = 345MiB ] || fail "operator GOMEMLIMIT is $(gomemlimit "$op")"
[ "$(gomemlimit "$lia")" = 115MiB ] || fail "liaison GOMEMLIMIT is $(gomemlimit "$lia")"
[ "$(gomemlimit "$acc")" = 115MiB ] || fail "accelerator-mcp GOMEMLIMIT is $(gomemlimit "$acc")"
[ "$(grep -c '^ *- name: GOMEMLIMIT$' <<<"$out")" = 3 ] || fail "GOMEMLIMIT is not set exactly three times"

grep -q 'cpu: null' <<<"$out" && fail "the render emits a null CPU limit"

# component_limit <component> <rendered>: that component's GOMEMLIMIT, empty when it has none.
component_limit() {
  case "$1" in
    operator) gomemlimit "$(op_container "$2")" ;;
    liaison) gomemlimit "$(liaison_container "$2")" ;;
    accelerator-mcp) gomemlimit "$(acc_server "$2" | yq '{"env": .spec.env}')" ;;
  esac
}
# Each component's own memory limit path in values.
path_of() { case "$1" in operator) echo resources ;; liaison) echo liaison.resources ;; accelerator-mcp) echo acceleratorMcp.resources ;; esac; }

# Table: limit written | percent | expected GOMEMLIMIT ("-" for none). Run for every component
# with that component's own path, so one component's override never changes another's.
cases=(
  "512Mi|90|460MiB"     # 460.8 truncates down
  "1Gi|90|921MiB"
  "1Gi|80|819MiB"
  "128Mi|100|128MiB"
  "128Mi|0|-"
  "1500M|90|-"          # decimal M is not Mi
  "1.5Gi|90|-"          # a fraction is not a whole number of Gi
  "134217728|90|-"      # bytes
)
for component in operator liaison accelerator-mcp; do
  p="$(path_of "$component")"
  for row in "${cases[@]}"; do
    IFS='|' read -r limit pct want <<<"${row%%#*}"; want="${want%% *}"
    out="$(render --set "$p.limits.memory=$limit" --set "goMemLimitPercent=$pct")"
    got="$(component_limit "$component" "$out")"; [ -n "$got" ] || got="-"
    [ "$got" = "$want" ] || fail "$component limit $limit at $pct percent gave $got, want $want"
    # The other two components keep their defaults (345MiB operator, 115MiB the others) at 90.
  done
done

# One component's override leaves the others on their defaults.
out="$(render --set liaison.resources.limits.memory=512Mi)"
[ "$(component_limit liaison "$out")" = 460MiB ] || fail "liaison override not applied"
[ "$(component_limit operator "$out")" = 345MiB ] || fail "liaison override changed the operator"
[ "$(component_limit accelerator-mcp "$out")" = 115MiB ] || fail "liaison override changed accelerator-mcp"
out="$(render --set acceleratorMcp.resources.limits.memory=512Mi)"
[ "$(component_limit accelerator-mcp "$out")" = 460MiB ] || fail "accelerator-mcp override not applied"
[ "$(component_limit liaison "$out")" = 115MiB ] || fail "accelerator-mcp override changed the liaison"

# No limit at all sets nothing.
out="$(render --set-json 'resources.limits=null')"
[ "$(component_limit operator "$out")" = "" ] || fail "operator got GOMEMLIMIT with no limit at all"
out="$(render --set-json 'acceleratorMcp.resources.limits=null')"
[ "$(component_limit accelerator-mcp "$out")" = "" ] || fail "accelerator-mcp got GOMEMLIMIT with no limit at all"

# A CPU limit the user sets is rendered as written.
out="$(render --set resources.limits.cpu=2)"
[ "$(op_container "$out" | yq '.resources.limits.cpu')" = 2 ] || fail "a user CPU limit was lost"

# Percent 0 sets nothing anywhere.
out="$(render --set goMemLimitPercent=0)"
grep -q 'GOMEMLIMIT' <<<"$out" && fail "GOMEMLIMIT rendered with goMemLimitPercent=0"

# Out of range, negative, fractional and non-numeric percents fail the render.
for bad in 101 -1 abc 90.5 ""; do
  if render --set "goMemLimitPercent=$bad" >/dev/null 2>&1; then fail "goMemLimitPercent=\"$bad\" rendered"; fi
done
out="$(render --set-string goMemLimitPercent=75 2>&1)" || fail "a numeric string percent was refused"
[ "$(component_limit operator "$out")" = 288MiB ] || fail "string percent 75 of 384Mi is not 288MiB"

echo "go resources render tests passed"
