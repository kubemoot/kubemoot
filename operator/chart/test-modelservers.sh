#!/usr/bin/env bash
# Render tests for the ModelServers the operator chart declares from its modelServers
# values. Needs helm and yq. Run from anywhere: operator/chart/test-modelservers.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
servers() { helm template kubemoot-operator "$chart" "$@" | yq 'select(.kind == "ModelServer")'; }
# field <rendered> <server name> <yq path>
field() { yq -N "select(.metadata.name == \"$2\") | $3" <<<"$1"; }

# Default: no ModelServers.
out="$(servers)"
[ -z "$out" ] || fail "default render contains ModelServers"

out="$(servers \
  --set 'modelServers[0].name=gpu' --set 'modelServers[0].type=ollama' \
  --set 'modelServers[0].endpoint=http://ollama.gpu:11434' --set 'modelServers[0].schedulingWeight=25' \
  --set 'modelServers[1].name=cpu' --set 'modelServers[1].type=ollama' \
  --set 'modelServers[1].endpoint=http://ollama.cpu:11434' --set 'modelServers[1].memoryMiB=20480' \
  --set 'modelServers[2].name=both' --set 'modelServers[2].type=ollama' \
  --set 'modelServers[2].endpoint=http://ollama.both:11434' --set 'modelServers[2].schedulingWeight=50' \
  --set 'modelServers[2].memoryMiB=6144' \
  --set 'modelServers[3].name=bare' --set 'modelServers[3].type=ollama' \
  --set 'modelServers[3].endpoint=http://ollama.bare:11434' --namespace km)"

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


# vllm servers: serviceRef, servers, and sleepLevel render at the top of the spec,
# and no endpoint renders.
values="$(mktemp)"
trap 'rm -f "$values"' EXIT
cat >"$values" <<'EOF'
modelServers:
  - name: vllm-gpu
    type: vllm
    schedulingWeight: 80
    serviceRef: {name: vllm-gpu, namespace: vllm}
  - name: vllm-external
    type: vllm
    sleepLevel: 2
    servers:
      - {model: "qwen3:14b", endpoint: "http://vllm-14b.models:8000"}
      - {model: "qwen3:8b", endpoint: "http://vllm-8b.models:8000"}
  - name: ollama-gpu
    type: ollama
    endpoint: http://ollama.gpu:11434
EOF
out="$(servers -f "$values" --namespace km)"

[ "$(field "$out" vllm-gpu .spec.type)" = "vllm" ] || fail "vllm type not rendered"
[ "$(field "$out" vllm-gpu .spec.endpoint)" = "null" ] || fail "a vllm server rendered an endpoint"
[ "$(field "$out" vllm-gpu .spec.scheduling.weight)" = "80" ] || fail "schedulingWeight lost on a vllm server"
[ "$(field "$out" vllm-gpu .spec.serviceRef.name)" = "vllm-gpu" ] || fail "serviceRef.name not rendered"
[ "$(field "$out" vllm-gpu .spec.serviceRef.namespace)" = "vllm" ] || fail "serviceRef.namespace not rendered"
[ "$(field "$out" vllm-gpu .spec.servers)" = "null" ] || fail "a serviceRef server rendered servers"
[ "$(field "$out" vllm-gpu .spec.sleepLevel)" = "null" ] || fail "sleepLevel rendered without being set"
[ "$(field "$out" vllm-gpu .spec.vllm)" = "null" ] || fail "a vllm block rendered; the fields are flat"

[ "$(field "$out" vllm-external .spec.serviceRef)" = "null" ] || fail "a servers server rendered a serviceRef"
[ "$(field "$out" vllm-external '.spec.servers | length')" = "2" ] || fail "servers not rendered"
[ "$(field "$out" vllm-external .spec.servers[1].endpoint)" = "http://vllm-8b.models:8000" ] || fail "server endpoint not rendered"
[ "$(field "$out" vllm-external '.spec.sleepLevel | tag')" = "!!int" ] || fail "sleepLevel is not an integer"
[ "$(field "$out" vllm-external .spec.sleepLevel)" = "2" ] || fail "sleepLevel not rendered"

[ "$(field "$out" ollama-gpu .spec.serviceRef)" = "null" ] || fail "an ollama server rendered a serviceRef"
[ "$(field "$out" ollama-gpu .spec.endpoint)" = "http://ollama.gpu:11434" ] || fail "an ollama server lost its endpoint"

