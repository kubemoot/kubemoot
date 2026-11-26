#!/usr/bin/env bash
# Tests for check-release-paths.sh against a small synthetic repository: an operator module
# with packages a (imports b) and c, a component that imports a, and a Gradle build that
# includes another build.
# Usage: bash .github/scripts/test-check-release-paths.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="${here}/check-release-paths.sh"

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

# build_repo DIR: the synthetic repository with release workflows that cover everything.
build_repo() {
  local r="$1"
  mkdir -p "$r/operator/pkg/a" "$r/operator/pkg/b" "$r/operator/pkg/c" "$r/comp" "$r/gcomp" "$r/gcore" "$r/.github/workflows"
  printf 'module github.com/kubemoot/kubemoot/operator\n\ngo 1.21\n' > "$r/operator/go.mod"
  printf 'package b\n\nfunc B() {}\n' > "$r/operator/pkg/b/b.go"
  printf 'package a\n\nimport "github.com/kubemoot/kubemoot/operator/pkg/b"\n\nfunc A() { b.B() }\n' > "$r/operator/pkg/a/a.go"
  printf 'package c\n\nfunc C() {}\n' > "$r/operator/pkg/c/c.go"
  printf 'module example.com/comp\n\ngo 1.21\n\nrequire github.com/kubemoot/kubemoot/operator v0.0.0\n\nreplace github.com/kubemoot/kubemoot/operator => ../operator\n' > "$r/comp/go.mod"
  printf 'package main\n\nimport "github.com/kubemoot/kubemoot/operator/pkg/a"\n\nfunc main() { a.A() }\n' > "$r/comp/main.go"
  printf 'includeBuild("../gcore")\n' > "$r/gcomp/settings.gradle.kts"
  : > "$r/gcore/build.gradle.kts"
  workflow comp "'comp/**'" "'operator/pkg/a/**'" "'operator/pkg/b/**'" "'operator/go.mod'" "'operator/go.sum'"
  workflow gcomp "'gcomp/**'" "'gcore/**'"
}

# workflow NAME PATTERN...: writes release-NAME.yaml with those push paths (repo in $repo).
workflow() {
  local name="$1" p
  shift
  {
    printf 'name: Release %s\non:\n  push:\n    branches: [main]\n    paths:\n' "$name"
    for p in "$@"; do printf '      - %s\n' "$p"; done
    printf '  workflow_dispatch:\njobs: {}\n'
  } > "${repo}/.github/workflows/release-${name}.yaml"
}

run() { bash "$script" "$repo" 2>&1 || echo "exit=$?"; }

repo="${work}/ok"; build_repo "$repo"
out="$(run)"
check "covered" "every release filter covers the sources its image is built from" "$out"

# A transitive dependency (b, imported by a) missing from the filter.
repo="${work}/transitive"; build_repo "$repo"
workflow comp "'comp/**'" "'operator/pkg/a/**'" "'operator/go.mod'" "'operator/go.sum'"
out="$(run)"
check "transitive missing" "1" "$(grep -c '^MISSING comp: operator/pkg/b/ ' <<<"$out")"
check "transitive exit" "exit=1" "$(tail -1 <<<"$out")"

# A package the component does not import (c) in the filter is allowed: broader is not an error.
repo="${work}/broad"; build_repo "$repo"
workflow comp "'comp/**'" "'operator/pkg/**'" "'operator/go.mod'" "'operator/go.sum'"
check "broader filter" "every release filter covers the sources its image is built from" "$(run)"

# A negated pattern after the include removes the files.
repo="${work}/negated"; build_repo "$repo"
workflow comp "'comp/**'" "'operator/pkg/**'" "'!operator/pkg/b/**'"
check "negated" "1" "$(grep -c '^MISSING comp: operator/pkg/b/ ' <<<"$(run)")"

# The operator's go.mod and go.sum missing: their dependencies are built into the image.
repo="${work}/gomod"; build_repo "$repo"
workflow comp "'comp/**'" "'operator/pkg/**'"
out="$(run)"
check "go.mod missing" "1" "$(grep -c '^MISSING comp: operator/go.mod ' <<<"$out")"
check "go.sum missing" "1" "$(grep -c '^MISSING comp: operator/go.sum ' <<<"$out")"

# A Gradle included build missing from the filter.
repo="${work}/gradle"; build_repo "$repo"
workflow gcomp "'gcomp/**'"
check "gradle missing" "1" "$(grep -c '^MISSING gcomp: gcore/ ' <<<"$(run)")"

# No release workflow, and a filter that cannot be read.
repo="${work}/noworkflow"; build_repo "$repo"; rm "${repo}/.github/workflows/release-comp.yaml"
check "no workflow" "1" "$(grep -c '^MISSING comp: no .github/workflows/release-comp.yaml' <<<"$(run)")"
repo="${work}/nopaths"; build_repo "$repo"
printf 'name: Release comp\non:\n  push:\n    branches: [main]\n' > "${repo}/.github/workflows/release-comp.yaml"
check "no paths" "1" "$(grep -c 'has no on.push.paths' <<<"$(run)")"

if [ "$failures" -gt 0 ]; then
  echo "${failures} failure(s)"
  exit 1
fi
echo "all passed"
