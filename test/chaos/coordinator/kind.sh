#!/usr/bin/env bash
# Runs the coordinator chaos test on a throwaway kind cluster: creates the cluster with its
# own kubeconfig, installs Kubemoot and the quickstart "hello" crew with the quickstart
# script, turns the coordinator protections on, runs the protected matrix, turns them off,
# runs the control matrix, and deletes the cluster.
#
# The default kubeconfig is never read or written: the cluster gets a kubeconfig file in a
# temporary directory, and every kubectl, helm and kind call below names it. A kind delete
# against the default kubeconfig would unset the current context of whatever cluster you
# were using; this script cannot do that.
#
# Requires: kind, kubectl, helm, go, python3, and a container runtime kind can use.
#
# Environment (all optional):
#   CLUSTER_NAME        kind cluster name; must start with "kubemoot-chaos" (default kubemoot-chaos)
#   KUBEMOOT_IMAGE_TAG  tag of Kubemoot images you already loaded into the cluster with
#                       `kind load docker-image` (passed to the quickstart); unset uses the
#                       chart's released images, which must contain the coordinator log, resume
#                       and lease code (PRs 1 to 4 of the stateless coordinator)
#   CHAOS_CASES         comma-separated case name filters (default: the whole matrix)
#   OUT_DIR             where the reports go (default ./chaos-report)
#   KEEP_CLUSTER        set to 1 to leave the cluster up when the run ends
#   READY_TIMEOUT       safety net in seconds for each wait (default 900)
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
CLUSTER_NAME="${CLUSTER_NAME:-kubemoot-chaos}"
OUT_DIR="${OUT_DIR:-$PWD/chaos-report}"
READY_TIMEOUT="${READY_TIMEOUT:-900}"
CREW=hello

case "$CLUSTER_NAME" in
  kubemoot-chaos*) ;;
  *) echo "kind.sh: CLUSTER_NAME must start with kubemoot-chaos (got $CLUSTER_NAME)" >&2; exit 2 ;;
esac
for tool in kind kubectl helm go python3; do
  command -v "$tool" >/dev/null 2>&1 || { echo "kind.sh: missing tool: $tool" >&2; exit 2; }
done

WORK="$(mktemp -d)"
export KUBECONFIG="$WORK/kubeconfig"
CONTEXT="kind-$CLUSTER_NAME"

# shellcheck disable=SC2329 # run by the EXIT trap
cleanup() {
  if [ "${KEEP_CLUSTER:-0}" = 1 ]; then
    echo "kind.sh: leaving the cluster up; use KUBECONFIG=$KUBECONFIG"
    return
  fi
  kind delete cluster --name "$CLUSTER_NAME" --kubeconfig "$KUBECONFIG" || true
  rm -rf "$WORK"
}
existing="$(kind get clusters)" || { echo "kind.sh: cannot list kind clusters (is the container runtime up?)" >&2; exit 2; }
if printf '%s\n' "$existing" | grep -qx "$CLUSTER_NAME"; then
  echo "kind.sh: a kind cluster named $CLUSTER_NAME already exists; delete it or set CLUSTER_NAME" >&2
  exit 2
fi
trap cleanup EXIT

step() { printf '\n== %s ==\n' "$*"; }

# set_flags VALUE: the chart's coordinator protections on (true) or off (false).
set_flags() {
  helm upgrade kubemoot-operator "$ROOT/operator/chart/kubemoot-operator" --namespace kubemoot \
    --kubeconfig "$KUBECONFIG" --kube-context "$CONTEXT" --reuse-values --wait --timeout 10m \
    --set "coordinator.eventLog=$1" --set "coordinator.resume=$1" --set "coordinator.lease=$1"
}

# wait_flags MODE: poll the driver's own preflight until the coordinator pods carry the flags
# of the mode (the operator rolls them after the KubemootConfig changes).
wait_flags() {
  local mode="$1" deadline=$(( $(date +%s) + READY_TIMEOUT ))
  until "$WORK/coordinator-chaos" --kubeconfig "$KUBECONFIG" --context "$CONTEXT" --crew "$CREW" \
        --mode "$mode" --preflight >/dev/null 2>&1; do
    [ "$(date +%s)" -gt "$deadline" ] && { echo "kind.sh: coordinator flags for $mode mode not in effect" >&2; return 1; }
    sleep 5
  done
}

step "build the driver"
(cd "$ROOT/operator" && go build -o "$WORK/coordinator-chaos" ./cmd/coordinator-chaos)

step "create kind cluster $CLUSTER_NAME (kubeconfig $KUBECONFIG)"
kind create cluster --name "$CLUSTER_NAME" --kubeconfig "$KUBECONFIG" --wait 120s

step "install Kubemoot and the $CREW crew with the quickstart"
KUBEMOOT_CHART="$ROOT/operator/chart/kubemoot-operator" bash "$ROOT/quickstart/quickstart.sh"

rc=0
run() {
  local mode="$1" code=0
  "$WORK/coordinator-chaos" --kubeconfig "$KUBECONFIG" --context "$CONTEXT" --crew "$CREW" --mode "$mode" \
    ${CHAOS_CASES:+--cases "$CHAOS_CASES"} --out "$OUT_DIR/$mode" || code=$?
  # keep the worst result: a refusal (2) or a failure (1) must not be hidden by a later 3 or 0
  case "${code:-0}" in
    0) ;;
    2) rc=2 ;;
    1) if [ "$rc" != 2 ]; then rc=1; fi ;;
    *) if [ "$rc" = 0 ]; then rc="$code"; fi ;;
  esac
}

step "protected matrix (event log, resume and leases on)"
set_flags true
wait_flags protected
run protected

step "control matrix (all three off)"
set_flags false
wait_flags control
run control

echo
echo "reports: $OUT_DIR/protected/report.md and $OUT_DIR/control/report.md"
exit "$rc"
