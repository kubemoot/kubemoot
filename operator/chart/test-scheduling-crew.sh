#!/usr/bin/env bash
# Render tests for the scheduling crew the operator chart ships (schedulingCrew values).
# Needs helm and yq. Run from anywhere: operator/chart/test-scheduling-crew.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
release_ns=kubemoot
render() { helm template kubemoot-operator "$chart" --namespace "$release_ns" "$@"; }
# count <rendered> <yq filter>: how many documents match; a yq error is fatal.
count() {
  local kinds
  kinds="$(yq -N "select($2) | .kind" <<<"$1")" || fail "yq filter failed: $2"
  [ -z "$kinds" ] && { echo 0; return; }
  wc -l <<<"$kinds" | tr -d ' '
}
# get <rendered> <kind> <name> <yq path>
get() { yq -N "select(.kind == \"$2\" and .metadata.name == \"$3\") | $4" <<<"$1"; }

servers=(--set 'modelServers[0].name=gpu' --set 'modelServers[0].type=ollama'
  --set 'modelServers[0].endpoint=http://ollama.gpu:11434'
  --set 'modelServers[1].name=cpu' --set 'modelServers[1].type=ollama'
  --set 'modelServers[1].endpoint=http://ollama.cpu:11434')
out="$(render "${servers[@]}")"
ns=kubemoot-scheduling

# On by default, in its own namespace, which the chart creates.
[ "$(get "$out" Namespace "$ns" .metadata.name)" = "$ns" ] || fail "the crew namespace is not rendered"
[ "$(get "$out" Crew kubemoot-scheduling .metadata.namespace)" = "$ns" ] || fail "the Crew is not in $ns"
[ "$(count "$out" '.metadata.namespace == "kubemoot-scheduling" and .kind == "Agent"')" = 2 ] \
  || fail "the crew should have two agents"
[ "$(get "$out" Agent scheduling-coordinator .spec.discussRole)" = coordinator ] || fail "no coordinator"

# The calibrator: a tooler limited to the accelerator-mcp tools it needs for judgment.
[ "$(get "$out" Agent model-server-calibrator .spec.discussRole)" = tooler ] || fail "the calibrator is not a tooler"
[ "$(get "$out" Agent model-server-calibrator '.spec.enabledTools | join(",")')" = \
  "list_servers,server_state,observed_performance,calibrate_server" ] || fail "the calibrator's tools are wrong"

# Every prompt module an agent refers to exists in the crew namespace.
for ref in $(yq -N 'select(.kind == "Agent" and .metadata.namespace == "kubemoot-scheduling") | .spec.promptRefs[]' <<<"$out"); do
  [ "$(get "$out" PromptModule "$ref" .metadata.namespace)" = "$ns" ] || fail "promptRef $ref has no PromptModule"
done
[ "$(get "$out" Skill judge-a-drift .metadata.namespace)" = "$ns" ] || fail "the drift-judgment skill is missing"

# The crew ships no Models: it selects from the cluster's catalog, requiring tool-calling
# and, in triage, preferring the measured-faster model (no size preference).
[ "$(count "$out" '.kind == "Model" and .metadata.namespace == "kubemoot-scheduling"')" = 0 ] \
  || fail "the crew ships Models of its own"
[ "$(count "$out" '.kind == "Model"')" = 0 ] || fail "the chart renders Models without a models entry"
policy=CrewSchedulingPolicy
[ "$(get "$out" $policy kubemoot-scheduling-scheduling '.spec.rules[] | select(.phase == "mulling") | .require.matchLabels["capability/tool-calling"]')" = "true" ] \
  || fail "mulling does not require tool-calling"
[ "$(get "$out" $policy kubemoot-scheduling-scheduling '.spec.rules[] | select(.phase == "triage") | .require.matchLabels["capability/tool-calling"]')" = "true" ] \
  || fail "triage does not require tool-calling"