# The scheduling crew declares its Ollama-tag model on the ollama server only.
models="$(helm template kubemoot-operator "$chart" -f "$values" --namespace km | yq -N 'select(.kind == "Model") | .spec.serverRef')"
[ "$models" = "ollama-gpu" ] || fail "the scheduling crew's Model should name only the ollama server, got: $models"

# Catalog models: a models entry renders one Model in the server's namespace, named
# for the server and the model, with only what the entry sets.
cat >"$values" <<'EOF'
modelServers:
  - name: ollama-gpu
    type: ollama
    endpoint: http://ollama.gpu:11434
    models:
      - model: qwen3:32b
      - model: qwen3:14b
        parameters: 14B
        capabilities: [tool-calling, reasoning]
        contextLength: 40960
        vramMib: 11000
        labels: {kubemoot.ai/data-residency: on-premises}
      - model: Gemma3:4B
        disabled: true
  - name: vllm-gpu
    type: vllm
    namespace: other
    serviceRef: {name: vllm-gpu, namespace: vllm}
    models:
      - model: qwen3:14b
        labels: {kubemoot.ai/license: apache-2.0}
  - name: no-models
    type: ollama
    endpoint: http://ollama.none:11434
EOF
catalog="$(helm template kubemoot-operator "$chart" -f "$values" --namespace km --set schedulingCrew.enabled=false | yq 'select(.kind == "Model")')"
model() { yq -N "select(.metadata.name == \"$1\") | $2" <<<"$catalog"; }
[ "$(yq -N '.metadata.name' <<<"$catalog" | sort | tr '\n' ' ')" = "ollama-gpu-gemma3-4b ollama-gpu-qwen3-14b ollama-gpu-qwen3-32b vllm-gpu-qwen3-14b " ] \
  || fail "unexpected catalog Models: $(yq -N '.metadata.name' <<<"$catalog" | tr '\n' ' ')"
[ "$(model ollama-gpu-qwen3-32b .spec.serverRef)" = "ollama-gpu" ] || fail "serverRef"
[ "$(model ollama-gpu-qwen3-32b .metadata.namespace)" = "km" ] || fail "a catalog Model lives in the server's namespace"
[ "$(model ollama-gpu-qwen3-32b .spec.model)" = "qwen3:32b" ] || fail "model name"
[ "$(model ollama-gpu-qwen3-32b .spec.parameters)" = "null" ] || fail "parameters rendered without being set"
[ "$(model ollama-gpu-qwen3-32b .spec.disabled)" = "null" ] || fail "disabled rendered without being set"
[ "$(model ollama-gpu-qwen3-14b .spec.parameters)" = "14B" ] || fail "parameters"
[ "$(model ollama-gpu-qwen3-14b '.spec.capabilities | join(",")')" = "tool-calling,reasoning" ] || fail "capabilities"
[ "$(model ollama-gpu-qwen3-14b '.spec.contextLength | tag')" = "!!int" ] || fail "contextLength is not an integer"
[ "$(model ollama-gpu-qwen3-14b '.spec.vramMib')" = "11000" ] || fail "vramMib"
[ "$(model ollama-gpu-qwen3-14b '.metadata.labels["kubemoot.ai/data-residency"]')" = "on-premises" ] || fail "owner label lost"
[ "$(model ollama-gpu-gemma3-4b .spec.disabled)" = "true" ] || fail "disabled"
[ "$(model vllm-gpu-qwen3-14b .metadata.namespace)" = "other" ] || fail "a Model follows its server's namespace"
[ "$(model vllm-gpu-qwen3-14b '.metadata.labels["kubemoot.ai/license"]')" = "apache-2.0" ] || fail "label on a vllm catalog Model"

# Two models whose names differ only by punctuation would render one Model name: refused.
cat >"$values" <<'EOF'
modelServers:
  - name: ollama-gpu
    type: ollama
    endpoint: http://ollama.gpu:11434
    models:
      - model: "qwen3:14b"
      - model: "qwen3-14b"
EOF
if out="$(helm template kubemoot-operator "$chart" -f "$values" --namespace km 2>&1)"; then fail "colliding Model names were accepted"; fi
grep -qF 'both render the Model name "ollama-gpu-qwen3-14b"' <<<"$out" || fail "collision message: $out"

echo "PASS: modelServers render tests"
