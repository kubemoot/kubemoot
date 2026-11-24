#!/usr/bin/env bash
# Tests for write-schema-assets.sh with the real generator against the real CRDs.
# Usage: bash .github/scripts/test-write-schema-assets.sh (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
operator="${here}/../../operator"
command -v jq >/dev/null || { echo "jq is required"; exit 1; }
command -v go >/dev/null || { echo "go is required"; exit 1; }

failures=0
check() {
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want [$2] got [$3]"; failures=$((failures + 1)); fi
}

root="$(mktemp -d)"
trap 'rm -rf "$root"' EXIT
tag="operator-chart-v1.2.3"

# The assets of a release: dashboards already listed are kept, and each schema is added.
mkdir -p "${root}/files"
printf '%s\t%s\n' "$tag" "dashboards/d.json" > "${root}/files/assets.tsv"
status=0
bash "${here}/write-schema-assets.sh" "${root}/files" "$operator" "$tag" > "${root}/out.log" 2>&1 || status=$?
check "generates the schemas" 0 "$status"
check "keeps the dashboard asset" 1 "$(grep -c dashboards/d.json "${root}/files/assets.tsv")"
check "lists the combined schema" 1 "$(grep -c "^${tag}"$'\t'"schemas/kubemoot_v1alpha1.json$" "${root}/files/assets.tsv")"
check "lists a schema per kind" 1 "$(grep -c "^${tag}"$'\t'"schemas/crew_v1alpha1.json$" "${root}/files/assets.tsv")"
listed="$(grep -c 'schemas/' "${root}/files/assets.tsv")"
check "lists exactly the files written" "$(find "${root}/files/schemas" -name '*.json' | wc -l)" "$listed"
check "the combined schema is JSON Schema" draft-07 "$(jq -r '."$schema"' "${root}/files/schemas/kubemoot_v1alpha1.json" | sed 's#.*/\(draft-07\)/.*#\1#')"

# Run again, the schemas are listed once.
bash "${here}/write-schema-assets.sh" "${root}/files" "$operator" "$tag" > /dev/null 2>&1
check "a second run lists each schema once" "$listed" "$(grep -c 'schemas/' "${root}/files/assets.tsv")"
check "a second run keeps the dashboard asset" 1 "$(grep -c dashboards/d.json "${root}/files/assets.tsv")"

# A source without CRDs stops the run and writes nothing.
mkdir -p "${root}/empty" "${root}/files2"
status=0
bash "${here}/write-schema-assets.sh" "${root}/files2" "${root}/empty" "$tag" > /dev/null 2>&1 || status=$?
check "no CRDs fails" 1 "$status"
check "no CRDs writes no asset list" 0 "$(cat "${root}/files2/assets.tsv" 2>/dev/null | wc -l)"

status=0
bash "${here}/write-schema-assets.sh" > /dev/null 2>&1 || status=$?
check "missing arguments fail" 1 "$status"

[ "$failures" -eq 0 ] || { echo "$failures failed"; exit 1; }
echo "all write-schema-assets tests passed"
