#!/usr/bin/env bash
# Kubemoot quickstart: from an empty cluster to a crew answering a question.
#
# Installs NATS, a CPU Ollama with a small model, the operator chart, a ModelProvider,
# and the two-agent "hello" crew; then asks the crew one question and prints the
# answer. The same script is what CI runs (.github/workflows/quickstart.yaml), so the
# README and the test cannot drift apart.
#
# Requires: kubectl (pointed at the target cluster), helm, python3. Nothing else.
#
# Environment (all optional):
#   KUBEMOOT_CHART          chart to install. A local path in a checkout, or the OCI
#                           reference of a release (default: the published chart).
#   KUBEMOOT_CHART_VERSION  chart version when installing from OCI (default: latest).
#   KUBEMOOT_IMAGE_TAG      tag for locally built images already loaded into the
#                           cluster (CI: "ci"). Unset means the chart's released images.
#   QUICKSTART_MODEL        Ollama model for the crew (default: qwen2.5:1.5b).
#   QUESTION                what to ask (default: the capital of France).
#   EXPECT                  substring the answer must contain (default: Paris).
#   READY_TIMEOUT           safety net in seconds for each readiness wait (default 900).
#   ASK_TIMEOUT             safety net for the discussion (default 15m).
#
# Every wait below polls a Kubernetes status field; the timeouts only stop a run that
# is never going to finish.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
KUBEMOOT_CHART="${KUBEMOOT_CHART:-oci://ghcr.io/kubemoot/charts/kubemoot-operator}"
KUBEMOOT_CHART_VERSION="${KUBEMOOT_CHART_VERSION:-}"
KUBEMOOT_IMAGE_TAG="${KUBEMOOT_IMAGE_TAG:-}"
QUICKSTART_MODEL="${QUICKSTART_MODEL:-qwen2.5:1.5b}"
QUESTION="${QUESTION:-What is the capital of France? Answer in one sentence.}"
EXPECT="${EXPECT:-Paris}"
READY_TIMEOUT="${READY_TIMEOUT:-900}"
ASK_TIMEOUT="${ASK_TIMEOUT:-15m}"
CREW=hello
CREW_NS=hello

need() { command -v "$1" >/dev/null 2>&1 || { echo "quickstart: missing tool: $1" >&2; exit 2; }; }
need kubectl; need helm; need python3

step() { printf '\n== %s ==\n' "$*"; }

# wait_for LABEL SECONDS COMMAND...: poll COMMAND until it prints "true".
wait_for() {
  local label="$1" budget="$2"; shift 2
  local deadline=$(( $(date +%s) + budget ))
  until [ "$("$@" 2>/dev/null)" = "true" ]; do
    if [ "$(date +%s)" -gt "$deadline" ]; then
      echo "quickstart: $label not ready after ${budget}s" >&2
      return 1
    fi
    sleep 5
  done
  echo "$label ready"
}

printf 'cluster %s\n' "$(kubectl config current-context)"
kubectl get nodes -o custom-columns='NODE:.metadata.name,CPU:.status.allocatable.cpu,MEMORY:.status.allocatable.memory'

step "1/6 NATS JetStream (release nats, namespace nats)"
helm repo add nats https://nats-io.github.io/k8s/helm/charts/ >/dev/null 2>&1 || true
helm repo update nats >/dev/null
helm upgrade --install nats nats/nats --namespace nats --create-namespace \
  --values "$HERE/nats-values.yaml" --wait --timeout 10m

step "2/6 Ollama on CPU, pulling $QUICKSTART_MODEL"
kubectl apply -f "$HERE/ollama.yaml"
kubectl rollout status deployment/ollama -n ollama --timeout="${READY_TIMEOUT}s"
kubectl exec -n ollama deployment/ollama -- ollama pull "$QUICKSTART_MODEL"

step "3/6 Kubemoot operator ($KUBEMOOT_CHART)"
# A checkout carries the dashboard as a local subchart that must be built into charts/.
[ -d "$KUBEMOOT_CHART" ] && helm dependency build "$KUBEMOOT_CHART" >/dev/null
chart_args=(--namespace kubemoot --create-namespace --values "$HERE/operator-values.yaml" --wait --timeout 10m)
[ -n "$KUBEMOOT_CHART_VERSION" ] && chart_args+=(--version "$KUBEMOOT_CHART_VERSION")
if [ -n "$KUBEMOOT_IMAGE_TAG" ]; then
  # Locally built images, already loaded into the cluster under the chart's default
  # registry prefix. Component images are "name:tag"; the chart adds the registry.
  # The dashboard image is not built locally, so it stays off in this mode.
  chart_args+=(--set "dashboard.enabled=false"
               --set "image.tag=$KUBEMOOT_IMAGE_TAG"
               --set "image.pullPolicy=IfNotPresent"
               --set "kubemootConfig.images.agentRuntime=agent-runtime:$KUBEMOOT_IMAGE_TAG"
               --set "kubemootConfig.images.discussionGateway=discussion-gateway:$KUBEMOOT_IMAGE_TAG")
