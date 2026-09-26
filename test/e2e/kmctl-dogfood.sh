#!/usr/bin/env bash
#
# Tier 3 dogfood e2e: drive Kubemoot THROUGH kmctl against a LIVE cluster - the
# real drift catch (if kmctl drifts from the operator's CRDs/endpoints, this fails).
#
# Two parts, by design:
#   A. Lifecycle on a SCAFFOLDED crew: status -> create -> apply -> Ready -> delete.
#      Proves scaffolding + CRD apply/cleanup. Self-cleaning.
#   B. Inference on an EXISTING known-good crew (default homelab-pilot): conversation
#      ask + fitness read/poll/download. Proves kmctl's conversation/fitness plumbing
#      against a crew that actually runs - decoupled from scaffold quality. Read-only +
#      ephemeral; it does NOT modify or delete the existing crew.
#
# DELIBERATELY NOT in per-push CI: needs a live cluster + operator. Run by hand or via
# the workflow_dispatch action.  Idempotent: unique scaffold namespace per run, a
# pre-run sweep of orphaned scaffold namespaces, and cleanup on EVERY exit (pass or fail).
#
# Requires: kmctl, kubectl (cluster-admin context).
#
set -euo pipefail

SCAFFOLD_NAME="dogfood"
SCAFFOLD_NS="kmctl-dogfood-$$-$(date +%s)"
WORKDIR="$(mktemp -d)"
READY_TIMEOUT="${READY_TIMEOUT:-600}"
ASK_TIMEOUT="${ASK_TIMEOUT:-5m}"
E2E_CREW="${E2E_CREW:-homelab-pilot}"
E2E_CREW_NS="${E2E_CREW_NS:-crew-homelab-pilot}"

need() { command -v "$1" >/dev/null 2>&1 || { echo "dogfood: missing tool: $1" >&2; exit 1; }; }
need kmctl; need kubectl

cleanup() {
  echo "== cleanup (runs on success or failure) =="
  kmctl delete crewfitnesssuite "$SCAFFOLD_NAME-starter" -n "$SCAFFOLD_NS" 2>/dev/null || true
  kmctl delete crew "$SCAFFOLD_NAME" -n "$SCAFFOLD_NS" 2>/dev/null || true
  kubectl delete namespace "$SCAFFOLD_NS" --ignore-not-found --wait=false 2>/dev/null || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

echo "== idempotency: sweep orphaned scaffold namespaces from prior aborted runs =="
kubectl get ns -o name 2>/dev/null | grep '^namespace/kmctl-dogfood-' | grep -v "$SCAFFOLD_NS" \
  | xargs -r -n1 kubectl delete --ignore-not-found --wait=false 2>/dev/null || true

echo "== preflight: cluster reachable + Kubemoot installed =="
kmctl status

# ---- Part A: scaffold lifecycle ---------------------------------------------
echo "== A1 scaffold a crew (kmctl create) =="
kmctl create "$SCAFFOLD_NAME" --members 2 --model-family qwen --no-input -o "$WORKDIR"

echo "== A2 namespace + apply (kmctl apply, CRD-only) =="
kubectl create namespace "$SCAFFOLD_NS"
kmctl apply -f "$WORKDIR/$SCAFFOLD_NAME" -n "$SCAFFOLD_NS"

echo "== A3 wait for the crew to come Ready (<= ${READY_TIMEOUT}s) =="
deadline=$(( $(date +%s) + READY_TIMEOUT ))
until [ "$(kubectl get crew "$SCAFFOLD_NAME" -n "$SCAFFOLD_NS" -o jsonpath='{.status.ready}' 2>/dev/null)" = "true" ]; do
  [ "$(date +%s)" -gt "$deadline" ] && { echo "dogfood: scaffold crew not Ready in time" >&2; exit 1; }
  sleep 5
done
echo "scaffold crew Ready; deleting it (lifecycle proven)"
kmctl delete crew "$SCAFFOLD_NAME" -n "$SCAFFOLD_NS"

# ---- Part B: inference against an existing crew ------------------------------
echo "== B1 confirm the existing crew $E2E_CREW is Ready =="
[ "$(kubectl get crew "$E2E_CREW" -n "$E2E_CREW_NS" -o jsonpath='{.status.ready}' 2>/dev/null)" = "true" ] \
  || { echo "dogfood: existing crew $E2E_CREW (-n $E2E_CREW_NS) not Ready; set E2E_CREW/E2E_CREW_NS" >&2; exit 1; }

echo "== B2 ask it a question (kmctl conversation ask) =="
kmctl conversation ask "$E2E_CREW" "Briefly, what is this crew responsible for?" --quiet -n "$E2E_CREW_NS" --timeout "$ASK_TIMEOUT"

echo "== B3 fitness read plumbing on an existing suite =="
SUITE="$(kubectl get crewfitnesssuites -n "$E2E_CREW_NS" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)"
if [ -n "$SUITE" ]; then
  echo "  suite: $SUITE"
  # capture before head: piping kmctl into head closes the pipe early -> SIGPIPE -> pipefail.
  scn="$(kmctl fitness scenarios "$SUITE" -n "$E2E_CREW_NS")"
  printf '%s\n' "$scn" | head -3
  kmctl fitness get "$SUITE" -n "$E2E_CREW_NS"
  kmctl fitness run "$SUITE" -n "$E2E_CREW_NS" --timeout 2m   # already Completed -> returns at once (tests poll path)
  kmctl fitness download "$SUITE" -n "$E2E_CREW_NS" -o "$WORKDIR/$SUITE.xlsx"
  [ -s "$WORKDIR/$SUITE.xlsx" ] || { echo "dogfood: artifact download empty" >&2; exit 1; }
  echo "  artifact: $(wc -c < "$WORKDIR/$SUITE.xlsx") bytes"
else
  echo "  no existing suite in $E2E_CREW_NS; skipping fitness read steps"
fi

echo "== PASS: kmctl drove scaffold lifecycle + live inference against $E2E_CREW =="
