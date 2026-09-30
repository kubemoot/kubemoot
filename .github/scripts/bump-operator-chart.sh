#!/usr/bin/env bash
# Collision-safe bump + release of the operator Helm chart.
#
# The operator chart's version counter and its values.yaml image pins are bumped
# by EIGHT release workflows (7 component releases + the operator release), all in
# the kubemoot-operator-chart concurrency group. Serialization alone was not
# enough: each release read the chart version from its OWN (stale) checkout, so two
# releases triggered from the same commit computed the SAME next version, and the
# OCI `helm push` (done BEFORE the git push) overwrote each other - the published
# chart then diverged from git (operator frozen a version behind, 2026-06-21).
#
# This script fixes that. It ALWAYS bumps from the CURRENT origin/main chart
# version inside a push-retry loop, re-applying ONLY this release's own edit onto
# fresh main, and pushes the chart to OCI ONLY after the git push lands. So:
#   - the chart version is unique (each attempt re-reads origin/main), and
#   - the published OCI artifact always matches what is in git.
# See [[Chart Version Collision on Concurrent Releases]].
#
# Every release from main is a release candidate: the chart version is X.Y.Z-rc.N and
# goes to Harbor only, tagged operator-chart-vX.Y.Z-rc.N. Promote Release publishes the
# final X.Y.Z (see promote-release.sh); the next candidate then starts at X.Y.(Z+1)-rc.0.
#
# Required env:
#   VERSION     image version just built+pushed (e.g. 0.296.0)
#   EDIT_KIND   "operator" (bump Chart.yaml appVersion) | "component" | "dashboard"
#               ("dashboard" edits nothing: the dashboard subchart is a local file://
#               dependency, so re-packaging from fresh main embeds the just-released
#               dashboard chart; only the operator chart version moves)
#   REGISTRY    OCI registry host; helm must already be logged in by the caller
# Component mode additionally requires:
#   VALUES_KEY  values.yaml image key, e.g. agentRuntime
#   IMAGE_REPO  bare image name as it appears in values.yaml, e.g. agent-runtime
#               (the chart prefixes global.imageRegistry at render time)
#   LABEL       human label for the commit message, e.g. agent-runtime
set -euo pipefail

# shellcheck source-path=SCRIPTDIR source=release-lib.sh
source "$(dirname "$0")/release-lib.sh"

CHART_DIR="operator/chart/kubemoot-operator"
CHART_FILE="${CHART_DIR}/Chart.yaml"
CHART_TAG_PREFIX="operator-chart-v"
VALUES_FILE="${CHART_DIR}/values.yaml"

: "${VERSION:?VERSION required}"
: "${EDIT_KIND:?EDIT_KIND required}"
: "${REGISTRY:?REGISTRY required}"
# helm push flag: components use --plain-http (proven). The operator release pins
# helm v3.14.0 + --insecure-skip-tls-verify because newer helm refuses Harbor's
# http token realm even with --plain-http (see release.yaml + the Harbor HTTP Realm
# card); it overrides this. The caller's `helm registry login` must use the matching
# flag.
: "${HELM_PUSH_FLAGS:=--plain-http}"

git config user.name "github-actions[bot]"
git config user.email "github-actions[bot]@users.noreply.github.com"

