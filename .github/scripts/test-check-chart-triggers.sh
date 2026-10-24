#!/usr/bin/env bash
# Tests for check-chart-triggers.sh against small fake repositories, plus the real one.
# Usage: bash .github/scripts/test-check-chart-triggers.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="${here}/check-chart-triggers.sh"

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

# fake_repo DIR TRIGGER...: a repository whose chart stamps alpha and beta-svc (and an
# external image with a registry), with release workflows for both, and a chart release
# triggered by the given workflow names.
fake_repo() {
  local dir="$1"
  shift
  mkdir -p "${dir}/operator/chart/kubemoot-operator" "${dir}/.github/workflows"
  cat >"${dir}/operator/chart/kubemoot-operator/values.yaml" <<'EOF'
runner:
  image: alpha:0.0.0
images:
  betaSvc: beta-svc:0.0.0
  docling: quay.io/docling/serve:latest
  other: ghcr.io/kubemoot/external:0.0.0
EOF
  printf 'name: Release Alpha\non: push\n' >"${dir}/.github/workflows/release-alpha.yaml"
  printf 'name: Release Beta Svc\non: push\n' >"${dir}/.github/workflows/release-beta-svc.yaml"
  {
    printf 'name: Release Operator Chart\n\non:\n  workflow_run:\n    workflows:\n'
    local t
    for t in "$@"; do printf '      - %s\n' "$t"; done
    printf '    types: [completed]\n'
  } >"${dir}/.github/workflows/release-chart.yaml"
}

status_of() {
  local rc=0
  bash "$script" "$1" >"${work}/out" 2>&1 || rc=$?
  echo "$rc"
}

fake_repo "${work}/all" "Release Alpha" "Release Beta Svc"
check "every image triggers the chart" 0 "$(status_of "${work}/all")"

fake_repo "${work}/one-missing" "Release Alpha"
check "an image whose release is not a trigger fails" 1 "$(status_of "${work}/one-missing")"
check "the failure names the image" 1 "$(grep -c 'MISSING beta-svc' "${work}/out")"

fake_repo "${work}/no-workflow" "Release Alpha" "Release Beta Svc"
rm "${work}/no-workflow/.github/workflows/release-beta-svc.yaml"
check "an image with no release workflow fails" 1 "$(status_of "${work}/no-workflow")"
check "the failure says the workflow is missing" 1 "$(grep -c 'no release workflow' "${work}/out")"

fake_repo "${work}/prefix" "Release Alpha Extra" "Release Beta Svc"
check "a trigger must match the whole name" 1 "$(status_of "${work}/prefix")"

check "the repository's own chart is covered" 0 "$(status_of "${here}/../..")"
cat "${work}/out"

if [ "$failures" -gt 0 ]; then
  echo "${failures} test(s) failed"
  exit 1
fi
echo "all check-chart-triggers tests passed"
