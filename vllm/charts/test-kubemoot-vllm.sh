#!/usr/bin/env bash
# Tests for the kubemoot-vllm chart: lint, render assertions, rejected values, and the
# launcher's own tests. Needs helm, yq, and python3. Run from anywhere:
# vllm/charts/test-kubemoot-vllm.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-vllm"
example="$chart/examples/two-models.yaml"

fail() { echo "FAIL: $*" >&2; exit 1; }
render() { helm template vg "$chart" --namespace vllm "$@"; }
# get <rendered> <kind> <yq path>: the path in the one document of that kind.
get() { yq -N "select(.kind == \"$2\") | $3" <<<"$1"; }
# rejects <expected message fragment> <helm args>: the render must fail with the message.
rejects() {
  local want="$1" out
  shift
  if out="$(render "$@" 2>&1)"; then fail "expected a failure containing: $want"; fi
  grep -qF -- "$want" <<<"$out" || fail "failure does not contain '$want': $out"
}

helm lint "$chart" -f "$example" >/dev/null || fail "helm lint failed"

# ---- required values --------------------------------------------------------
rejects "models: list at least one model" --set 'template.container.image=x/y:1@sha256:0000000000000000000000000000000000000000000000000000000000000000' --set weights.size=1Gi
rejects "must be pinned by digest" -f "$example" --set template.container.image=vllm/vllm-openai:v0.32.0
rejects "must be pinned by digest" -f "$example" --set template.container.image=
rejects "weights.size is required" -f "$example" --set weights.size=
rejects "sleepLevel must be 1 or 2" -f "$example" --set sleepLevel=3

# ---- the two-model example --------------------------------------------------
out="$(render -f "$example")"

# No Kubemoot objects: a plain vLLM server chart.
[ "$(yq -N 'select(.apiVersion | test("kubemoot.ai")) | .kind' <<<"$out")" = "" ] || fail "the chart rendered a Kubemoot object"
[ "$(yq -N '.kind' <<<"$out" | sort | tr '\n' ' ')" = "ConfigMap Deployment PersistentVolumeClaim Service " ] \
  || fail "unexpected kinds: $(yq -N '.kind' <<<"$out" | tr '\n' ' ')"

# One Deployment, one replica, recreated, one container, service links off.
[ "$(get "$out" Deployment .spec.replicas)" = "1" ] || fail "replicas is not 1"
[ "$(get "$out" Deployment .spec.strategy.type)" = "Recreate" ] || fail "strategy is not Recreate"
[ "$(get "$out" Deployment '.spec.template.spec.containers | length')" = "1" ] || fail "not exactly one container"
[ "$(get "$out" Deployment .spec.template.spec.enableServiceLinks)" = "false" ] || fail "service links are on"
[ "$(get "$out" Deployment '.spec.template.spec.containers[0].command | join(" ")')" = "python3 /launcher/launcher.py /launcher/servers.json" ] \
  || fail "the container does not run the launcher"
[ "$(get "$out" Deployment '.spec.template.spec.containers[0].image')" = "vllm/vllm-openai:v0.32.0@sha256:befccf0e7e4089aa23926bb6f887a85c0d46c2a99e6de5c5ffdf5cb7957c5b34" ] \
  || fail "image not rendered intact"
[ "$(get "$out" Deployment '.spec.template.spec.nodeSelector["kubernetes.io/hostname"]')" = "gpu-worker" ] || fail "nodeSelector lost"
[ "$(get "$out" Deployment '.spec.template.spec.tolerations[0].key')" = "nvidia.com/gpu" ] || fail "toleration lost"
[ "$(get "$out" Deployment '.spec.template.spec.containers[0].resources.limits["nvidia.com/gpu"]')" = "1" ] || fail "accelerator resource lost"
[ "$(get "$out" Deployment '.spec.template.spec.containers[0].resources.limits.memory')" = "44Gi" ] || fail "memory limit lost"

