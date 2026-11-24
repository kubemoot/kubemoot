#!/usr/bin/env bash
# The pairing rule of the operator chart release: a chart candidate pins a component
# image only when that image was built from code at least as new as the commit the chart
# is packaged from, for every file the component's image is built from.
#
# A component's release workflow runs on pushes that touch its path filter
# (on.push.paths of release.yaml for the operator, release-NAME.yaml for the rest) and
# tags the commit it built. So the pinned image is current when no commit after that
# tag's commit touches the component's path filter. The workflow file is the one source
# of the filter, read from the commit being packaged; nothing is listed twice.
#
# Chart templates may pass a flag or read a value that only newer component code
# understands, so a chart is not built while a pinned image is behind. The component's
# release, when it succeeds, triggers the chart release again (state, not a wait), and
# that run finds every pin current.
#
# Source it from a script that has release-lib.sh of kubemoot/release-actions loaded.
# pins_are_current returns 0 (every pin current), 1 (a pin is behind) or 2 (the rule
# cannot be decided: no workflow, no filter or no tag), printing the lines found.

# component_workflow NAME: the release workflow file name of the component.
component_workflow() {
  if [ "$1" = "kubemoot-operator" ]; then echo "release.yaml"; else echo "release-$1.yaml"; fi
}

# component_tag_prefix NAME: the tag prefix of the component's candidates.
component_tag_prefix() {
  if [ "$1" = "kubemoot-operator" ]; then echo "v"; else echo "$1-v"; fi
}

# workflow_pathspecs TEXT: git pathspecs for the on.push.paths filter in the workflow TEXT
# (stdin), one per line: "dir/**" as it is, "!dir/**.md" as ":(exclude)dir/**.md". Git's
# default pathspec matching lets * cross directories, as the workflow filter does.
workflow_pathspecs() {
  awk '
    /^[[:space:]]*(#.*)?$/ { next }
    /^  push:[[:space:]]*$/ { push = 1; next }
    push && /^    paths:[[:space:]]*$/ { paths = 1; next }
    paths && /^      -[[:space:]]/ {
      sub(/^      -[[:space:]]*/, "")
      if (match($0, /^'\''[^'\'']*'\''/) || match($0, /^"[^"]*"/)) $0 = substr($0, 2, RLENGTH - 2)
      else sub(/[[:space:]]+#.*$/, "")
      if (substr($0, 1, 1) == "!") print ":(exclude)" substr($0, 2); else print
      next
    }
    paths { exit }
    push && /^  [a-z_]+:/ { push = 0 }
  '
}

# stale_pins COMMIT PINS: for every "NAME VERSION" line of PINS (a "dashboard/NAME" line
# is a public-registry final the filter does not apply to, and is skipped) prints
# "NAME VERSION: N commit(s) after <tag commit> touch its sources" when a commit between
# the tagged commit and COMMIT touches the component's path filter. Returns 1 when the
# rule cannot be decided (no workflow, no filter, no tag), printing why instead.
stale_pins() {
  local commit="$1" name version tag tagged specs text workflow count status=0
  local -a specs_arr
  while read -r name version; do
    [ -n "$name" ] || continue
    case "$name" in dashboard/*) continue ;; esac
    workflow=".github/workflows/$(component_workflow "$name")"
    # Read the whole file first: awk stops at the end of the list and would SIGPIPE git.
    text=$(git show "${commit}:${workflow}" 2>/dev/null) || text=""
    specs=$(workflow_pathspecs <<<"$text")
    if [ -z "$specs" ]; then
      echo "${name}: no on.push.paths filter in ${workflow} at ${commit}"
      status=1
      continue
    fi
    tag="$(component_tag_prefix "$name")${version}"
    if ! tagged=$(git rev-parse -q --verify "refs/tags/${tag}^{commit}"); then
      echo "${name}: no tag ${tag}"
      status=1
      continue
    fi
    mapfile -t specs_arr <<<"$specs"
    count=$(git rev-list --count "${tagged}..${commit}" -- "${specs_arr[@]}")
    if [ "$count" -gt 0 ]; then
      echo "${name} ${version}: ${count} commit(s) after ${tagged} touch its sources"
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
