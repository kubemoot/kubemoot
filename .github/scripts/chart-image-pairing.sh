#!/usr/bin/env bash
# The pairing rule of the operator chart release: a chart candidate pins a component
# image only when that image was built from code at least as new as the commit the chart
# is packaged from, for every file the component's image is built from.
#
# A component's release workflow runs on pushes that touch its path filter
# (on.push.paths of release.yaml for the operator, release-NAME.yaml for the rest) and
# tags the commit it built. So the pinned image is current when no commit after that
# tag's commit touched the component's path filter. Each commit is judged against the
# filter as it was at that commit, because that is the filter that decided whether the
# push released anything; the workflow file is the one source, nothing is listed twice.
#
# Chart templates may pass a flag or read a value that only newer component code
# understands, so a chart is not built while a pinned image is behind. The component's
# release, when it succeeds, triggers the chart release again (state, not a wait), and
# that run finds every pin current.
#
# Filter semantics follow GitHub: "*" matches within a directory, "**" across them,
# "?" one character, and the last pattern that matches a file decides (a "!" pattern
# excludes what the patterns before it included). The parser reads the layout of this
# repository's workflows: a top-level "on:" mapping with "push:" at two spaces,
# "paths:" at four and the entries ("- pattern", at six) below it. A filter it cannot
# read (paths-ignore, a "[" pattern, no paths list at the packaged commit, no git
# object) makes the rule undecidable, never "current".
#
# Source it from a script that has release-lib.sh of kubemoot/release-actions loaded.
# pins_are_current returns 0 (every pin current), 1 (a pin is behind) or 2 (the rule
# cannot be decided), printing the lines found.

# shellcheck source=/dev/null
source "$(dirname "${BASH_SOURCE[0]}")/chart-components.sh"

