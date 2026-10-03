#!/usr/bin/env bash
# Promote a tested Kubemoot release candidate to a public release.
#
# main builds release candidates only: every component image X.Y.Z-rc.N and the
# operator chart A.B.C-rc.N go to Harbor, and the homelab runs them. This script
# publishes one of those operator chart candidates, with every component it pins,
# as the final release. Nothing is rebuilt: each image is copied by digest, so the
# published bytes are the tested bytes.
#
#   1. The candidate: the highest operator-chart-vA.B.C-rc.N tag reachable from the
#      starting point (RC_TAG, or the tip of main for "latest").
#   2. Images: every X.Y.Z-rc.N image the chart pins at that commit (values.yaml,
#      the operator appVersion, the dashboard chart when it is a subchart), plus the
#      latest candidate, as of that commit, of each image the chart does not pin
#      (STANDALONE_IMAGES). The promotion is main as it was at the chart candidate.
#      Each is copied by digest to ${RELEASE_REGISTRY}/<name>:X.Y.Z and tagged X.Y.Z
#      in Harbor, and its candidate tag's commit gets the final <prefix>X.Y.Z tag.
#   3. Charts: packaged from the candidate commit with the final version and the
#      final image pins (in a scratch worktree, never committed), pushed to Harbor
#      and the release registry.
#   4. Tags are pushed together (atomic); the release notes go to ${OUT_DIR}/notes.md
#      and ${OUT_DIR}/releases.tsv names the GitHub Release to write.
#
# Re-running after a partial failure is safe: an image already published with the
# same digest is skipped, a different digest stops the run, and the run refuses to
# start when the chart's final tag already exists.
#
# Env:
#   RC_TAG            "latest" (default) or a release-candidate tag on main
#   DRY_RUN           "true" (default) plans, reads registries, packages the charts, and
#                     writes the notes; it pushes nothing and creates no tag
#   REGISTRY          Harbor host (images at ${REGISTRY}/kubemoot/<name>)
#   RELEASE_REGISTRY  registry plus namespace, e.g. ghcr.io/kubemoot
#   HARBOR_USERNAME, HARBOR_PASSWORD, GHCR_USERNAME, GHCR_TOKEN
#   STANDALONE_IMAGES images released on their own (default below)
#   OUT_DIR           where packaged charts and notes.md land (default: promotion)
#   RELEASE_LIB       release-lib.sh of kubemoot/release-actions (its actions set it)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=/dev/null
source "${RELEASE_LIB:?RELEASE_LIB must point to release-lib.sh from kubemoot/release-actions}"

RC_TAG="${RC_TAG:-latest}"
DRY_RUN="${DRY_RUN:-true}"
STANDALONE_IMAGES="${STANDALONE_IMAGES:-code-sandbox artifact-access test-runner dashboard}"
OUT_DIR="$(mkdir -p "${OUT_DIR:-promotion}" && cd "${OUT_DIR:-promotion}" && pwd)"
: "${REGISTRY:?REGISTRY required}"
: "${RELEASE_REGISTRY:?RELEASE_REGISTRY required}"

CHART_PREFIX="operator-chart-v"
CHART_DIR="operator/chart/kubemoot-operator"
DASH_DIR="dashboard/charts/kubemoot-dashboard"

PLAN_NAMES=()     # image names to promote
PLAN_RCS=()       # their candidate versions

die() { echo "ERROR: $*" >&2; exit 1; }
trap rl_remove_worktrees EXIT

# image_prefix NAME: the git tag prefix of an image's releases.
image_prefix() {
  if [ "$1" = "kubemoot-operator" ]; then echo "v"; else echo "$1-v"; fi
}

planned() {
  local n
  for n in "${PLAN_NAMES[@]}"; do [ "$n" = "$1" ] && return 0; done
  return 1
}

plan_add() {
  planned "$1" && return 0
  PLAN_NAMES+=("$1")
  PLAN_RCS+=("$2")
}

# collect_pins WORKTREE: every release-candidate image the operator chart pins.
collect_pins() {
  local wt="$1" app name version
  app=$(sed -nE 's/^appVersion: "?([^"]+)"?$/\1/p' "${wt}/${CHART_DIR}/Chart.yaml")
  rl_is_rc "$app" && plan_add kubemoot-operator "$app"
  while read -r name version; do
    plan_add "$name" "$version"
  done < <(sed -nE 's/^[[:space:]]*[A-Za-z]+: ([a-z0-9-]+):([0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+)[[:space:]]*$/\1 \2/p' \
             "${wt}/${CHART_DIR}/values.yaml")
  if dashboard_is_subchart "$wt"; then
    app=$(sed -nE 's/^appVersion: "?([^"]+)"?$/\1/p' "${wt}/${DASH_DIR}/Chart.yaml")
    rl_is_rc "$app" && plan_add dashboard "$app"
  fi
  return 0
}

