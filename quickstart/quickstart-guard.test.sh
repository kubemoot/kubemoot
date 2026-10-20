#!/usr/bin/env bash
# Tests for quickstart.sh's release-match guard: it runs the install when the quickstart
# files match the release tag, and refuses with a clear message when they differ or when
# no release tag is reachable. The real script runs in a throwaway git repository with
# stub kubectl and helm, so nothing touches a cluster.
# Usage: bash quickstart/quickstart-guard.test.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
failures=0

# Stubs: kubectl and helm record that the script got past the guard, then stop it.
mkdir -p "$work/bin"
for tool in kubectl helm; do
  printf '#!/bin/sh\necho "STUB-%s $*"\nexit 99\n' "$tool" >"$work/bin/$tool"
  chmod +x "$work/bin/$tool"
done

# A repository holding a copy of the real quickstart directory.
repo="$work/repo"
mkdir -p "$repo"
cp -r "$here" "$repo/quickstart"
git -C "$repo" init -q
git -C "$repo" config user.name test
git -C "$repo" config user.email test@example.invalid
git -C "$repo" config commit.gpgsign false
git -C "$repo" config tag.gpgsign false
git -C "$repo" add -A
git -C "$repo" commit -q -m one

# run [ENV=VALUE...]: the script's exit code and combined output.
run() {
  env PATH="$work/bin:$PATH" "$@" bash "$repo/quickstart/quickstart.sh" 2>&1 && echo "rc=0" || echo "rc=$?"
}
check() { # name, expected substring, output
  if grep -qF -- "$2" <<<"$3"; then echo "ok   $1"; else
    echo "FAIL $1: want [$2] in:"; echo "$3"; failures=$((failures + 1)); fi
}
refuse() { # name, expected substring, output
  check "$1" "rc=2" "$3"
  check "$1 (message)" "$2" "$3"
  if grep -qF STUB- <<<"$3"; then echo "FAIL $1: ran past the guard"; failures=$((failures + 1)); fi
}

# No release tag reachable.
out="$(run KUBEMOOT_UNUSED=1)"
refuse "no reachable release tag" "no release tag" "$out"
check "no reachable release tag (explains the stop)" "cannot choose the chart version" "$out"

# At a release: runs, and uses the tag's version.
git -C "$repo" tag operator-chart-v1.2.3
out="$(run KUBEMOOT_UNUSED=1)"
check "files match the release: the script proceeds" "STUB-kubectl" "$out"
check "files match the release: the tag's version is announced" "chart version 1.2.3" "$out"

# An explicit version skips the guard.
out="$(run KUBEMOOT_CHART_VERSION=9.9.9)"
check "an explicit version skips the guard" "STUB-kubectl" "$out"

# A local chart path skips the guard.
out="$(run KUBEMOOT_CHART="$repo/quickstart")"
check "a local chart path skips the guard" "STUB-" "$out"

# Quickstart files changed after the release: refused with the reason.
echo "# changed" >>"$repo/quickstart/operator-values.yaml"
out="$(run KUBEMOOT_UNUSED=1)"
refuse "files differ from the release" "differ from the latest release (operator-chart-v1.2.3)" "$out"
out="$(run KUBEMOOT_CHART_VERSION=9.9.9)"
check "an explicit version still overrides a mismatch" "STUB-kubectl" "$out"

[ "$failures" -eq 0 ] || { echo "$failures failed"; exit 1; }
echo "all passed"