# Caches on the weights volume, sleep endpoints on, shared memory in memory.
env="$(get "$out" Deployment '.spec.template.spec.containers[0].env[] | .name + "=" + .value')"
grep -qx 'HF_HOME=/models/huggingface' <<<"$env" || fail "HF_HOME is not on the weights volume"
grep -qx 'VLLM_CACHE_ROOT=/models/vllm' <<<"$env" || fail "VLLM_CACHE_ROOT is not on the weights volume"
grep -qx 'VLLM_SERVER_DEV_MODE=1' <<<"$env" || fail "sleep endpoints are not enabled"
[ "$(get "$out" Deployment '.spec.template.spec.volumes[] | select(.name == "shm") | .emptyDir.medium')" = "Memory" ] || fail "/dev/shm is not memory-backed"
[ "$(get "$out" Deployment '.spec.template.spec.volumes[] | select(.name == "shm") | .emptyDir.sizeLimit')" = "8Gi" ] || fail "shmSize not rendered"
[ "$(get "$out" Deployment '.spec.template.spec.volumes[] | select(.name == "models") | .persistentVolumeClaim.claimName')" = "vg-kubemoot-vllm-weights" ] \
  || fail "the weights volume is not mounted from the claim"

# The weights claim is kept on uninstall.
[ "$(get "$out" PersistentVolumeClaim '.metadata.annotations["helm.sh/resource-policy"]')" = "keep" ] || fail "the weights claim is not kept"
[ "$(get "$out" PersistentVolumeClaim .spec.resources.requests.storage)" = "100Gi" ] || fail "weights size lost"
[ "$(get "$out" PersistentVolumeClaim .spec.storageClassName)" = "fast" ] || fail "storage class lost"
[ "$(get "$out" PersistentVolumeClaim '.spec.accessModes[0]')" = "ReadWriteOnce" ] || fail "the claim is not ReadWriteOnce"

# One Service port per model, sorted by served name, labelled so it can be found.
[ "$(get "$out" Service '.metadata.labels["kubemoot.ai/engine"]')" = "vllm" ] || fail "the Service is not labelled"
[ "$(get "$out" Service '.spec.ports | map(.port) | join(",")')" = "8000,8001" ] || fail "ports are not 8000,8001"
[ "$(get "$out" Service '.spec.ports | map(.name) | join(",")')" = "vllm-0,vllm-1" ] || fail "port names are not deterministic"
[ "$(get "$out" Service .metadata.name)" = "vg-kubemoot-vllm" ] || fail "unexpected Service name"

# The launcher and its server list, one server per model with its own arguments.
servers="$(get "$out" ConfigMap '.data["servers.json"]')"
[ "$(yq -p=json '.sleepLevel' <<<"$servers")" = "1" ] || fail "sleepLevel not in the launcher config"
[ "$(yq -p=json '.servers | length' <<<"$servers")" = "2" ] || fail "not two servers"
[ "$(yq -p=json '.servers[0].name + " " + .servers[0].port' <<<"$servers")" = "qwen3:14b 8000" ] || fail "first server is not qwen3:14b on 8000"
[ "$(yq -p=json '.servers[1].name + " " + .servers[1].port' <<<"$servers")" = "qwen3:32b 8001" ] || fail "second server is not qwen3:32b on 8001"
[ "$(yq -p=json '.servers[0].args | join(" ")' <<<"$servers")" = \
  "serve Qwen/Qwen3-14B-AWQ --revision=31c69efc --served-model-name=qwen3:14b --host=0.0.0.0 --port=8000 --enable-sleep-mode --gpu-memory-utilization=0.85 --max-model-len=16384 --enable-auto-tool-choice --tool-call-parser=hermes --reasoning-parser=qwen3" ] \
  || fail "first server args wrong: $(yq -p=json '.servers[0].args | join(" ")' <<<"$servers")"
