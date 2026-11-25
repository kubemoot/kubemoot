#!/usr/bin/env bash
# Build the operator chart release candidate, with every version read from the tags.
#
# Git holds 0.0.0 for the operator chart and the dashboard subchart and for every
# Kubemoot image tag in their values. Component releases build and tag their images
# only; this script packages the chart from HEAD (the tip of main in CI) in a scratch
# worktree, never committed, stamped with:
#   chart version     the next operator-chart-vX.Y.Z-rc.N: X.Y.Z is one conventional-
#                     commit bump (feat minor, fix patch, ! major held at 0.x as the
#                     next minor) past the last final chart tag, from the commits that
#                     touch the two chart directories; N counts up until X.Y.Z is
#                     published, then the next bump is computed from the commits after it
#   appVersion        the latest operator candidate vX.Y.Z-rc.N (the operator image)
#   dashboard         the subchart's version and appVersion (the dashboard image): the
#                     latest dashboard-vX.Y.Z-rc.N
#   operator values   each NAME:0.0.0 image: the latest NAME-vX.Y.Z-rc.N, the candidate
#                     the homelab runs
#   dashboard values  each NAME:0.0.0 image: the latest final NAME-vX.Y.Z, because it is
#                     a full reference to the public registry, which holds finals only
# All of them are tags reachable from HEAD.
#
# A chart is never built while a pinned image is older than the code at HEAD: the
# commits after the image's tagged commit must not touch the component's release path
# filter (chart-image-pairing.sh). The component's own release triggers this workflow
# again when it finishes, so the chart follows the image, FORCE or not.
#
# The resolved versions and the chart source trees are the build's manifest, recorded
# in the candidate tag's message. When the latest candidate has the same manifest, the
# chart is current and nothing is built; FORCE=true builds anyway.
#
# With DRY_RUN=false the candidate tag operator-chart-vX.Y.Z-rc.N is pushed on HEAD
# first, which claims the version, and then the chart goes to Harbor. A failed chart
# push deletes the tag again, so a tag always names a chart Harbor holds.
#
# Env:
#   DRY_RUN      "true" (default) resolves and packages into OUT_DIR; pushes nothing
#   FORCE        "true" builds even when the latest candidate is current
#   REGISTRY     Harbor host (chart at oci://${REGISTRY}/kubemoot/charts); required
#                unless dry; the caller logs helm in
#   OUT_DIR      where the packaged chart lands (default: chart-release)
#   RELEASE_LIB  release-lib.sh of kubemoot/release-actions (its actions set it)
# Outputs ($GITHUB_OUTPUT): will_release (a candidate is due: true in a dry run too, so
# a workflow can plan first and mint its push token only when one is), released,
# held and held_file (a pinned image is behind the code; the stale pins, one per line, are
# in held_file for report-held-chart.sh), chart_version, test_runner (the test-runner tag
# the chart pins for verification jobs).
set -euo pipefail
# The release token leaves the environment before any other command runs (see push-token.sh).
# shellcheck source=/dev/null
source "$(dirname "${BASH_SOURCE[0]}")/push-token.sh"
# A failure inside $(...) stops the script too.
shopt -s inherit_errexit

# shellcheck source=/dev/null
source "${RELEASE_LIB:?RELEASE_LIB must point to release-lib.sh from kubemoot/release-actions}"
# shellcheck source=/dev/null
source "$(dirname "${BASH_SOURCE[0]}")/release-chart-lib.sh"
# shellcheck source=/dev/null
source "$(dirname "${BASH_SOURCE[0]}")/chart-image-pairing.sh"

FORCE="${FORCE:-false}"
OUT_DIR="$(mkdir -p "${OUT_DIR:-chart-release}" && cd "${OUT_DIR:-chart-release}" && pwd)"
rl_is_dry || : "${REGISTRY:?REGISTRY required}"

# shellcheck disable=SC2034 # read by release-chart-lib.sh
CHART_PREFIX="operator-chart-v"
CHART_DIR="operator/chart/kubemoot-operator"
DASH_DIR="dashboard/charts/kubemoot-dashboard"
# shellcheck disable=SC2034 # read by release-chart-lib.sh
CHART_PATHS=("$CHART_DIR" "$DASH_DIR")

trap rl_remove_worktrees EXIT

