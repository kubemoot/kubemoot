#!/usr/bin/env bash
# The first crew you make works: scaffold the crew `kmctl create` writes, install it on the
# quickstart cluster, wait for it by state, ask it about its own namespace, and check the
# answer against the cluster.
#
# Run it after quickstart.sh, on the same cluster (it needs the operator and the catalog
# model the quickstart installs; the scaffold ships no Models). quickstart.yaml runs it
# against a release candidate, with a kmctl candidate built by kmctl-ref.sh.
#
# The check is deterministic: the ground truth is `kubectl get pods` for the crew's
# namespace, reduced to workload names (pod hashes dropped). The answer passes when it
# names every one of them, in any wording. It does not rescue a weak model: one ask, no
# retry, and a miss fails with the answer and what was expected. Lower NAMED_MIN only to
# measure how many names a model finds, never to make a run pass.
#
# Requires: kubectl (pointed at the cluster), python3, kmctl (KMCTL, default: on PATH).
#
# Environment (all optional):
#   KMCTL           the kmctl binary (default: kmctl)
#   CREW            name of the scaffolded crew (default: starter-guide)
#   MEMBERS         specialists beside the coordinator (default: 1, the scaffold's default)
#   QUESTION        what to ask (default: names every pod in the crew's namespace)
#   NAMED_MIN       workload names the answer must contain (default: all of them)
#   READY_TIMEOUT   safety net in seconds for the crew to become ready (default 900)
#   ASK_TIMEOUT     safety net for the discussion (default 15m)
#
# The waits poll Kubernetes status; the timeouts only stop a run that will never finish.
set -euo pipefail

KMCTL="${KMCTL:-kmctl}"
CREW="${CREW:-starter-guide}"
MEMBERS="${MEMBERS:-1}"
NS="crew-${CREW}"
QUESTION="${QUESTION:-Which pods are running in your namespace? Name every one of them.}"
NAMED_MIN="${NAMED_MIN:-}"
READY_TIMEOUT="${READY_TIMEOUT:-900}"
ASK_TIMEOUT="${ASK_TIMEOUT:-15m}"

need() { command -v "$1" >/dev/null 2>&1 || { echo "scaffolded-crew: missing tool: $1" >&2; exit 2; }; }
step() { printf '\n== %s ==\n' "$*"; }

# workloads: the workload name of each pod in the namespace, one per line, sorted and
# distinct. A Deployment pod is <name>-<replicaset hash>-<pod hash>, a StatefulSet pod
# <name>-<ordinal>; finished Job pods are not part of what the crew runs.
workloads() {
  kubectl get pods -n "$NS" --field-selector=status.phase!=Succeeded -o name \
    | sed -E 's#^pod/##; s/-[a-z0-9]{6,10}-[a-z0-9]{5}$//; s/-[0-9]+$//' | sort -u
}

# crew_state: "true" when the crew is reconciled and a model in the catalog can run it.
# The phase alone is not enough on the way to Ready, and Ready alone is also true while
# the crew waits for capacity.
crew_state() {
  local phase capacity
  phase="$(kubectl get crew "$CREW" -n "$NS" -o jsonpath='{.status.phase}')"
  capacity="$(kubectl get crew "$CREW" -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="CapacityAvailable")].status}')"
  [ "$phase" = "Ready" ] && [ "$capacity" = "True" ] && echo true || echo false
}

# diagnose: what the crew says about itself and what its agents report, for a failed run.
diagnose() {
  echo "--- crew $CREW ---" >&2
  kubectl get crew "$CREW" -n "$NS" -o jsonpath='phase={.status.phase}{"\n"}{range .status.conditions[*]}{.type}={.status} ({.reason}) {.message}{"\n"}{end}' >&2 || true
  echo "--- agents ---" >&2
  kubectl get agents -n "$NS" -o custom-columns='AGENT:.metadata.name,PHASE:.status.phase,MESSAGE:.status.message' >&2 || true
  echo "--- pods ---" >&2
  kubectl get pods -n "$NS" -o wide >&2 || true
  echo "--- warning events ---" >&2
  kubectl get events -n "$NS" --field-selector type=Warning --sort-by=.lastTimestamp >&2 || true
}