dashboard_is_subchart() {
  grep -qE '^[[:space:]]+- name: kubemoot-dashboard' "$1/${CHART_DIR}/Chart.yaml"
}

# collect_standalone COMMIT WORKTREE: the latest candidate, as of the chart
# candidate's COMMIT, of each image the chart does not pin. A dashboard that ships as a
# subchart is pinned by the chart instead.
collect_standalone() {
  local point="$1" wt="$2" name prefix rc
  for name in ${STANDALONE_IMAGES}; do
    planned "$name" && continue
    [ "$name" = "dashboard" ] && dashboard_is_subchart "$wt" && continue
    prefix=$(image_prefix "$name")
    rc=$(rl_latest_rc "$prefix" "$point")
    [ -n "$rc" ] || continue
    rl_tag_exists "${prefix}$(rl_final_of "${rc#"$prefix"}")" && continue
    plan_add "$name" "${rc#"$prefix"}"
  done
}

# promote_image NAME RC: copy the candidate by digest to its final tag in the
# release registry and in Harbor.
promote_image() {
  local name="$1" rc="$2" final digest published harbor
  final=$(rl_final_of "$rc")
  digest=$(crane digest --insecure "${REGISTRY}/kubemoot/${name}:${rc}") \
    || die "${REGISTRY}/kubemoot/${name}:${rc} not found in Harbor"
  published=$(crane digest "${RELEASE_REGISTRY}/${name}:${final}" 2>/dev/null || true)
  if [ -n "$published" ] && [ "$published" != "$digest" ]; then
    die "${RELEASE_REGISTRY}/${name}:${final} exists with digest ${published}, not the candidate's ${digest}"
  fi
  harbor=$(crane digest --insecure "${REGISTRY}/kubemoot/${name}:${final}" 2>/dev/null || true)
  if [ -n "$harbor" ] && [ "$harbor" != "$digest" ]; then
    die "${REGISTRY}/kubemoot/${name}:${final} exists with digest ${harbor}, not the candidate's ${digest}"
  fi
  echo "image ${name}: ${rc} (${digest}) -> ${final}${published:+ (already published)}"
  rl_is_dry && return 0
  if [ -z "$published" ]; then
    bash "${here}/publish-release-image.sh" "${REGISTRY}/kubemoot/${name}@${digest}" "$final"
  fi
  crane tag --insecure "${REGISTRY}/kubemoot/${name}@${digest}" "$final"
}

# tag_final PREFIX RC: tag the candidate's commit with the final version.
tag_final() {
  local prefix="$1" rc="$2" rc_tag final_tag
  rc_tag="${prefix}${rc}"
  final_tag="${prefix}$(rl_final_of "$rc")"
  rl_tag_exists "$final_tag" && return 0
  rl_tag_exists "$rc_tag" || die "no candidate tag ${rc_tag} for a pinned image"
  rl_make_tag "$final_tag" "$rc_tag"
}


# set_chart_version DIR VERSION: version and appVersion of a chart whose app
# version follows the chart (the dashboard chart).
set_chart_version() {
  sed -i -E "s/^version:.*/version: $2/; s/^appVersion:.*/appVersion: $2/" "$1/Chart.yaml"
}

# finalize_pins WORKTREE: rewrite every candidate pin to its final version.
finalize_pins() {
  local wt="$1" i name rc final
  for i in "${!PLAN_NAMES[@]}"; do
    name="${PLAN_NAMES[$i]}"; rc="${PLAN_RCS[$i]}"; final=$(rl_final_of "$rc")
    case "$name" in
      kubemoot-operator) sed -i -E "s/^appVersion:.*/appVersion: \"${final}\"/" "${wt}/${CHART_DIR}/Chart.yaml" ;;
      dashboard) dashboard_is_subchart "$wt" && set_chart_version "${wt}/${DASH_DIR}" "$final" ;;
      *) sed -i -E "s|^([[:space:]]*[A-Za-z]+: ${name}:)${rc//./\\.}([[:space:]]*)$|\1${final}\2|" "${wt}/${CHART_DIR}/values.yaml" ;;
    esac
  done
  if grep -nE -- '-rc\.[0-9]+' "${wt}/${CHART_DIR}/values.yaml"; then
    die "the chart still pins a release candidate the promotion does not cover"
  fi
  return 0
}

