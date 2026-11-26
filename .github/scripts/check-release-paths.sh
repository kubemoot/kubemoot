#!/usr/bin/env bash
# Checks that the push path filter of every component's release workflow covers every source
# directory the component is built from, so a change to a shared package rebuilds the image
# (and the chart pairing sees the image as behind) instead of leaving it stale:
#   - a Go module that replaces the operator module (replace ... => ../operator): every
#     operator package the module imports, directly or not (go list -deps), and the
#     operator's go.mod and go.sum, whose dependencies the module builds with;
#   - a Gradle build that includes another (includeBuild("../NAME")): NAME's sources.
# The filter is the workflow's on.push.paths, read the way chart-image-pairing.sh reads it.
# It does not flag a filter that is broader than needed; it fails on one that is too narrow.
#
# Usage: check-release-paths.sh [REPO_ROOT]   (exit 0 = every filter covers its sources)
# Needs go on PATH for the Go modules. Tested by test-check-release-paths.sh.
set -euo pipefail

# shellcheck source=/dev/null
source "$(dirname "${BASH_SOURCE[0]}")/chart-image-pairing.sh"

root="$(cd "${1:-.}" && pwd)"
operator_module="github.com/kubemoot/kubemoot/operator"
missing=0

# covered PATTERNS FILE: true when the release filter includes the repository-relative FILE.
covered() {
  [ -n "$(touches_filter "$1" "$2")" ]
}

# report MODULE SOURCE: one line for a source the module's release filter does not cover.
report() {
  echo "MISSING ${1}: ${2} is built into the image but release-${1}.yaml's push paths do not include it"
  missing=$((missing + 1))
}

# filter_of MODULE: the push paths of the module's release workflow; fails with a message
# when the workflow is missing or its filter cannot be read.
filter_of() {
  local workflow="${root}/.github/workflows/release-${1}.yaml" patterns
  [ -f "$workflow" ] || { echo "MISSING ${1}: no .github/workflows/release-${1}.yaml" >&2; return 1; }
  patterns="$(workflow_push_paths < "$workflow")" || { echo "MISSING ${1}: release-${1}.yaml has a push filter this check cannot read" >&2; return 1; }
  [ -n "$patterns" ] || { echo "MISSING ${1}: release-${1}.yaml has no on.push.paths" >&2; return 1; }
  printf '%s' "$patterns"
}

# check_go_module MODULE: the operator packages MODULE builds, as repository-relative files.
check_go_module() {
  local module="$1" patterns dir files file rel
  patterns="$(filter_of "$module")" || { missing=$((missing + 1)); return; }
  for file in operator/go.mod operator/go.sum; do
    covered "$patterns" "$file" || report "$module" "$file"
  done
  while IFS=$'\t' read -r dir files; do
    for file in $files; do
      rel="${dir#"${root}"/}/${file}"
      if ! covered "$patterns" "$rel"; then
        report "$module" "${dir#"${root}"/}/"
        break
      fi
    done
  done < <(cd "${root}/${module}" && go list -deps -f '{{with .Module}}{{if eq .Path "'"${operator_module}"'"}}{{$.Dir}}{{"\t"}}{{join $.GoFiles " "}}{{end}}{{end}}' ./... | grep -v '^$')
}

# check_gradle_module MODULE: the builds it includes.
check_gradle_module() {
  local module="$1" patterns included
  patterns="$(filter_of "$module")" || { missing=$((missing + 1)); return; }
  while read -r included; do
    covered "$patterns" "${included}/build.gradle.kts" || report "$module" "${included}/"
  done < <(sed -nE 's/^[[:space:]]*includeBuild\("\.\.\/([A-Za-z0-9_-]+)"\).*$/\1/p' "${root}/${module}/settings.gradle.kts")
}

for gomod in "${root}"/*/go.mod; do
  module="$(basename "$(dirname "$gomod")")"
  grep -qE "^replace ${operator_module} => \.\./operator" "$gomod" || continue
  check_go_module "$module"
done
for settings in "${root}"/*/settings.gradle.kts; do
  module="$(basename "$(dirname "$settings")")"
  grep -qE '^[[:space:]]*includeBuild\("\.\./' "$settings" || continue
  check_gradle_module "$module"
done

if [ "$missing" -gt 0 ]; then
  echo "${missing} source(s) would not rebuild their image when changed"
  exit 1
fi
echo "every release filter covers the sources its image is built from"