for attempt in 1 2 3 4 5 6 7 8; do
  # Always re-base the edit on CURRENT main so the chart version is read fresh and
  # this release's edit is re-applied cleanly on top of any release that landed first.
  git fetch origin main
  git reset --hard FETCH_HEAD

  if [ "${EDIT_KIND}" = "operator" ]; then
    sed -i "s/^appVersion:.*/appVersion: \"${VERSION}\"/" "${CHART_FILE}"
    msg="update operator image to v${VERSION}"
  elif [ "${EDIT_KIND}" = "dashboard" ]; then
    embedded=$(grep '^version:' dashboard/charts/kubemoot-dashboard/Chart.yaml | awk '{print $2}')
    if [ "${embedded}" != "${VERSION}" ]; then
      echo "ERROR: main carries dashboard chart ${embedded}, not ${VERSION}; the dashboard chart commit did not land" >&2
      exit 1
    fi
    msg="update dashboard subchart to v${VERSION}"
  else
    : "${VALUES_KEY:?VALUES_KEY required in component mode}"
    : "${IMAGE_REPO:?IMAGE_REPO required in component mode}"
    : "${LABEL:?LABEL required in component mode}"
    # Anchor on the line's own indent and the bare image name so the key matches only
    # the real image line, never a comment or a longer key that contains this name.
    sed -i "s|^\(\s*\)${VALUES_KEY}: ${IMAGE_REPO}:.*|\1${VALUES_KEY}: ${IMAGE_REPO}:${VERSION}|" "${VALUES_FILE}"
    msg="update ${LABEL} to v${VERSION}"
  fi

  current=$(grep '^version:' "${CHART_FILE}" | awk '{print $2}')
  # ls-remote --exit-code: 0 = the final tag exists, 2 = it does not, else an error.
  status=0
  git ls-remote --exit-code --tags origin "refs/tags/${CHART_TAG_PREFIX}$(rl_final_of "${current}")" >/dev/null || status=$?
  case "${status}" in
    0) promoted=true ;;
    2) promoted=false ;;
    *) echo "ERROR: could not read tags from origin (git ls-remote exit ${status})" >&2; exit 1 ;;
  esac
  new_chart=$(rl_next_chart_rc "${current}" "${promoted}")
  sed -i "s/^version:.*/version: ${new_chart}/" "${CHART_FILE}"

  git add "${CHART_FILE}" "${VALUES_FILE}"
  git commit -m "chore: ${msg}, operator chart to v${new_chart} [skip ci]"

  if git push origin main; then
    # OCI push AFTER the git push lands: the published chart always matches git,
    # and because we re-read origin/main on each attempt the version is unique -
    # concurrent releases can never publish the same chart version with different
    # content.
    # -u runs `helm dependency update` first: the chart depends on the dashboard chart
    # by local path (file://), and packaging fails without the subchart built into charts/.
    helm package -u "${CHART_DIR}"
    # The version is now claimed in git. Push the matching OCI artifact with retries
    # so a transient Harbor hiccup can't leave a git-committed chart version with no
    # artifact (Flux would fail to pull it). No re-read needed: git already landed.
    for push_attempt in 1 2 3; do
      # HELM_PUSH_FLAGS may hold more than one flag, so it is split on purpose.
      # shellcheck disable=SC2086
      if helm push ${HELM_PUSH_FLAGS} "kubemoot-operator-${new_chart}.tgz" "oci://${REGISTRY}/kubemoot/charts"; then
        echo "Released operator chart v${new_chart} (${msg})"
        # The candidate tag names the commit whose chart the homelab runs; Promote
        # Release takes it (or the latest one) as its input.
        if ! git rev-parse -q --verify "refs/tags/${CHART_TAG_PREFIX}${new_chart}" >/dev/null; then
          git tag -a "${CHART_TAG_PREFIX}${new_chart}" -m "Release candidate operator chart ${new_chart}"
        fi
        git push origin "${CHART_TAG_PREFIX}${new_chart}"
        exit 0
      fi
      echo "helm push failed (attempt ${push_attempt}/3); retrying in 5s"
      sleep 5
    done
    echo "ERROR: git committed chart v${new_chart} but OCI helm push failed after 3 attempts" >&2
    exit 1
  fi

  echo "git push rejected (attempt ${attempt}/8); re-reading origin/main and retrying"
done

# 5 attempts is ample: cancel-in-progress=false queues at most one pending run per
# group, so at worst two releases land back-to-back and only one push ever conflicts.
echo "ERROR: could not land the operator chart bump after 5 attempts" >&2
exit 1