# workflow_push_paths: the on.push.paths patterns of the workflow on stdin, one per
# line, "!" kept. Prints nothing when there are none; returns 3 (message on stderr) for
# a filter it cannot read.
workflow_push_paths() {
  awk '
    { sub(/\r$/, "") }
    /^[[:space:]]*(#.*)?$/ { next }
    /^  push:[[:space:]]*$/ { push = 1; next }
    push && /^    paths-ignore:/ { print "paths-ignore is not supported" > "/dev/stderr"; exit 3 }
    push && /^    paths:[[:space:]]*$/ { paths = 1; next }
    paths && /^      -[[:space:]]/ {
      sub(/^      -[[:space:]]*/, "")
      if (match($0, /^'\''[^'\'']*'\''/) || match($0, /^"[^"]*"/)) $0 = substr($0, 2, RLENGTH - 2)
      else { sub(/[[:space:]]+#.*$/, ""); sub(/[[:space:]]+$/, "") }
      if ($0 ~ /\[/) { print "a [ pattern is not supported: " $0 > "/dev/stderr"; exit 3 }
      print
      next
    }
    paths { exit }
    push && /^  [a-z_]+:/ { push = 0 }
  '
}

# touches_filter PATTERNS FILES: prints "yes" when the last matching pattern of PATTERNS
# (newline separated, GitHub semantics above) includes any path of FILES (newline
# separated).
touches_filter() {
  PATTERNS="$1" awk '
    function regex(p,    i, c, r) {
      r = "^"
      for (i = 1; i <= length(p); i++) {
        c = substr(p, i, 1)
        if (c == "*" && substr(p, i + 1, 1) == "*") { r = r ".*"; i++ }
        else if (c == "*") r = r "[^/]*"
        else if (c == "?") r = r "[^/]"
        else if (c ~ /[].^$+(){}|\\]/) r = r "\\" c
        else r = r c
      }
      return r "$"
    }
    BEGIN {
      n = split(ENVIRON["PATTERNS"], raw, "\n")
      for (i = 1; i <= n; i++) {
        neg[i] = (substr(raw[i], 1, 1) == "!")
        re[i] = regex(neg[i] ? substr(raw[i], 2) : raw[i])
      }
    }
    {
      included = 0
      for (i = 1; i <= n; i++) if ($0 ~ re[i]) included = !neg[i]
      if (included) { print "yes"; exit }
    }
  ' <<<"$2"
}

# filter_at COMMIT NAME: the path filter patterns of NAME's release workflow at COMMIT
# (nothing when the workflow has none: every push releases). Returns 2 when COMMIT has
# no such workflow, 1, printing why, when the filter cannot be read.
filter_at() {
  local workflow text patterns rc=0
  workflow=".github/workflows/$(component_workflow "$2")"
  git cat-file -e "${1}:${workflow}" 2>/dev/null || return 2
  text=$(git show "${1}:${workflow}") || { echo "cannot read ${workflow} at ${1}"; return 1; }
  patterns=$(workflow_push_paths <<<"$text" 2>&1) || rc=$?
  if [ "$rc" -ne 0 ]; then echo "${workflow} at ${1}: ${patterns}"; return 1; fi
  printf '%s' "$patterns"
}

# commit_touches COMMIT NAME: prints "yes" when COMMIT changed a file in the path filter
# NAME's release workflow had at COMMIT, "no" when not (a commit before the workflow
# existed released nothing). Returns 1, printing why, when that cannot be told.
commit_touches() {
  local patterns files rc=0
  patterns=$(filter_at "$1" "$2") || rc=$?
  case "$rc" in
    0) ;;
    2) echo "no"; return 0 ;;
    *) printf '%s\n' "$patterns"; return 1 ;;
  esac
  [ -n "$patterns" ] || { echo "yes"; return 0; }
  files=$(git diff-tree -r -m --first-parent --root --no-commit-id --name-only "$1") \
    || { echo "cannot list the files of ${1}"; return 1; }
  [ -n "$(touches_filter "$patterns" "$files")" ] && echo "yes" || echo "no"
}

# stale_pins COMMIT PINS: for every "NAME VERSION" line of PINS (a "dashboard/NAME" line
# is a public-registry final the filter does not apply to, and is skipped) prints
# "NAME VERSION: N commit(s) after <tag commit> touch its sources; ..." with the command
# that builds it, when a commit between the tagged commit and COMMIT touches the
# component's path filter. Returns 1, printing why instead, when the rule cannot be
# decided for some pin. Every git call is checked: a failure is "undecidable", never
# "current".
stale_pins() {
  local commit="$1" name version tag tagged workflow commits count c answer filter status=0
  while read -r name version; do
    [ -n "$name" ] || continue
    case "$name" in dashboard/*) continue ;; esac
    workflow="$(component_workflow "$name")"
    tag="$(component_tag_prefix "$name")${version}"
    if ! tagged=$(git rev-parse -q --verify "refs/tags/${tag}^{commit}"); then
      echo "${name}: no tag ${tag} (fetch the tags: git fetch --tags)"
      status=1
      continue
    fi
    filter=$(filter_at "$commit" "$name") || { echo "${name}: ${filter:-no ${workflow} at ${commit}}"; status=1; continue; }
    [ -n "$filter" ] || { echo "${name}: no on.push.paths filter in ${workflow} at ${commit}"; status=1; continue; }
    if ! commits=$(git rev-list "${tagged}..${commit}"); then
      echo "${name}: cannot list the commits ${tagged}..${commit}"
      status=1
      continue
    fi
    count=0
    while read -r c; do
      [ -n "$c" ] || continue
      if ! answer=$(commit_touches "$c" "$name"); then
        echo "${name}: ${answer}"
        status=1
        continue 2
      fi
      [ "$answer" != "yes" ] || count=$((count + 1))
    done <<<"$commits"
    [[ $count =~ ^[0-9]+$ ]] || { echo "${name}: no commit count"; status=1; continue; }
    if [ "$count" -gt 0 ]; then
      echo "${name} ${version}: ${count} commit(s) after ${tagged} touch its sources; build it: gh workflow run ${workflow} -f force_release=true"
    fi
  done <<<"$2"
  return "$status"
}

# pins_are_current COMMIT PINS: 0 when stale_pins finds nothing; otherwise the lines it
# found go to stderr, and the status is 1 when a pin is behind, 2 when undecidable.
pins_are_current() {
  local found rc=0
  found=$(stale_pins "$1" "$2") || rc=$?
  [ "$rc" -eq 0 ] && [ -z "$found" ] && return 0
  printf '%s\n' "$found" >&2
  [ "$rc" -eq 0 ] && return 1
  return 2
}
