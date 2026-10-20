#!/usr/bin/env bash
# Kubemoot quickstart: from an empty cluster to a crew answering a question.
#
# Installs NATS, a CPU Ollama, the operator chart with a ModelServer for that Ollama
# and one model in the cluster's catalog, and the two-agent "hello" crew; then asks
# the crew one question and prints the answer. The crew ships no Models: the operator
# downloads the catalog model onto the server and the scheduler binds the agents to it.
# The same script is what CI runs (.github/workflows/quickstart.yaml), so the README
# and the test cannot drift apart.
#
# Requires: kubectl (pointed at the target cluster), helm, python3. Nothing else.
#
# Environment (all optional):
#   KUBEMOOT_CHART          chart to install. A local path in a checkout, or the OCI
#                           reference of a release (default: the published chart).
#   KUBEMOOT_CHART_VERSION  chart version when installing from OCI. Default: the release
#                           this checkout belongs to, read from its git tag
#                           (chart-version.sh); refused when the quickstart files
#                           changed after that release, because they may not match it.
#   KUBEMOOT_IMAGE_TAG      tag for locally built images already loaded into the
#                           cluster (CI: "ci"). Unset means the chart's released images.
#   QUICKSTART_MODEL        Ollama model for the catalog (default: the one in
#                           operator-values.yaml).
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
QUICKSTART_MODEL="${QUICKSTART_MODEL:-}"
QUESTION="${QUESTION:-What is the capital of France? Answer in one sentence.}"
EXPECT="${EXPECT:-Paris}"
READY_TIMEOUT="${READY_TIMEOUT:-900}"
ASK_TIMEOUT="${ASK_TIMEOUT:-15m}"
CREW=hello
CREW_NS=hello

need() { command -v "$1" >/dev/null 2>&1 || { echo "quickstart: missing tool: $1" >&2; exit 2; }; }
need kubectl; need helm; need python3

# shellcheck source-path=SCRIPTDIR source=chart-version.sh
source "$HERE/chart-version.sh"

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

# A published chart must be the one these manifests were written for.
if [ -z "$KUBEMOOT_CHART_VERSION" ] && [[ "$KUBEMOOT_CHART" == oci://* ]]; then
  KUBEMOOT_CHART_VERSION="$(release_chart_version "$HERE")" || {
    echo "quickstart: cannot choose the chart version that matches these manifests" >&2
    exit 2
  }
  echo "chart version $KUBEMOOT_CHART_VERSION (the release this checkout belongs to)"
fi

printf 'cluster %s\n' "$(kubectl config current-context)"
kubectl get nodes -o custom-columns='NODE:.metadata.name,CPU:.status.allocatable.cpu,MEMORY:.status.allocatable.memory'

step "1/6 NATS JetStream (release nats, namespace nats)"
helm repo add nats https://nats-io.github.io/k8s/helm/charts/ >/dev/null 2>&1 || true
helm repo update nats >/dev/null
helm upgrade --install nats nats/nats --namespace nats --create-namespace \
  --values "$HERE/nats-values.yaml" --wait --timeout 10m

step "2/6 Ollama on CPU"
kubectl apply -f "$HERE/ollama.yaml"
kubectl rollout status deployment/ollama -n ollama --timeout="${READY_TIMEOUT}s"

step "3/6 Kubemoot operator ($KUBEMOOT_CHART), its ModelServer and catalog model"
# A checkout carries the dashboard as a local subchart that must be built into charts/.
[ -d "$KUBEMOOT_CHART" ] && helm dependency build "$KUBEMOOT_CHART" >/dev/null
chart_args=(--namespace kubemoot --create-namespace --values "$HERE/operator-values.yaml" --wait --timeout 10m)
[ -n "$KUBEMOOT_CHART_VERSION" ] && chart_args+=(--version "$KUBEMOOT_CHART_VERSION")
# The catalog model is the first models entry of the ModelServer in operator-values.yaml.
[ -n "$QUICKSTART_MODEL" ] && chart_args+=(--set-string "modelServers[0].models[0].model=$QUICKSTART_MODEL")
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

step "4/6 ModelServer ollama-local and its catalog model"
wait_for "ModelServer ollama-local" "$READY_TIMEOUT" \
  kubectl get modelserver ollama-local -n kubemoot -o jsonpath='{.status.ready}'
# The operator downloads the model onto the server; the Model is ready once it is there.
wait_for "catalog model" "$READY_TIMEOUT" \
  kubectl get models -n kubemoot -o jsonpath='{.items[?(@.spec.serverRef=="ollama-local")].status.ready}'

step "5/6 The hello crew (namespace $CREW_NS)"
kubectl create namespace "$CREW_NS" --dry-run=client -o yaml | kubectl apply -f -
# The crew ships no Models; its agents are scheduled from the catalog.
kubectl apply -n "$CREW_NS" -f "$HERE/crew/promptmodules.yaml" -f "$HERE/crew/agents.yaml" -f "$HERE/crew/crew.yaml"
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