wait_for_crew() {
  local deadline=$(( $(date +%s) + READY_TIMEOUT ))
  until [ "$(crew_state 2>/dev/null)" = "true" ]; do
    if [ "$(date +%s)" -gt "$deadline" ]; then
      echo "scaffolded-crew: crew $CREW not Ready with capacity after ${READY_TIMEOUT}s" >&2
      diagnose; return 1
    fi
    sleep 5
  done
  echo "crew $CREW is Ready and has capacity"
}

# check_answer ANSWER_FILE WORKLOADS_FILE: print what the answer names and what it misses;
# succeed when it names at least NAMED_MIN workloads (default: all).
check_answer() {
  python3 -I - "$1" "$2" "${NAMED_MIN:-}" <<'PY'
import sys
answer = open(sys.argv[1], encoding="utf-8").read().lower()
expected = [w for w in open(sys.argv[2], encoding="utf-8").read().split() if w]
need = int(sys.argv[3]) if sys.argv[3] else len(expected)
named = [w for w in expected if w.lower() in answer]
missed = [w for w in expected if w not in named]
print(f"named {len(named)} of {len(expected)} workloads (need {need})")
for w in named:
    print(f"  named   {w}")
for w in missed:
    print(f"  missing {w}")
sys.exit(0 if expected and len(named) >= need else 1)
PY
}

main() {
  need kubectl; need python3; need "$KMCTL"
  local work answer_file
  work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT

  step "1/4 Scaffold the crew with $("$KMCTL" version 2>&1 | head -n 1)"
  "$KMCTL" create "$CREW" --members "$MEMBERS" --no-input -n "$NS" -o "$work"

  step "2/4 Install it in namespace $NS"
  kubectl create namespace "$NS" --dry-run=client -o yaml | kubectl apply -f -
  kubectl apply -n "$NS" -f "$work/$CREW/access/rbac.yaml"
  "$KMCTL" apply -f "$work/$CREW" -n "$NS"

  step "3/4 Wait for the crew, by state (phase Ready and CapacityAvailable True)"
  wait_for_crew
  # The crew's own workloads (agents, tool server, gateway, discussion gateway) are what the
  # question is about; wait until each deployment is available so the pods exist to be named.
  if ! kubectl wait --for=condition=Available deployment --all -n "$NS" --timeout="${READY_TIMEOUT}s"; then
    echo "scaffolded-crew: the crew's deployments did not become available" >&2
    diagnose; exit 1
  fi

  step "4/4 Ask: $QUESTION"
  answer_file="$work/answer.txt"
  if ! "$KMCTL" conversation ask "$CREW" "$QUESTION" --quiet -n "$NS" --timeout "$ASK_TIMEOUT" > "$answer_file"; then
    echo "scaffolded-crew: kmctl conversation ask failed" >&2
    diagnose; exit 1
  fi
  printf '\n--- answer ---\n%s\n--------------\n' "$(cat "$answer_file")"
  [ -s "$answer_file" ] || { echo "scaffolded-crew: the crew gave no answer" >&2; diagnose; exit 1; }

  # The ground truth is read after the answer, from the live cluster.
  workloads > "$work/workloads.txt"
  echo "the namespace runs: $(paste -sd' ' "$work/workloads.txt")"
  if ! check_answer "$answer_file" "$work/workloads.txt"; then
    echo "scaffolded-crew: the answer does not name the crew's pods" >&2
    diagnose; exit 1
  fi
  echo "scaffolded-crew: PASS"
}

# Sourced (by the tests), it defines the functions and runs nothing.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