[ "$(get "$out" $policy kubemoot-scheduling-scheduling '.spec.rules[] | select(.phase == "triage") | .prefer // "none"')" = none ] \
  || fail "triage states a size or other preference besides preferFaster"
[ "$(get "$out" $policy kubemoot-scheduling-scheduling '.spec.rules[] | select(.phase == "triage") | .require.matchLabels | keys | join(",")')" = "capability/tool-calling" ] \
  || fail "triage requires more than tool-calling"
[ "$(get "$out" $policy kubemoot-scheduling-scheduling '.spec.rules[] | select(.phase == "triage") | .preferFaster')" = 50 ] \
  || fail "triage does not prefer the faster model"
[ -n "$(get "$out" $policy kubemoot-scheduling-scheduling '.spec.qualityBias.default // ""')" ] \
  || fail "mulling has no qualityBias to follow size"
if get "$out" $policy kubemoot-scheduling-scheduling . | grep -q latencyClass; then
  fail "the policy selects on the retired latencyClass label"
fi

# The tools: accelerator-mcp is a core MCP server in the operator's (release)
# namespace, offered to crews, started by the buildpacks launcher, as a ServiceAccount
# allowed to write ModelServer status and nothing more. The crew's gateway selects it.
[ "$(get "$out" MCPServer accelerator-mcp .metadata.namespace)" = "$release_ns" ] \
  || fail "accelerator-mcp is not in the operator's namespace"
[ "$(get "$out" MCPServer accelerator-mcp '.metadata.labels["kubemoot.ai/core-mcp"]')" = "true" ] \
  || fail "accelerator-mcp is not offered as a core MCP server"
[ "$(count "$out" '.kind == "MCPServer" and .metadata.namespace == "kubemoot-scheduling"')" = 0 ] \
  || fail "the crew runs its own MCP server copy"
