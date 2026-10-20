#!/usr/bin/env bash
# The operator chart version that matches this checkout's quickstart files, read from
# the release tags. Sourced by quickstart.sh; no version is typed in any file.
#
# A release tags the commit it published as operator-chart-vX.Y.Z (release candidates
# carry an -rc.N suffix and are skipped). The quickstart applies manifests next to the
# script, so they must match the chart they run against. This takes the nearest release
# tag behind HEAD and accepts it when the quickstart files are the same as at that tag,
# whatever else changed since.

# release_chart_version DIR: print the chart version for the checkout holding DIR.
# Exit 1 with the reason on stderr when there is none: not a git checkout, no release
# tag reachable (a shallow clone), or quickstart files that changed after the release.
release_chart_version() {
  local dir="$1" top tag
  top="$(git -C "$dir" rev-parse --show-toplevel 2>/dev/null)" || {
    echo "not inside a git checkout, so the release this script belongs to is unknown" >&2
    return 1
  }
  tag="$(git -C "$top" describe --tags --abbrev=0 --match 'operator-chart-v[0-9]*' --exclude '*-rc.*' 2>/dev/null)" || {
    echo "no release tag (operator-chart-vX.Y.Z) is reachable from this checkout; fetch tags or use a full clone" >&2
    return 1
  }
  # The working tree against the tag: committed and uncommitted edits both count. The
  # README does not feed the cluster, so it does not matter.
  if ! git -C "$top" diff --quiet "$tag" -- quickstart ':(exclude)quickstart/README.md' ':(exclude)quickstart/*.test.sh'; then
    echo "the quickstart files differ from the latest release ($tag), so they may not match the published chart;" >&2
    echo "check out that tag, or set KUBEMOOT_CHART_VERSION to a chart you know matches" >&2
    return 1
  fi
  printf '%s\n' "${tag#operator-chart-v}"
}