fi
# Helm installs CRDs on first install and never upgrades them, so apply the chart's CRDs
# explicitly: a rerun on an existing cluster then picks up new fields too.
crd_args=("$KUBEMOOT_CHART"); [ -n "$KUBEMOOT_CHART_VERSION" ] && crd_args+=(--version "$KUBEMOOT_CHART_VERSION")
helm show crds "${crd_args[@]}" | kubectl apply --server-side --force-conflicts -f -
helm upgrade --install kubemoot-operator "$KUBEMOOT_CHART" "${chart_args[@]}"

step "4/6 ModelProvider ollama-local"
kubectl apply -f "$HERE/modelprovider.yaml"
wait_for "ModelProvider ollama-local" "$READY_TIMEOUT" \
  kubectl get modelprovider ollama-local -n kubemoot -o jsonpath='{.status.ready}'

step "5/6 The hello crew (namespace $CREW_NS)"
kubectl create namespace "$CREW_NS" --dry-run=client -o yaml | kubectl apply -f -
# The Model's spec names the quickstart model; keep the manifest and the variable in step.
python3 - "$HERE/crew/models.yaml" "$QUICKSTART_MODEL" <<'PY' | kubectl apply -n "$CREW_NS" -f -
import re, sys
text = open(sys.argv[1]).read()
out, n = re.subn(r'^(\s*model:\s*).*$', lambda m: f'{m.group(1)}"{sys.argv[2]}"', text, count=1, flags=re.M)
if n != 1:
    sys.exit("quickstart: no 'model:' line found in " + sys.argv[1])
print(out)
PY
kubectl apply -n "$CREW_NS" -f "$HERE/crew/promptmodules.yaml" -f "$HERE/crew/agents.yaml" -f "$HERE/crew/crew.yaml"
wait_for "Model" "$READY_TIMEOUT" \
  kubectl get models -n "$CREW_NS" -o jsonpath='{.items[0].status.ready}'
wait_for "Crew $CREW" "$READY_TIMEOUT" \
  kubectl get crew "$CREW" -n "$CREW_NS" -o jsonpath='{.status.ready}'
kubectl rollout status deployment -n "$CREW_NS" -l kubemoot.ai/crew="$CREW" --timeout="${READY_TIMEOUT}s"

step "6/6 Ask: $QUESTION"
# The crew's discussion gateway is reached through the API server's service proxy, so
# the only credential needed is the kubeconfig already in use (kmctl does the same).
proxy="/api/v1/namespaces/$CREW_NS/services/$CREW-discussion:80/proxy/api/v1/discussions/$CREW"
body="$(python3 -c 'import json,sys; print(json.dumps({"message": sys.argv[1]}))' "$QUESTION")"
conversation="$(printf '%s' "$body" | kubectl create --raw "$proxy" -f - \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["conversationId"])')"
echo "conversation $conversation started; streaming until synthesis"
# The stream is server-sent events, one JSON object per "data:" line with the event
# kind in its "type" field; it closes on "done", so kubectl returns it whole. Capture it
# first so a transport failure is reported as such, not as a missing synthesis.
stream="$(mktemp)"; trap 'rm -f "$stream"' EXIT
if ! timeout "$ASK_TIMEOUT" kubectl get --raw "$proxy/$conversation/stream" > "$stream"; then
  echo "quickstart: could not read the discussion stream (gateway unreachable or ASK_TIMEOUT hit)" >&2; exit 1
fi
answer="$(python3 -c '
import json, sys
answer = ""
for line in sys.stdin:
    if not line.startswith("data:"):
        continue
    event = json.loads(line[5:])
    kind = event.get("type")
    if kind == "synthesis":
        answer = event.get("content", "")
    elif kind == "error":
        sys.exit("discussion error: " + event.get("error", line.strip()))
    elif kind == "phase":
        print("  " + event.get("agent", "") + " " + event.get("status", "") + " " + event.get("signal", ""), file=sys.stderr)
print(answer)
' < "$stream")"

printf '\n--- answer ---\n%s\n--------------\n' "$answer"
[ -n "$answer" ] || { echo "quickstart: no synthesis received" >&2; exit 1; }
case "$answer" in
  "The discussion could not be started"*) echo "quickstart: gateway could not start the discussion" >&2; exit 1 ;;
esac
if [ -n "$EXPECT" ] && ! printf '%s' "$answer" | grep -qi -- "$EXPECT"; then
  echo "quickstart: answer does not mention '$EXPECT'" >&2; exit 1
fi
echo "quickstart: PASS"
echo "dashboard (no login, so it is not exposed): kubectl port-forward -n kubemoot svc/kubemoot-operator-dashboard 8080:80"
echo "then open http://localhost:8080/dashboard"