# stamp WORKTREE COMMIT: stamps both charts in WORKTREE and prints the manifest lines
# of what it resolved ("NAME VERSION", dashboard values prefixed "dashboard/").
stamp() {
  local wt="$1" commit="$2" operator dashboard pins
  operator=$(rl_latest_version "$(component_tag_prefix kubemoot-operator)" "$commit" rc) || die "no operator candidate tag vX.Y.Z-rc.N"
  dashboard=$(rl_latest_version "$(component_tag_prefix dashboard)" "$commit" rc) || die "no dashboard candidate tag"
  rl_stamp_chart "${wt}/${DASH_DIR}" "$dashboard" || die "cannot stamp ${DASH_DIR}"
  echo "kubemoot-operator ${operator}"
  echo "dashboard ${dashboard}"
  pins=$(rl_stamp_local_images "${wt}/${CHART_DIR}/values.yaml" "$commit" rc) \
    || die "cannot resolve every image of ${CHART_DIR}/values.yaml"
  [ -z "$pins" ] || printf '%s\n' "$pins"
  pins=$(rl_stamp_local_images "${wt}/${DASH_DIR}/values.yaml" "$commit" final) \
    || die "cannot resolve every image of ${DASH_DIR}/values.yaml"
  [ -z "$pins" ] || awk '{ print "dashboard/" $0 }' <<<"$pins"
  if grep -nE ':0\.0\.0([^0-9.]|$)' "${wt}/${CHART_DIR}/values.yaml" "${wt}/${DASH_DIR}/values.yaml" >&2; then
    die "an image above still has the tag 0.0.0"
  fi
}

# manifest COMMIT PINS: what a chart built at COMMIT contains; equal manifests mean
# the same chart.
manifest() {
  echo "chart-source $(git rev-parse "$1:${CHART_DIR}")"
  echo "dashboard-source $(git rev-parse "$1:${DASH_DIR}")"
  printf '%s\n' "$2"
}

# chart_is_current COMMIT MANIFEST: true when the latest candidate recorded MANIFEST.
chart_is_current() {
  [ "$FORCE" != "true" ] && [ "$2" = "$(current_manifest "$1")" ]
}

main() {
  local commit wt pins body version operator_test_runner
  commit=$(git rev-parse HEAD)
  rl_checkout_at "$commit"
  wt="$RL_CHECKOUT"
  pins=$(stamp "$wt" "$commit")
  body=$(manifest "$commit" "$pins")
  # Never pin an image built from older code than the chart's templates (chart-image-pairing.sh).
  local pairing=0 lines
  lines=$(pins_are_current "$commit" "$pins" 2>&1) || pairing=$?
  [ -z "$lines" ] || printf '%s\n' "$lines" >&2
  [ "$pairing" -ne 2 ] || die "cannot tell whether every pinned image is current (above)"
  if [ "$pairing" -ne 0 ]; then
    echo "::warning::No chart candidate at ${commit}: ${lines//$'\n'/%0A}"
    # The lines go to a file for report-held-chart.sh, which fails the job when no release
    # of the held component is running, so a hold nothing will clear is not just a warning.
    printf '%s\n' "$lines" > "${OUT_DIR}/held-chart.txt"
    output held true
    output held_file "${OUT_DIR}/held-chart.txt"
    output will_release false
    output released false
    return 0
  fi
  if chart_is_current "$commit" "$body"; then
    echo "The latest chart candidate already has these versions and chart sources; nothing to build"
    output will_release false
    output released false
    return 0
  fi

  version=$(next_chart_version "$commit")
  rl_stamp_chart "${wt}/${CHART_DIR}" "$version" "$(sed -n 's/^kubemoot-operator //p' <<<"$pins")"
  helm package --dependency-update "${wt}/${CHART_DIR}" --destination "${OUT_DIR}" >&2
  echo "Operator chart ${version} at ${commit}; dry run: ${DRY_RUN:-true}"
  printf '%s\n' "$body" | sed 's/^/  /'

  # The operator's verify image, not the dashboard's public one (dashboard/test-runner).
  operator_test_runner=$(sed -n 's/^test-runner //p' <<<"$pins")
  output chart_version "$version"
  output test_runner "$operator_test_runner"
  output will_release true
  if rl_is_dry; then
    output released false
    return 0
  fi
  claim_and_push_chart "$version" "$commit" "$body" "${OUT_DIR}/kubemoot-operator-${version}.tgz" "operator chart"
  output released true
}

main "$@"
