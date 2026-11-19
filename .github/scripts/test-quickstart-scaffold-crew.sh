#!/usr/bin/env bash
# Tests for quickstart-scaffold-crew.sh: the answer parsing and the check against the cluster.
# Usage: bash .github/scripts/test-quickstart-scaffold-crew.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=/dev/null
source "${here}/quickstart-scaffold-crew.sh"
failures=0
check() {
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want [$2] got [$3]"; failures=$((failures + 1)); fi
}
status() { if "$@" >/dev/null 2>&1; then echo 0; else echo 1; fi; }

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

stream='{"type":"conversation","conversationId":"c1"}
{"type":"finding","agent":"a"}
{"type":"result","conversationId":"c1","answer":"two pods\nsg-coordinator"}'
check "the answer is the result line's" "$(printf 'two pods\nsg-coordinator')" "$(answer_of <<<"$stream")"
check "a stream without a result fails" "1" "$(status answer_of <<<'{"type":"conversation"}')"

# Agents, a blank line, then everything in the namespace.
printf 'sg-coordinator\nsg-workloads\n\nsg-coordinator\nsg-coordinator-7d9f-abc12\nsg-workloads\nsg-workloads-5c-xyz99\nsg-gateway\nsg-discussion\n' > "$tmp/names"
verdict() { printf '%s\n' "$1" > "$tmp/answer"; status check_answer sg "$tmp/answer" "$tmp/names"; }

check "every agent named, full pod names" "0" "$(verdict 'sg-coordinator-7d9f-abc12 Running; sg-workloads-5c-xyz99 Running; sg-gateway Running')"
check "agent base names alone pass" "0" "$(verdict 'sg-coordinator, sg-workloads')"
check "a missing agent fails" "1" "$(verdict 'sg-coordinator is Running')"
check "an invented name fails" "1" "$(verdict 'sg-coordinator, sg-workloads, sg-reviewer-1a2b')"
check "an invented hash fails" "1" "$(verdict 'sg-coordinator-ffff-00000, sg-workloads')"
check "other crews' names are not checked" "0" "$(verdict 'sg-coordinator, sg-workloads, kube-proxy-abc')"
check "a trailing hyphen or period is not part of the name" "0" "$(verdict 'Pods: sg-coordinator-, sg-workloads.')"

[ "$failures" -eq 0 ] || { echo "${failures} failed"; exit 1; }
echo "all passed"
