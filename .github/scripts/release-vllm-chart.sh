#!/usr/bin/env bash
# Build the kubemoot-vllm chart release candidate, with its version read from the tags.
#
# Git holds 0.0.0 in vllm/charts/kubemoot-vllm/Chart.yaml. The chart references no
# Kubemoot image (the vLLM image is set by whoever installs it, pinned by digest), so its
# only version is its own: the next vllm-chart-vX.Y.Z-rc.N after the latest candidate, or
# X.Y.(Z+1)-rc.0 once that candidate's X.Y.Z is published, or 0.1.0-rc.0 for the first.
# The chart is packaged from HEAD (the tip of main in CI) in a scratch worktree, stamped,
# and never committed.
#
# The chart's source tree is the build's manifest, recorded in the candidate tag's
# message. When the latest candidate has the same tree, the chart is current and nothing
# is built; FORCE=true builds anyway.
#
# With DRY_RUN=false the candidate tag vllm-chart-vX.Y.Z-rc.N is pushed on HEAD first,
# which claims the version, and then the chart goes to Harbor. A failed chart push
# deletes the tag again, so a tag always names a chart Harbor holds.
#
# Env:
#   DRY_RUN      "true" (default) resolves and packages into OUT_DIR; pushes nothing
#   FORCE        "true" builds even when the latest candidate is current
#   REGISTRY     Harbor host (chart at oci://${REGISTRY}/kubemoot/charts); required
#                unless dry; the caller logs helm in
#   OUT_DIR      where the packaged chart lands (default: chart-release)
#   RELEASE_LIB  release-lib.sh of kubemoot/release-actions (its actions set it)
# Outputs ($GITHUB_OUTPUT): released, chart_version.
set -euo pipefail
# A failure inside $(...) stops the script too.
shopt -s inherit_errexit

# shellcheck source=/dev/null
source "${RELEASE_LIB:?RELEASE_LIB must point to release-lib.sh from kubemoot/release-actions}"
# shellcheck source=./release-chart-lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/release-chart-lib.sh"

FORCE="${FORCE:-false}"
OUT_DIR="$(mkdir -p "${OUT_DIR:-chart-release}" && cd "${OUT_DIR:-chart-release}" && pwd)"
rl_is_dry || : "${REGISTRY:?REGISTRY required}"

# shellcheck disable=SC2034 # read by release-chart-lib.sh
CHART_PREFIX="vllm-chart-v"
CHART_DIR="vllm/charts/kubemoot-vllm"
FIRST_VERSION="0.1.0-rc.0"

trap rl_remove_worktrees EXIT

# manifest COMMIT: what a chart built at COMMIT contains; equal manifests mean the same chart.
manifest() {
  echo "chart-source $(git rev-parse "$1:${CHART_DIR}")"
}

main() {
  local commit body version
  commit=$(git rev-parse HEAD)
  body=$(manifest "$commit")
  if [ "$FORCE" != "true" ] && [ "$body" = "$(current_manifest "$commit")" ]; then
    echo "The latest chart candidate already has these chart sources; nothing to build"
    output released false
    return 0
  fi

  rl_checkout_at "$commit"
  version=$(next_chart_version "$commit" "$FIRST_VERSION")
  rl_stamp_chart "${RL_CHECKOUT}/${CHART_DIR}" "$version"
  helm package "${RL_CHECKOUT}/${CHART_DIR}" --destination "${OUT_DIR}" >&2
  echo "kubemoot-vllm chart ${version} at ${commit}; dry run: ${DRY_RUN:-true}"
  output chart_version "$version"
  if rl_is_dry; then
    output released false
    return 0
  fi
  claim_and_push_chart "$version" "$commit" "$body" "${OUT_DIR}/kubemoot-vllm-${version}.tgz" "kubemoot-vllm chart"
  output released true
}

main "$@"
