#!/usr/bin/env bash
# Tests for release-vllm-chart.sh: a dry run in scratch repositories that hold the
# chart, with and without earlier candidate tags. Needs helm and RELEASE_LIB (the
# release-lib.sh of kubemoot/release-actions; the CI job sets it).
# Usage: bash .github/scripts/test-release-vllm-chart.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$here/../.." && pwd)"
: "${RELEASE_LIB:?RELEASE_LIB must point to release-lib.sh from kubemoot/release-actions}"

failures=0
check() {
  local name="$1" want="$2" got="$3"
  if [ "$want" = "$got" ]; then
    echo "ok   ${name}"
  else
    echo "FAIL ${name}: want [${want}] got [${got}]"
    failures=$((failures + 1))
  fi
}

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# A repository holding the chart as git does: version 0.0.0.
new_repo() {
  local dir="$work/$1"
  mkdir -p "$dir/vllm/charts"
  cp -r "$repo_root/vllm/charts/kubemoot-vllm" "$dir/vllm/charts/"
  git -C "$dir" init -q
  git -C "$dir" -c user.name=t -c user.email=t@example.com add -A
  git -C "$dir" -c user.name=t -c user.email=t@example.com commit -q -m "chart"
  echo "$dir"
}

# dry DIR [FORCE]: a dry run in DIR; prints the version it stamped.
dry() {
  (cd "$1" && DRY_RUN=true FORCE="${2:-false}" OUT_DIR="$1/out" GITHUB_OUTPUT="$1/output" bash "$here/release-vllm-chart.sh" >/dev/null 2>&1)
  sed -n 's/^chart_version=//p' "$1/output" 2>/dev/null | tail -n 1
}

tag() {
  git -C "$1" -c user.name=t -c user.email=t@example.com tag -a "$2" -m "candidate" -m "${3:-}"
}

# The first release.
repo="$(new_repo first)"
check "the first candidate is 0.1.0-rc.0" "0.1.0-rc.0" "$(dry "$repo")"
check "the packaged chart carries the stamped version" "0.1.0-rc.0" \
  "$(tar -xzOf "$repo/out/kubemoot-vllm-0.1.0-rc.0.tgz" kubemoot-vllm/Chart.yaml | sed -n 's/^version: //p')"
check "git keeps 0.0.0" "version: 0.0.0" "$(grep '^version:' "$repo/vllm/charts/kubemoot-vllm/Chart.yaml")"

# The next candidate counts from the latest tag.
repo="$(new_repo next)"
tag "$repo" vllm-chart-v0.1.0-rc.3
check "the next candidate follows the latest rc" "0.1.0-rc.4" "$(dry "$repo")"

# A published final starts the next patch.
tag "$repo" vllm-chart-v0.1.0
rm -f "$repo/output"
check "a published final starts the next patch" "0.1.1-rc.0" "$(dry "$repo")"

# A feat commit to the chart after the final bumps the minor; a commit elsewhere does not.
git -C "$repo" -c user.name=t -c user.email=t@example.com commit -q --allow-empty -m "chore: elsewhere"
rm -f "$repo/output"
check "a commit outside the chart is a patch rebuild" "0.1.1-rc.0" "$(dry "$repo")"
echo "x: 1" >> "$repo/vllm/charts/kubemoot-vllm/values.yaml"
git -C "$repo" add vllm
git -C "$repo" -c user.name=t -c user.email=t@example.com commit -q -m "feat: a new value"
rm -f "$repo/output"
check "a feat commit to the chart bumps the minor" "0.2.0-rc.0" "$(dry "$repo")"

# A candidate with the same chart sources is current; FORCE builds anyway.
repo="$(new_repo current)"
tree="$(git -C "$repo" rev-parse HEAD:vllm/charts/kubemoot-vllm)"
tag "$repo" vllm-chart-v0.2.0-rc.0 "chart-source ${tree}"
check "a current chart builds nothing" "" "$(dry "$repo")"
rm -f "$repo/output"
check "FORCE builds a current chart" "0.2.0-rc.1" "$(dry "$repo" true)"

if [ "$failures" -ne 0 ]; then
  echo "${failures} failed"
  exit 1
fi
echo "all passed"
