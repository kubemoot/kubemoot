#!/usr/bin/env bash
# Tests for chart-image-pairing.sh: the path filters read from a release workflow, and the
# decision whether a pinned image is older than the code a chart is packaged from, in
# scratch repositories.
# Usage: bash .github/scripts/test-chart-image-pairing.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=/dev/null
source "${here}/chart-image-pairing.sh"

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

g() { git -C "$repo" -c user.name=t -c user.email=t@example.com "$@"; }

# commit FILE...: a commit touching the files (created or appended to).
commit() {
  local f
  for f in "$@"; do
    mkdir -p "$repo/$(dirname "$f")"
    echo "x" >>"$repo/$f"
  done
  g add -A
  g commit -q -m "fix: change ${*}"
}

# A repository with the operator workflow (operator/** minus markdown, and a shared
# script) and an indexer workflow, both tagged at the first commit.
repo="${work}/r"
mkdir -p "$repo/.github/workflows"
git -C "$repo" init -q
cat >"$repo/.github/workflows/release.yaml" <<'YAML'
name: Release

on:
  push:
    branches: [main]
    paths:
      - 'operator/**'
      - '!operator/**.md'
      - '.github/scripts/retag-image.sh'
  workflow_dispatch:
    inputs:
      force_release:
        default: 'false'
YAML
cat >"$repo/.github/workflows/release-indexer.yaml" <<'YAML'
name: Release Indexer

on:
  push:
    branches: [main]
    paths:
      - 'indexer/**'
      - '!indexer/**.md'
YAML
commit operator/main.go indexer/app.java
g tag -a v0.357.0-rc.27 -m c
g tag -a indexer-v1.0.0-rc.1 -m c
pins=$'kubemoot-operator 0.357.0-rc.27\nindexer 1.0.0-rc.1\ndashboard/test-runner 3.0.0'

check "path filters: includes and excludes" \
  $'operator/**\n:(exclude)operator/**.md\n.github/scripts/retag-image.sh' \
  "$(workflow_pathspecs <"$repo/.github/workflows/release.yaml")"

cd "$repo"
tip() { git rev-parse HEAD; }
status_of() { local rc=0; stale_pins "$(tip)" "$1" >"${work}/out" 2>&1 || rc=$?; echo "$rc"; }

pins_status() { local rc=0; pins_are_current "$(tip)" "$1" >/dev/null 2>&1 || rc=$?; echo "$rc"; }
check "images built from the tip are current" 0 "$(pins_status "$pins")"
status_of "$pins" >/dev/null
check "no problem lines when current" "" "$(cat "${work}/out")"

# A commit under the operator filter after the operator tag, with no newer tag (the
# image build did not finish).
commit operator/chart/templates/deployment.yaml
check "a commit under the operator filter makes the operator image stale" 1 "$(pins_status "$pins")"
check "stale_pins itself still returns 0 (problems are lines)" 0 "$(status_of "$pins")"
check "the stale line names the operator" 1 "$(grep -c '^kubemoot-operator 0.357.0-rc.27: 1 commit' "${work}/out")"
check "the indexer stays current" 0 "$(grep -c '^indexer' "${work}/out")"
check "pins_are_current refuses" 1 "$(pins_status "$pins")"

# stale_pins prints and returns 0 for stale; confirm a new tag brings it back.
commit operator/pkg/x.go
g tag -a v0.357.0-rc.28 -m c
check "the new operator tag at the tip is current" 0 "$(pins_status $'kubemoot-operator 0.357.0-rc.28\nindexer 1.0.0-rc.1')"
check "the old operator tag is still stale" 1 "$(pins_status "$pins")"

# A commit the filter ignores (markdown, other component paths, docs) never holds the chart.
commit operator/README.md
commit docs/guide.md
commit crews/x.yaml
check "ignored paths do not make it stale" 0 \
  "$(pins_status $'kubemoot-operator 0.357.0-rc.28\nindexer 1.0.0-rc.1')"

# A shared file in the filter does.
commit .github/scripts/retag-image.sh
check "a shared file in the filter makes the operator stale" 1 \
  "$(pins_status $'kubemoot-operator 0.357.0-rc.28')"
check "the indexer filter does not include the shared file" 0 \
  "$(pins_status $'indexer 1.0.0-rc.1')"

# Unexpected input fails closed.
check "an unknown component is undecidable" 2 "$(pins_status $'ghost 1.0.0')"
check "a missing tag is undecidable" 2 "$(pins_status $'indexer 9.9.9')"
check "no pins is current" 0 "$(pins_status "")"
check "the dashboard public-registry pins are skipped" 0 \
  "$(pins_status $'dashboard/ghost 1.0.0')"

# Comments and blank lines inside the list, and a trailing comment on an item.
commented=$'on:\n  push:\n    paths:\n      # the sources\n      - \'a/**\'  # trailing\n\n      - "!a/**.md"\n      # more\n      - b/x.sh\n  workflow_dispatch:\n'
check "comments and blanks do not cut or taint the filter" $'a/**\n:(exclude)a/**.md\nb/x.sh' "$(workflow_pathspecs <<<"$commented")"
check "a push without paths has no filter" "" "$(workflow_pathspecs <<<$'on:\n  push:\n    branches: [main]\n  workflow_dispatch:\n')"

# The dashboard candidate pin maps to release-dashboard.yaml and dashboard-v tags.
mkdir -p .github/workflows
printf 'on:\n  push:\n    paths:\n      - %s\n' "'dashboard/**'" >.github/workflows/release-dashboard.yaml
printf 'on:\n  push:\n    branches: [main]\n' >.github/workflows/release-nopaths.yaml
commit dashboard/a.ts
g tag -a dashboard-v2.0.0-rc.1 -m c
check "the dashboard pin is current at its tag" 0 "$(pins_status $'dashboard 2.0.0-rc.1')"
commit dashboard/b.ts
check "a dashboard commit after its tag is stale" 1 "$(pins_status $'dashboard 2.0.0-rc.1')"
check "a workflow with no paths is undecidable" 2 "$(pins_status $'nopaths 1.0.0')"

# Every image the real chart pins has a release workflow with a path filter.
real="$(cd "$here/../.." && pwd)"
while read -r image; do
  check "real workflow of ${image} has a path filter" yes \
    "$(workflow_pathspecs <"$real/.github/workflows/$(component_workflow "$image")" | grep -q . && echo yes || echo no)"
done < <(grep -oE '[a-z0-9-]+:0\.0\.0' "$real/operator/chart/kubemoot-operator/values.yaml" | sed 's/:0.0.0//' | sort -u)

if [ "$failures" -gt 0 ]; then
  echo "${failures} failure(s)"
  exit 1
fi
echo "all passed"
