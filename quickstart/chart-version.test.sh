#!/usr/bin/env bash
# Tests for release_chart_version in chart-version.sh, against throwaway git repositories.
# Usage: bash quickstart/chart-version.test.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR source=chart-version.sh
source "$here/chart-version.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
failures=0

check() {
  local name="$1" want="$2" got="$3"
  if [ "$want" = "$got" ]; then
    echo "ok   $name"
  else
    echo "FAIL $name: want [$want] got [$got]"
    failures=$((failures + 1))
  fi
}

# A repository with a quickstart directory; commit helper with a fixed identity.
repo() {
  local dir="$work/$1"
  mkdir -p "$dir/quickstart"
  git -C "$dir" init -q
  git -C "$dir" config user.name test
  git -C "$dir" config user.email test@example.invalid
  git -C "$dir" config commit.gpgsign false
  git -C "$dir" config tag.gpgsign false
  echo "kind: ModelServer" >"$dir/quickstart/modelserver.yaml"
  echo "$dir"
}
commit() { git -C "$1" add -A && git -C "$1" commit -q -m "$2"; }
# version DIR: the function's output, or "fail" when it refuses.
version() { release_chart_version "$1" 2>/dev/null || echo fail; }

# Exactly at a release.
d="$(repo at-release)"; commit "$d" one
git -C "$d" tag operator-chart-v1.2.3
check "a checkout at a release tag gives its version" 1.2.3 "$(version "$d/quickstart")"

# Release candidates are not releases.
d="$(repo rc-only)"; commit "$d" one
git -C "$d" tag operator-chart-v1.2.4-rc.7
check "a release candidate tag alone gives nothing" fail "$(version "$d")"

# Commits after the release that leave the quickstart files alone.
d="$(repo ahead-same)"; commit "$d" one
git -C "$d" tag operator-chart-v1.2.3
echo "x" >"$d/other.txt"; commit "$d" two
git -C "$d" tag operator-chart-v1.2.4-rc.1
check "later commits that leave the quickstart alone keep the release version" 1.2.3 "$(version "$d")"

# The README is not an input to the cluster.
echo "words" >"$d/quickstart/README.md"; commit "$d" three
check "a README change does not matter" 1.2.3 "$(version "$d")"
echo "# test" >"$d/quickstart/x.test.sh"; commit "$d" four
check "a test script change does not matter" 1.2.3 "$(version "$d")"

# The quickstart changed after the release: refuse, in a commit and in the working tree.
d="$(repo ahead-changed)"; commit "$d" one
git -C "$d" tag operator-chart-v1.2.3
echo "kind: ModelServer2" >"$d/quickstart/modelserver.yaml"
check "an uncommitted quickstart change is refused" fail "$(version "$d")"
commit "$d" two
check "a committed quickstart change is refused" fail "$(version "$d")"

# The nearest release wins over an older one.
d="$(repo two-releases)"; commit "$d" one
git -C "$d" tag operator-chart-v1.2.3
echo "kind: ModelServer2" >"$d/quickstart/modelserver.yaml"; commit "$d" two
git -C "$d" tag operator-chart-v1.3.0
check "the nearest release tag is used" 1.3.0 "$(version "$d")"

# A tag of another component is not a chart release.
d="$(repo other-tag)"; commit "$d" one
git -C "$d" tag agent-runtime-v9.9.9
check "another component's tag gives nothing" fail "$(version "$d")"

# Not a checkout at all.
mkdir -p "$work/plain"
check "a directory outside git gives nothing" fail "$(version "$work/plain")"

[ "$failures" -eq 0 ] || { echo "$failures failed"; exit 1; }
echo "all passed"