sel="$(get "$out" MCPGateway kubemoot-scheduling-gateway '.spec.coreServerSelector.matchLabels["kubemoot.ai/tools"]')"
[ "$(get "$out" MCPServer accelerator-mcp ".metadata.labels[\"kubemoot.ai/tools\"]")" = "$sel" ] \
  || fail "the crew's gateway does not select accelerator-mcp"
image="$(get "$out" MCPServer accelerator-mcp .spec.image)"
[[ "$image" == */accelerator-mcp:* ]] || fail "unexpected accelerator-mcp image: $image"
[ "$(get "$out" MCPServer accelerator-mcp '.spec.command[0]')" = /cnb/process/web ] || fail "not started by the launcher"
[ "$(get "$out" MCPServer accelerator-mcp .spec.serviceAccountName)" = accelerator-mcp ] || fail "no ServiceAccount"
writes="$(yq -N 'select(.kind == "ClusterRole" and (.metadata.name | test("accelerator-mcp"))) | .rules[] | select(.verbs[] == "patch") | .resources[]' <<<"$out" | sort -u)"
[ "$writes" = "modelservers/status" ] || fail "the tools may update more than ModelServer status: $writes"

# No rule names hardware: prompts and the skill read what exists through the tools.
if yq -N 'select(.metadata.namespace == "kubemoot-scheduling" and (.kind == "PromptModule" or .kind == "Skill")) | .spec.content' <<<"$out" \
  | grep -inwE 'rtx|nvidia|amd|gpu|4090|5090|rig[0-9]+|qwen[0-9.]*|llama[0-9.]*|ollama(-[a-z0-9]+)?'; then
  fail "a scheduling-crew prompt names hardware or a model"
fi

# The RBAC grants exactly what the tools call: get and list, and patch on status.
role='select(.kind == "ClusterRole" and (.metadata.name | test("accelerator-mcp")))'
[ "$(yq -N "$role | .rules[] | select(.resources[] == \"models\") | .verbs | join(\",\")" <<<"$out")" = "get,list" ] \
  || fail "the tools should only get and list ModelServers and Models"
[ "$(yq -N "$role | .rules[] | select(.resources[] == \"modelservers/status\") | .verbs | join(\",\")" <<<"$out")" = "patch" ] \
  || fail "the tools should only patch ModelServer status"
[ "$(get "$out" MCPServer accelerator-mcp .spec.securityMode)" = strict ] || fail "the MCP server should run in strict security mode"

# The operator is told where the crew is, so it can ask it to judge calibration drift.
crew_env() { yq -N 'select(.kind == "Deployment" and (.metadata.name | test("kubemoot-operator$"))) | .spec.template.spec.containers[0].env[] | select(.name == "KUBEMOOT_SCHEDULING_CREW") | .value' <<<"$1"; }
[ "$(crew_env "$out")" = "kubemoot-scheduling/kubemoot-scheduling" ] || fail "the operator is not told where the scheduling crew is"

# Without chart-declared ModelServers the crew would have no Model to think with: nothing renders.
none="$(render)"
[ "$(count "$none" '.metadata.namespace == "kubemoot-scheduling" or .metadata.name == "kubemoot-scheduling"')" = 0 ] \
  || fail "the crew renders without ModelServers"

# Off: nothing of the crew renders.
off="$(render "${servers[@]}" --set schedulingCrew.enabled=false)"
[ "$(count "$off" '.metadata.namespace == "kubemoot-scheduling" or .metadata.name == "kubemoot-scheduling"')" = 0 ] \
  || fail "the crew renders when disabled"
[ -z "$(crew_env "$off")" ] || fail "the operator is told about a crew that is disabled"
[ "$(get "$off" MCPServer accelerator-mcp .metadata.name)" = accelerator-mcp ] \
  || fail "accelerator-mcp is a core server and should not depend on the crew"
[ "$(get "$none" MCPServer accelerator-mcp .metadata.name)" = accelerator-mcp ] \
  || fail "accelerator-mcp should run with no chart-declared servers"

# accelerator-mcp off, or no NATS: none of it renders.
for value in acceleratorMcp.enabled=false nats.enabled=false; do
  gone="$(render "${servers[@]}" --set "$value")"
  [ "$(count "$gone" '(.metadata.name | test("accelerator-mcp"))')" = 0 ] || fail "accelerator-mcp renders with $value"
done

# Another namespace is honored.
other="$(render "${servers[@]}" --set schedulingCrew.namespace=km-sched)"
[ "$(get "$other" Crew kubemoot-scheduling .metadata.namespace)" = km-sched ] || fail "namespace value ignored"
[ "$(crew_env "$other")" = "km-sched/kubemoot-scheduling" ] || fail "the operator is told the wrong crew namespace"

# allowedIngress: unset renders no network key; set renders the selector on the Crew.
[ "$(get "$out" Crew kubemoot-scheduling '.spec | has("network")')" = false ] || fail "the Crew has a network key without allowedIngress"
admitted="$(render "${servers[@]}" --set 'schedulingCrew.allowedIngress[0].matchLabels.kubernetes\.io/metadata\.name=homelab-pilot')"
[ "$(get "$admitted" Crew kubemoot-scheduling '.spec.network.allowedIngress[0].matchLabels["kubernetes.io/metadata.name"]')" = homelab-pilot ] \
  || fail "allowedIngress is not rendered on the Crew"
[ "$(get "$admitted" Crew kubemoot-scheduling '.spec.network.allowedIngress | length')" = 1 ] || fail "allowedIngress renders the wrong number of selectors"

# A vLLM server alone is enough: the crew renders with no Model on it.
vllm="$(render --set 'modelServers[0].name=vllm-gpu' --set 'modelServers[0].type=vllm' \
  --set 'modelServers[0].serviceRef.name=kubemoot-vllm' --set 'modelServers[0].serviceRef.namespace=vllm')"
[ "$(get "$vllm" Crew kubemoot-scheduling .metadata.namespace)" = "$ns" ] || fail "the crew does not render on a vLLM server"
[ "$(count "$vllm" '.kind == "Model"')" = 0 ] || fail "a Model renders for the vLLM server"

echo "PASS: scheduling crew render tests"