# package_operator_chart WORKTREE VERSION
package_operator_chart() {
  sed -i -E "s/^version:.*/version: $2/" "$1/${CHART_DIR}/Chart.yaml"
  helm package --dependency-update "$1/${CHART_DIR}" --version "$2" --destination "${OUT_DIR}"
}

# package_standalone_dashboard RC: the dashboard chart, when it is released on its own,
# from its candidate's commit with the final version.
package_standalone_dashboard() {
  local rc="$1" wt final
  final=$(rl_final_of "$rc")
  rl_checkout_at "$(git rev-list -n 1 "dashboard-v${rc}")"
  wt="$RL_CHECKOUT"
  set_chart_version "${wt}/${DASH_DIR}" "$final"
  helm package "${wt}/${DASH_DIR}" --destination "${OUT_DIR}"
}

push_charts() {
  local tgz
  for tgz in "${OUT_DIR}"/*.tgz; do
    echo "chart $(basename "$tgz")"
    rl_is_dry && continue
    helm push --insecure-skip-tls-verify "$tgz" "oci://${REGISTRY}/kubemoot/charts"
    bash "${here}/publish-release-chart.sh" "$tgz"
  done
}

# previous_release COMMIT: the release the notes start from; read before this run
# creates any final tag. Until the first promoted chart, the last operator release.
previous_release() {
  local prev
  prev=$(rl_latest_final "$CHART_PREFIX" "$1")
  [ -n "$prev" ] || prev=$(rl_latest_final v "$1")
  echo "$prev"
}

# write_notes COMMIT CHART_FINAL PREVIOUS
write_notes() {
  local src="$1" chart_final="$2" prev="$3" i
  {
    echo "Kubemoot operator chart ${chart_final}."
    echo
    echo '```bash'
    echo "helm install kubemoot oci://${RELEASE_REGISTRY}/charts/kubemoot-operator --version ${chart_final} \\"
    echo "  --namespace kubemoot --create-namespace"
    echo '```'
    echo
    echo "| Component | Image |"
    echo "|---|---|"
    for i in "${!PLAN_NAMES[@]}"; do
      echo "| ${PLAN_NAMES[$i]} | \`${RELEASE_REGISTRY}/${PLAN_NAMES[$i]}:$(rl_final_of "${PLAN_RCS[$i]}")\` |"
    done
    echo
    echo "## Changes${prev:+ since ${prev}}"
    echo
    rl_release_notes "$prev" "$src"
  } > "${OUT_DIR}/notes.md"
}

output() {
  [ -n "${GITHUB_OUTPUT:-}" ] && echo "$1=$2" >> "$GITHUB_OUTPUT"
  return 0
}

main() {
  local point chart_rc chart_final src wt prev i
  point=$(rl_resolve_point "$RC_TAG")
  chart_rc=$(rl_latest_rc "$CHART_PREFIX" "$point")
  [ -n "$chart_rc" ] || die "no ${CHART_PREFIX}*-rc.* tag at or before ${RC_TAG}"
  chart_final=$(rl_final_of "${chart_rc#"$CHART_PREFIX"}")
  rl_tag_exists "${CHART_PREFIX}${chart_final}" && die "${CHART_PREFIX}${chart_final} is already released"
  src=$(git rev-list -n 1 "$chart_rc")
  prev=$(previous_release "$src")
  echo "Promoting ${chart_rc} (commit ${src}) to operator chart ${chart_final}; dry run: ${DRY_RUN}"

  rl_checkout_at "$src"
  wt="$RL_CHECKOUT"
  collect_pins "$wt"
  collect_standalone "$src" "$wt"

  # Package and check every chart before the first push.
  : > "${OUT_DIR}/releases.tsv"
  finalize_pins "$wt"
  package_operator_chart "$wt" "$chart_final"
  for i in "${!PLAN_NAMES[@]}"; do
    if [ "${PLAN_NAMES[$i]}" = "dashboard" ] && ! dashboard_is_subchart "$wt"; then
      package_standalone_dashboard "${PLAN_RCS[$i]}"
    fi
  done

  for i in "${!PLAN_NAMES[@]}"; do
    promote_image "${PLAN_NAMES[$i]}" "${PLAN_RCS[$i]}"
    tag_final "$(image_prefix "${PLAN_NAMES[$i]}")" "${PLAN_RCS[$i]}"
  done
  push_charts

  rl_make_tag "${CHART_PREFIX}${chart_final}" "$chart_rc"
  write_notes "$src" "$chart_final" "$prev"
  rl_push_new_tags
  rl_add_release "${OUT_DIR}" "${CHART_PREFIX}${chart_final}" "Kubemoot ${chart_final}" notes.md
  output chart_version "$chart_final"
}

main "$@"
