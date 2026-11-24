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

# GOMEMLIMIT is 90 percent of the memory limit (384Mi -> 345MiB, 128Mi -> 115MiB).
[ "$(gomemlimit "$op")" = 345MiB ] || fail "operator GOMEMLIMIT is $(gomemlimit "$op")"
[ "$(gomemlimit "$lia")" = 115MiB ] || fail "liaison GOMEMLIMIT is $(gomemlimit "$lia")"
[ "$(gomemlimit "$acc")" = 115MiB ] || fail "accelerator-mcp GOMEMLIMIT is $(gomemlimit "$acc")"
[ "$(grep -c '^ *- name: GOMEMLIMIT$' <<<"$out")" = 3 ] || fail "GOMEMLIMIT is not set exactly three times"

# A Gi limit converts; the percent follows the value.
out="$(render --set resources.limits.memory=1Gi --set goMemLimitPercent=80)"
[ "$(gomemlimit "$(op_container "$out")")" = 819MiB ] || fail "1Gi at 80 percent is not 819MiB"

# Percent 0 sets nothing anywhere.
out="$(render --set goMemLimitPercent=0)"
grep -q 'GOMEMLIMIT' <<<"$out" && fail "GOMEMLIMIT rendered with goMemLimitPercent=0"

# A limit written in another form (decimal M, bytes) is left without GOMEMLIMIT, not given a wrong one.
out="$(render --set resources.limits.memory=400M)"
[ "$(gomemlimit "$(op_container "$out")")" = "" ] || fail "operator got GOMEMLIMIT from an unconvertible limit"
out="$(render --set-json 'resources.limits=null')"
[ "$(gomemlimit "$(op_container "$out")")" = "" ] || fail "operator got GOMEMLIMIT with no limit at all"

echo "go resources render tests passed"
