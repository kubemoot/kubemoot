#!/usr/bin/env bash
# Tests for chart-image-pairing.sh (the path filters read from a release workflow and
# the decision whether a pinned image is older than the code a chart is packaged from)
# and for the branches it adds to release-operator-chart.sh, in scratch repositories.
# The release-operator-chart.sh tests need helm and RELEASE_LIB (the release-lib.sh of
# kubemoot/release-actions; the CI job sets it).
# Usage: bash .github/scripts/test-chart-image-pairing.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
real="$(cd "$here/../.." && pwd)"
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

# ---------------------------------------------------------------- the filter parser

pf() { workflow_push_paths <<<"$1" 2>/dev/null; }
pf_status() { local rc=0; workflow_push_paths <<<"$1" >/dev/null 2>&1 || rc=$?; echo "$rc"; }

check "path filters keep the order and the exclusions" $'operator/**\n!operator/**.md\n.github/scripts/retag-image.sh' \
  "$(pf $'on:\n  push:\n    branches: [main]\n    paths:\n      - \'operator/**\'\n      - \'!operator/**.md\'\n      - \'.github/scripts/retag-image.sh\'\n  workflow_dispatch:\n')"
check "comments and blanks do not cut or taint the filter" $'a/**\n!a/**.md\nb/x.sh' \
  "$(pf $'on:\n  push:\n    paths:\n      # the sources\n      - \'a/**\'  # trailing\n\n      - "!a/**.md"\n      # more\n      - b/x.sh\n  workflow_dispatch:\n')"
check "a quoted entry keeps a # inside the quotes" "a/#b" "$(pf $'on:\n  push:\n    paths:\n      - \'a/#b\'  # note\n')"
check "trailing whitespace on an unquoted entry is dropped" "a/**" "$(pf $'on:\n  push:\n    paths:\n      - a/**   \n')"
check "CRLF line endings are dropped" $'a/**\nb/x.sh' "$(pf $'on:\r\n  push:\r\n    paths:\r\n      - a/**\r\n      - \'b/x.sh\'\r\n  workflow_dispatch:\r\n')"
check "a push without paths has no filter" "" "$(pf $'on:\n  push:\n    branches: [main]\n  workflow_dispatch:\n')"
check "pull_request paths before push are not the push filter" "a/**" \
  "$(pf $'on:\n  pull_request:\n    paths:\n      - zzz/**\n  push:\n    paths:\n      - a/**\n')"
check "pull_request paths after push are not the push filter" "a/**" \
  "$(pf $'on:\n  push:\n    paths:\n      - a/**\n  pull_request:\n    paths:\n      - zzz/**\n')"
check "paths-ignore is refused" 3 "$(pf_status $'on:\n  push:\n    paths-ignore:\n      - docs/**\n')"
check "a [ pattern is refused" 3 "$(pf_status $'on:\n  push:\n    paths:\n      - \'a/[ab].go\'\n')"

# ---------------------------------------------------------------- the matcher

tf() { touches_filter "$1" "$2"; }
check "** crosses directories" yes "$(tf 'operator/**' $'operator/pkg/x.go')"
check "a single * stays in its directory" "" "$(tf 'operator/*.go' $'operator/pkg/x.go')"
check "a single * matches within the directory" yes "$(tf 'operator/*.go' $'operator/x.go')"
check "a later ! pattern excludes" "" "$(tf $'operator/**\n!operator/**.md' $'operator/docs/a.md')"
check "a ! pattern before the include does not exclude" yes "$(tf $'!operator/**.md\noperator/**' $'operator/docs/a.md')"
check "a . is literal" "" "$(tf 'a.b' $'axb')"
check "the matcher finds one file among many" yes "$(tf 'k/**' $'z/1\nk/2')"

# ---------------------------------------------------------------- stale_pins

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

wf() { # wf NAME PATHLINES: a release workflow whose push filter is PATHLINES
  mkdir -p "$repo/.github/workflows"
  printf 'name: %s\n\non:\n  push:\n    branches: [main]\n    paths:\n%s\n  workflow_dispatch:\n' "$1" "$3" >"$repo/.github/workflows/$2"
}

repo="${work}/r"
mkdir -p "$repo"
git -C "$repo" init -q
wf Release release.yaml "      - 'operator/**'
      - '!operator/**.md'
      - '.github/scripts/retag-image.sh'"
wf "Release Indexer" release-indexer.yaml "      - 'indexer/**'
      - '!indexer/**.md'"
commit operator/main.go indexer/app.java
g tag -a v0.357.0-rc.27 -m c
g tag indexer-v1.0.0-rc.1 # a lightweight tag
pins=$'kubemoot-operator 0.357.0-rc.27\nindexer 1.0.0-rc.1\ndashboard/test-runner 3.0.0'

cd "$repo"
tip() { git rev-parse HEAD; }
status_of() { local rc=0; stale_pins "$(tip)" "$1" >"${work}/out" 2>&1 || rc=$?; echo "$rc"; }
pins_status() { local rc=0; pins_are_current "$(tip)" "$1" >"${work}/err" 2>&1 || rc=$?; echo "$rc"; }

check "images built from the tip are current" 0 "$(pins_status "$pins")"
status_of "$pins" >/dev/null
check "no problem lines when current" "" "$(cat "${work}/out")"

# A commit under the operator filter after the operator tag, with no newer tag (the
# image build did not finish).
commit operator/chart/templates/deployment.yaml
check "a commit under the operator filter makes the operator image stale" 1 "$(pins_status "$pins")"
check "stale_pins itself returns 0 (problems are lines)" 0 "$(status_of "$pins")"
check "the stale line names the operator and the recovery command" 1 \
  "$(grep -c '^kubemoot-operator 0.357.0-rc.27: 1 commit.*gh workflow run release.yaml -f force_release=true' "${work}/out")"
check "the indexer stays current" 0 "$(grep -c '^indexer' "${work}/out")"

commit operator/pkg/x.go
g tag -a v0.357.0-rc.28 -m c
check "a new operator tag at the tip is current" 0 "$(pins_status $'kubemoot-operator 0.357.0-rc.28\nindexer 1.0.0-rc.1')"
check "the old operator tag is still stale" 1 "$(pins_status "$pins")"

# Commits the filter ignores never hold the chart.
commit operator/README.md
commit docs/guide.md
commit crews/x.yaml
check "ignored paths do not make it stale" 0 "$(pins_status $'kubemoot-operator 0.357.0-rc.28\nindexer 1.0.0-rc.1')"

commit .github/scripts/retag-image.sh
check "a shared file in the filter makes the operator stale" 1 "$(pins_status $'kubemoot-operator 0.357.0-rc.28')"
check "the indexer filter does not include the shared file" 0 "$(pins_status $'indexer 1.0.0-rc.1')"

# The filter is judged as it was at each commit: a commit under a path that was not in
# the filter does not hold the chart once a later commit adds the path to the filter.
commit newdir/a.go
g tag -a v0.357.0-rc.29 -m c
commit newdir/b.go
check "a path outside the filter at its commit is not stale" 0 "$(pins_status $'kubemoot-operator 0.357.0-rc.29')"
wf Release release.yaml "      - 'operator/**'
      - '!operator/**.md'
      - '.github/scripts/retag-image.sh'
      - 'newdir/**'"
g add -A
g commit -q -m "ci: the filter gains newdir"
check "a filter that gains a path later does not hold earlier commits" 0 "$(pins_status $'kubemoot-operator 0.357.0-rc.29')"
commit newdir/c.go
check "a commit under the new filter path is stale" 1 "$(pins_status $'kubemoot-operator 0.357.0-rc.29')"

# Unexpected input is undecidable, never current.
check "an unknown component is undecidable" 2 "$(pins_status $'ghost 1.0.0')"
check "a missing tag is undecidable" 2 "$(pins_status $'indexer 9.9.9')"
check "the missing-tag line names the fetch" 1 "$(grep -c 'git fetch --tags' "${work}/err")"
check "no pins is current" 0 "$(pins_status "")"
check "public-registry dashboard pins are skipped" 0 "$(pins_status $'dashboard/indexer 1.0.0\ndashboard/ghost 1.0.0')"
check "stale and undecidable together report undecidable" 2 "$(pins_status $'kubemoot-operator 0.357.0-rc.29\nghost 1.0.0')"
check "both lines are reported" 2 "$(grep -c 'kubemoot-operator 0.357.0-rc.29: \|^ghost' "${work}/err")"

# The dashboard candidate pin maps to release-dashboard.yaml and dashboard-v tags.
wf "Release Dashboard" release-dashboard.yaml "      - 'dashboard/**'"
printf 'name: x\non:\n  push:\n    branches: [main]\n' >.github/workflows/release-nopaths.yaml
printf 'name: x\non:\n  push:\n    paths-ignore:\n      - docs/**\n' >.github/workflows/release-ignore.yaml
commit dashboard/a.ts
g tag -a dashboard-v2.0.0-rc.1 -m c
check "the dashboard pin is current at its tag" 0 "$(pins_status $'dashboard 2.0.0-rc.1')"
commit dashboard/b.ts
check "a dashboard commit after its tag is stale" 1 "$(pins_status $'dashboard 2.0.0-rc.1')"
g tag nopaths-v1.0.0
g tag ignore-v1.0.0
check "a workflow with no paths is undecidable" 2 "$(pins_status $'nopaths 1.0.0')"
check "a workflow with paths-ignore is undecidable" 2 "$(pins_status $'ignore 1.0.0')"

# A git failure is undecidable, never current: a tag naming an object that is gone.
echo "0000000000000000000000000000000000000001" >"$repo/.git/refs/tags/indexer-v7.0.0"
check "a tag with a missing object is undecidable" 2 "$(pins_status $'indexer 7.0.0')"
rm -f "$repo/.git/refs/tags/indexer-v7.0.0"

cd "$here"

# ---------------------------------------------------------------- the real repository

# Every image the real chart stamps has a release workflow with a filter and the tag
# prefix its workflow publishes, and the operator's release triggers the chart release.
while read -r image; do
  workflow="$real/.github/workflows/$(component_workflow "$image")"
  check "real workflow of ${image} has a path filter" yes \
    "$(workflow_push_paths <"$workflow" | grep -q . && echo yes || echo no)"
  check "real workflow of ${image} publishes the tag prefix the chart reads" 1 \
    "$(grep -c "tag_prefix: \"$(component_tag_prefix "$image")\"" "$workflow")"
done < <(stamped_images "$real/operator/chart/kubemoot-operator/values.yaml")
check "the operator release has a path filter" yes \
  "$(workflow_push_paths <"$real/.github/workflows/release.yaml" | grep -q . && echo yes || echo no)"
check "the operator release publishes the tag prefix the chart reads" 1 \
  "$(grep -c "tag_prefix: \"$(component_tag_prefix kubemoot-operator)\"" "$real/.github/workflows/release.yaml")"
check "the dashboard release publishes the tag prefix the chart reads" 1 \
  "$(grep -c "tag_prefix: \"$(component_tag_prefix dashboard)\"" "$real/.github/workflows/release-dashboard.yaml")"
check "the operator release triggers the chart release" 1 \
  "$(grep -cxF '      - Release' "$real/.github/workflows/release-chart.yaml")"

# ---------------------------------------------------------------- release-operator-chart.sh

if [ -z "${RELEASE_LIB:-}" ] || ! command -v helm >/dev/null; then
  echo "skip release-operator-chart.sh tests: RELEASE_LIB and helm are needed"
else
  repo="${work}/chart"
  mkdir -p "$repo/operator/chart/kubemoot-operator" "$repo/dashboard/charts/kubemoot-dashboard"
  git -C "$repo" init -q
  printf 'apiVersion: v2\nname: kubemoot-operator\nversion: 0.0.0\nappVersion: 0.0.0\n' >"$repo/operator/chart/kubemoot-operator/Chart.yaml"
  printf 'apiVersion: v2\nname: kubemoot-dashboard\nversion: 0.0.0\nappVersion: 0.0.0\n' >"$repo/dashboard/charts/kubemoot-dashboard/Chart.yaml"
  printf 'runner:\n  image: indexer:0.0.0\n' >"$repo/operator/chart/kubemoot-operator/values.yaml"
  printf 'replicas: 1\n' >"$repo/dashboard/charts/kubemoot-dashboard/values.yaml"
  wf Release release.yaml "      - 'operator/**'"
  wf "Release Indexer" release-indexer.yaml "      - 'indexer/**'"
  wf "Release Dashboard" release-dashboard.yaml "      - 'dashboard/**'"
  commit operator/main.go indexer/app.java dashboard/app.ts
  g tag -a operator-chart-v0.1.0 -m c
  g tag -a v0.1.0-rc.1 -m c
  g tag -a dashboard-v0.1.0-rc.1 -m c
  g tag -a indexer-v1.0.0-rc.1 -m c

  # chart DRY FORCE: a dry run in the scratch repository; prints the exit status.
  run_chart() {
    local rc=0
    rm -rf "$repo/out" "$repo/output"
    (cd "$repo" && DRY_RUN=true FORCE="${1:-false}" OUT_DIR="$repo/out" GITHUB_OUTPUT="$repo/output" \
      bash "$here/release-operator-chart.sh" >"${work}/chart.log" 2>&1) || rc=$?
    echo "$rc"
  }
  out() { sed -n "s/^$1=//p" "$repo/output" 2>/dev/null | tail -n 1; }

  check "current images: the dry run plans a candidate" "0 true" "$(run_chart) $(out will_release)"

  # The manifest the script would record, as a candidate tag, so the chart is current.
  body=$(sed -n 's/^  //p' "${work}/chart.log" | grep -E '^(chart-source|dashboard-source|kubemoot-operator|dashboard|indexer) ')
  g tag -a operator-chart-v0.1.1-rc.0 -m c -m "$body"
  check "a chart already current builds nothing" "0 false" "$(run_chart) $(out will_release)"
  check "the current chart says so" 1 "$(grep -c 'nothing to build' "${work}/chart.log")"

  # A code change under the operator filter, no newer image: the pairing check holds the
  # chart before chart_is_current can call it current, and FORCE does not bypass it.
  commit operator/pkg/x.go
  check "a stale pin holds the chart (exit 0, no release)" "0 false" "$(run_chart) $(out will_release)"
  check "the hold names the component and the command" 1 "$(grep -c '^::warning::No chart candidate.*kubemoot-operator 0.1.0-rc.1.*gh workflow run release.yaml' "${work}/chart.log")"
  check "the hold is not the 'current' message (pairing runs first)" 0 "$(grep -c 'nothing to build' "${work}/chart.log")"
  check "a held chart packages nothing" 0 "$(find "$repo/out" -name "*.tgz" 2>/dev/null | wc -l)"
  check "FORCE=true still holds" "0 false" "$(run_chart true) $(out will_release)"

  g tag -a v0.1.0-rc.2 -m c
  check "the new image releases the chart again" "0 true" "$(run_chart) $(out will_release)"

  # An undecidable rule stops the script.
  git -C "$repo" rm -q .github/workflows/release-indexer.yaml
  g commit -q -m "ci: drop the indexer workflow"
  check "an undecidable pin fails the script" 1 "$(run_chart)"
  check "the failure says why" 1 "$(grep -c 'cannot tell whether every pinned image is current' "${work}/chart.log")"
fi

if [ "$failures" -gt 0 ]; then
  echo "${failures} failure(s)"
  exit 1
fi
echo "all passed"
