#!/usr/bin/env bash
# Tests for workloads, crew_state and check_answer in test-scaffolded-crew.sh, with kubectl
# stubbed by a shell function.
# Usage: bash quickstart/test-scaffolded-crew.test.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR source=test-scaffolded-crew.sh
source "$here/test-scaffolded-crew.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
failures=0

check() {
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want [$2] got [$3]"; failures=$((failures + 1)); fi
}

# kubectl stub: pods come from $PODS; the crew's phase and capacity from $PHASE and $CAPACITY.
kubectl() {
  case "$*" in
    "get pods"*) # shellcheck disable=SC2086 # PODS is a word list
      printf 'pod/%s\n' $PODS ;;
    *"jsonpath={.status.phase}"*) printf '%s' "$PHASE" ;;
    *"CapacityAvailable"*) printf '%s' "$CAPACITY" ;;
  esac
}
NS=crew-starter-guide CREW=starter-guide

PODS="starter-guide-coordinator-5d8f7c9b6-x2k4p starter-guide-workloads-7c6d9f8b5d-abcde starter-guide-workloads-7c6d9f8b5d-zzzzz starter-guide-kubernetes-mcp-6b8c5d7f9-q1w2e starter-guide-discussion-0"
check "pod hashes are dropped, replicas and ordinals fold into one workload" \
  "starter-guide-coordinator starter-guide-discussion starter-guide-kubernetes-mcp starter-guide-workloads" \
  "$(workloads | paste -sd' ')"

PHASE=Ready CAPACITY=True;  check "Ready with capacity is ready" true "$(crew_state)"
PHASE=Ready CAPACITY=False; check "Ready without capacity is not ready" false "$(crew_state)"
PHASE=WaitingForCapacity CAPACITY=False; check "WaitingForCapacity is not ready" false "$(crew_state)"
PHASE=Pending CAPACITY=;    check "Pending is not ready" false "$(crew_state)"
PHASE=Ready CAPACITY=;      check "Ready before the capacity condition exists is not ready" false "$(crew_state)"

printf 'starter-guide-coordinator\nstarter-guide-workloads\n' > "$work/w.txt"
status() { if "$@" >/dev/null 2>&1; then echo pass; else echo fail; fi; }
answer() { printf '%s\n' "$1" > "$work/a.txt"; }

answer "Two pods run here: starter-guide-coordinator-5d8f-x2k4p and STARTER-GUIDE-WORKLOADS."
check "names in any case and any surrounding wording pass" pass "$(NAMED_MIN='' status check_answer "$work/a.txt" "$work/w.txt")"
answer "Only starter-guide-workloads is running."
check "a missing pod fails" fail "$(NAMED_MIN='' status check_answer "$work/a.txt" "$work/w.txt")"
check "a lowered NAMED_MIN lets the partial answer count (measuring only)" pass \
  "$(NAMED_MIN=1 status check_answer "$work/a.txt" "$work/w.txt")"
answer "I could not find any pods."
check "an answer naming nothing fails" fail "$(NAMED_MIN='' status check_answer "$work/a.txt" "$work/w.txt")"
answer "The starter-guide crew has pods."
check "the crew's name alone is not a pod" fail "$(NAMED_MIN='' status check_answer "$work/a.txt" "$work/w.txt")"
: > "$work/none.txt"; answer "starter-guide-workloads"
check "no ground truth fails, never passes vacuously" fail "$(NAMED_MIN='' status check_answer "$work/a.txt" "$work/none.txt")"

[ "$failures" -eq 0 ] || { echo "${failures} failed"; exit 1; }
echo "all passed"
