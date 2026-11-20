#!/usr/bin/env bash
# Tests for choose_ref and tag_names in kmctl-ref.sh.
# Usage: bash quickstart/kmctl-ref.test.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR source=kmctl-ref.sh
source "$here/kmctl-ref.sh"
failures=0

check() {
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want [$2] got [$3]"; failures=$((failures + 1)); fi
}
# pick MODE TAGS...: the chosen tag, or "fail".
pick() { local mode="$1"; shift; printf '%s\n' "$@" | choose_ref "$mode" 2>/dev/null || echo fail; }

check "candidate: the highest rc of the highest unreleased version" "v0.21.0-rc.10" \
  "$(pick candidate v0.20.0 v0.20.1-rc.9 v0.21.0-rc.2 v0.21.0-rc.10 v0.21.0-rc.9)"
check "candidate: rc numbers compare as numbers" "v0.21.0-rc.10" \
  "$(pick candidate v0.20.0 v0.21.0-rc.9 v0.21.0-rc.10)"
check "candidate: a version bump beats older rc numbers" "v0.22.0-rc.0" \
  "$(pick candidate v0.20.0 v0.21.0-rc.12 v0.22.0-rc.0)"
check "candidate: only rcs of the released version are refused" "fail" \
  "$(pick candidate v0.20.0 v0.20.0-rc.3)"
check "candidate: rcs below the last release are refused" "fail" \
  "$(pick candidate v0.20.1 v0.20.0-rc.3)"
check "candidate: no rc tag is refused" "fail" "$(pick candidate v0.20.0)"
check "candidate: no tags at all is refused" "fail" "$(pick candidate)"
check "candidate: with no final yet, the newest rc" "v0.1.0-rc.2" "$(pick candidate v0.1.0-rc.1 v0.1.0-rc.2)"
check "final: the highest final, rcs ignored" "v0.20.10" "$(pick final v0.20.9 v0.20.10 v0.21.0-rc.1)"
check "final: no final is refused" "fail" "$(pick final v0.21.0-rc.1)"
check "an unknown mode is refused" "fail" "$(pick latest v0.20.0)"

ls_remote="$(printf '%s\trefs/tags/v0.20.1-rc.9\n%s\trefs/tags/v0.20.1-rc.9^{}\n' \
  "$(printf 'a%.0s' {1..40})" "$(printf 'b%.0s' {1..40})")"
check "tag_names drops peeled entries" "v0.20.1-rc.9" "$(tag_names <<<"$ls_remote")"

[ "$failures" -eq 0 ] || { echo "${failures} failed"; exit 1; }
echo "all passed"
