#!/usr/bin/env bash
# Build the operator chart release candidate, with every version read from the tags.
#
# Git holds 0.0.0 for the operator chart and the dashboard subchart and for every
# Kubemoot image tag in their values. Component releases build and tag their images
# only; this script packages the chart from HEAD (the tip of main in CI) in a scratch
# worktree, never committed, stamped with:
#   chart version     the next operator-chart-vX.Y.Z-rc.N after the latest candidate,
#                     or X.Y.(Z+1)-rc.0 once that candidate's X.Y.Z is published
#   appVersion        the latest operator candidate vX.Y.Z-rc.N (the operator image)
#   dashboard         the subchart's version and appVersion (the dashboard image): the
#                     latest dashboard-vX.Y.Z-rc.N
#   operator values   each NAME:0.0.0 image: the latest NAME-vX.Y.Z-rc.N, the candidate
#                     the homelab runs
#   dashboard values  each NAME:0.0.0 image: the latest final NAME-vX.Y.Z, because it is
#                     a full reference to the public registry, which holds finals only
# All of them are tags reachable from HEAD.
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
# Outputs ($GITHUB_OUTPUT): released, chart_version, test_runner (the test-runner tag
# the chart pins for verification jobs).
set -euo pipefail
# A failure inside $(...) stops the script too.
shopt -s inherit_errexit

# shellcheck source=/dev/null
source "${RELEASE_LIB:?RELEASE_LIB must point to release-lib.sh from kubemoot/release-actions}"

FORCE="${FORCE:-false}"
OUT_DIR="$(mkdir -p "${OUT_DIR:-chart-release}" && cd "${OUT_DIR:-chart-release}" && pwd)"
rl_is_dry || : "${REGISTRY:?REGISTRY required}"

CHART_PREFIX="operator-chart-v"
CHART_DIR="operator/chart/kubemoot-operator"
DASH_DIR="dashboard/charts/kubemoot-dashboard"

die() { echo "ERROR: $*" >&2; exit 1; }
trap rl_remove_worktrees EXIT

output() {
  [ -n "${GITHUB_OUTPUT:-}" ] && echo "$1=$2" >> "$GITHUB_OUTPUT"
  return 0
}

# next_chart_version COMMIT: the version after the latest chart candidate.
next_chart_version() {
  local latest current published=false
  latest=$(rl_latest_rc "$CHART_PREFIX" "$1")
  [ -n "$latest" ] || die "no ${CHART_PREFIX}X.Y.Z-rc.N tag reachable from $1 to count from"
  current="${latest#"$CHART_PREFIX"}"
  rl_tag_exists "${CHART_PREFIX}$(rl_final_of "$current")" && published=true
  rl_next_chart_rc "$current" "$published"
}

# stamp WORKTREE COMMIT: stamps both charts in WORKTREE and prints the manifest lines
# of what it resolved ("NAME VERSION", dashboard values prefixed "dashboard/").
stamp() {
  local wt="$1" commit="$2" operator dashboard pins
  operator=$(rl_latest_version v "$commit" rc) || die "no operator candidate tag vX.Y.Z-rc.N"
  dashboard=$(rl_latest_version dashboard-v "$commit" rc) || die "no dashboard candidate tag"
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

# current_manifest: the manifest recorded by the latest chart candidate, if any.
current_manifest() {
  local latest
  latest=$(rl_latest_rc "$CHART_PREFIX" "$1")
  [ -n "$latest" ] || return 0
  git tag -l --format='%(contents:body)' "$latest" | sed '/^$/d'
}

# push_chart TGZ: the chart to Harbor, three attempts.
push_chart() {
  local attempt
  for attempt in 1 2 3; do
    helm push --insecure-skip-tls-verify "$1" "oci://${REGISTRY}/kubemoot/charts" && return 0
    echo "helm push failed (attempt ${attempt}/3); retrying in 5s"
    sleep 5
  done
  return 1
}

# publish VERSION COMMIT MANIFEST: claim the version with the candidate tag, then push
# the chart. A tag without its chart would make every later run see the chart as
# current, so a failed push removes the tag or stops with the command to remove it.
publish() {
  local version="$1" commit="$2" body="$3" tag="${CHART_PREFIX}$1"
  git tag -a "$tag" -m "Release candidate operator chart ${version}" -m "$body" "$commit"
  if ! git push origin "refs/tags/${tag}"; then
    git tag -d "$tag" >/dev/null
    die "could not push ${tag}; it exists on origin already (another chart release claimed it), so run again"
  fi
  if push_chart "${OUT_DIR}/kubemoot-operator-${version}.tgz"; then
    echo "Released operator chart ${version}"
    return 0
  fi
  git tag -d "$tag" >/dev/null
  git push --delete origin "refs/tags/${tag}" \
    || die "the chart push failed and ${tag} could not be removed; remove it by hand: git push --delete origin refs/tags/${tag}"
  die "the chart push failed after 3 attempts; ${tag} was removed again"
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
  if chart_is_current "$commit" "$body"; then
    echo "The latest chart candidate already has these versions and chart sources; nothing to build"
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
  if rl_is_dry; then
    output released false
    return 0
  fi
  publish "$version" "$commit" "$body"
  output released true
}

main "$@"