yq -p=json '.servers[1].args | join(" ")' <<<"$servers" | grep -qF -- '--gpu-memory-utilization=0.95' || fail "second server lost its own memory fraction"
yq -p=json '.servers[1].args | join(" ")' <<<"$servers" | grep -qF -- '--revision=0499c3ac --served-model-name=qwen3:32b' || fail "second server revision or name wrong"
get "$out" ConfigMap '.data["launcher.py"]' | grep -q 'def start_all' || fail "the launcher script is not in the ConfigMap"

# ---- order does not depend on the values order ------------------------------
reordered="$(mktemp)"
trap 'rm -f "$reordered"' EXIT
yq '.models |= reverse' "$example" >"$reordered"
[ "$(render -f "$reordered" | yq -N 'select(.kind == "ConfigMap") | .data["servers.json"]')" = "$servers" ] \
  || fail "the order of the models in the values changed the render"

# ---- a changed model list rolls the pod --------------------------------------
before="$(get "$out" Deployment '.spec.template.metadata.annotations["checksum/launcher"]')"
more="$(mktemp)"
trap 'rm -f "$reordered" "$more"' EXIT
yq '.models += [{"servedName": "qwen3:8b", "repo": "Qwen/Qwen3-8B-AWQ", "revision": "abcdef12"}]' "$example" >"$more"
after="$(render -f "$more" | yq -N 'select(.kind == "Deployment") | .spec.template.metadata.annotations["checksum/launcher"]')"
[ -n "$before" ] && [ "$before" != "$after" ] || fail "adding a model does not change the pod template"
[ "$(render -f "$more" | yq -N 'select(.kind == "Service") | .spec.ports | length')" = "3" ] || fail "the new model has no port"

# ---- arguments the chart owns are rejected ----------------------------------
for flag in --model --served-model-name --revision --port --host --enable-sleep-mode; do
  rejects "set by the chart" -f "$example" --set "models[0].args[0]=${flag}"
  rejects "set by the chart" -f "$example" --set "models[0].args[0]=${flag}=x"
done
# A flag that only shares a prefix with an owned one is the user's.
render -f "$example" --set 'models[0].args[0]=--model-impl=vllm' >/dev/null || fail "--model-impl was rejected"

# ---- bad models --------------------------------------------------------------
rejects "appears twice" -f "$example" --set 'models[1].servedName=qwen3:14b'
rejects "models[0].repo is required" -f "$example" --set 'models[0].repo='
rejects "models[1].revision is required" -f "$example" --set 'models[1].revision='
rejects "models[0].servedName is required" -f "$example" --set 'models[0].servedName='

# ---- the notes print what to create in Kubemoot ------------------------------
notes="$(helm install vg "$chart" --namespace vllm --dry-run=client -f "$example" | sed -n '/^NOTES:/,$p')"
provider="$(awk '/^kind: ModelProvider/,/^---$/' <<<"$notes")"
grep -q '^  name: vg-kubemoot-vllm$' <<<"$provider" || fail "notes: ModelProvider name"
grep -q '^  type: vllm$' <<<"$provider" || fail "notes: ModelProvider type"
grep -q '^  sleepLevel: 1$' <<<"$provider" || fail "notes: sleepLevel must match the release"
grep -q '^    name: vg-kubemoot-vllm$' <<<"$provider" || fail "notes: serviceRef.name"
grep -q '^    namespace: vllm$' <<<"$provider" || fail "notes: serviceRef.namespace"
[ "$(grep -c '^kind: Model$' <<<"$notes")" = "2" ] || fail "notes: one Model skeleton per model"
grep -q '^  model: "qwen3:32b"$' <<<"$notes" || fail "notes: served name in the Model"
grep -q '^  providerRef: vg-kubemoot-vllm$' <<<"$notes" || fail "notes: providerRef"
grep -q 'labels: {}' <<<"$notes" || fail "notes: labels placeholder"

# ---- the launcher ------------------------------------------------------------
python3 -B -I "$here/launcher_test.py" >/dev/null 2>&1 || { python3 -B -I "$here/launcher_test.py" 2>&1 | tail -30; fail "launcher tests failed"; }

echo "PASS: kubemoot-vllm chart tests"
