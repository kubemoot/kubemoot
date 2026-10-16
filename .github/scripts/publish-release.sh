#!/usr/bin/env bash
# Publish a tested Kubemoot release candidate as a public release.
#
# main builds release candidates only: every component image X.Y.Z-rc.N and the
# operator chart A.B.C-rc.N go to Harbor, and the homelab runs them. This script
# publishes one of those operator chart candidates, with every component it pins,
# as the final release. Nothing is rebuilt: each image is copied by digest, so the
# published bytes are the tested bytes.
#
#   1. The candidate: the highest operator-chart-vA.B.C-rc.N tag reachable from the
#      starting point (RC_TAG, or the tip of main for "latest").
#   2. Images: every X.Y.Z-rc.N image the candidate chart in Harbor was packaged with
#      (the operator appVersion, its values, the dashboard subchart's appVersion), read
#      from that chart, so the release ships exactly what the homelab ran; plus the
#      latest candidate, as of the candidate's commit, of each image the chart does not
#      pin (STANDALONE_IMAGES). Each is copied by digest to
#      ${RELEASE_REGISTRY}/<name>:X.Y.Z and tagged X.Y.Z in Harbor, and its candidate
#      tag's commit gets the final <prefix>X.Y.Z tag.
#      A candidate must embed the dashboard as a subchart: candidates from before the
#      dashboard became a subchart cannot be published. A candidate built before the
#      versions moved to the tags (real versions in git at its commit) still publishes:
#      its pins are taken from the candidate chart the same way.
#   3. The chart: packaged from the candidate's commit (in a scratch worktree, never
#      committed; git holds 0.0.0) stamped with the final chart version and the finals
#      of the candidate's images, pushed to Harbor and the release registry.
#   4. Signing: every image the release ships and the chart are signed by digest in
#      the release registry with cosign, keyless, under the workflow's GitHub OIDC
#      identity, unless already signed by it (rl_sign_artifact of release-lib.sh).
#      The references go to ${OUT_DIR}/subjects.tsv and the `subjects` output, which
#      the workflow's attest job records SLSA build provenance for. A signing failure
#      stops the run before any tag, so no GitHub Release is written.
#   5. Tags are pushed together (atomic); the release notes go to ${OUT_DIR}/notes.md
#      and ${OUT_DIR}/releases.tsv names the GitHub Release to write.
#      ${OUT_DIR}/provenance.tsv maps every signed artifact to that release, which
#      carries their provenance as kubemoot_A.B.C.intoto.jsonl (create-github-releases.sh).
#
# Re-running after a partial failure is safe: an image already published with the
# same digest is skipped, a different digest stops the run, and the run refuses to
# start when the chart's final tag already exists.
#
# Env:
#   RC_TAG            "latest" (default) or a release-candidate tag on main
#   DRY_RUN           "true" (default) plans, reads registries, packages the charts, and
#                     writes the notes; it pushes nothing, signs nothing, and creates no
#                     tag, but checks that cosign and the signing identity are in place
#   REGISTRY          Harbor host (images at ${REGISTRY}/kubemoot/<name>)
#   RELEASE_REGISTRY  registry plus namespace, e.g. ghcr.io/kubemoot
#   HARBOR_USERNAME, HARBOR_PASSWORD, GHCR_USERNAME, GHCR_TOKEN
#   STANDALONE_IMAGES images released on their own (default below)
#   OUT_DIR           where packaged charts and notes.md land (default: release-files)
#   RELEASE_LIB       release-lib.sh of kubemoot/release-actions (its actions set it)
set -euo pipefail
# A failure inside $(...) stops the script too.
shopt -s inherit_errexit

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=/dev/null
source "${RELEASE_LIB:?RELEASE_LIB must point to release-lib.sh from kubemoot/release-actions}"

RC_TAG="${RC_TAG:-latest}"
DRY_RUN="${DRY_RUN:-true}"
STANDALONE_IMAGES="${STANDALONE_IMAGES:-code-sandbox artifact-access scheduling-mcp test-runner}"
OUT_DIR="$(mkdir -p "${OUT_DIR:-release-files}" && cd "${OUT_DIR:-release-files}" && pwd)"
: "${REGISTRY:?REGISTRY required}"
: "${RELEASE_REGISTRY:?RELEASE_REGISTRY required}"

CHART_PREFIX="operator-chart-v"
CHART_DIR="operator/chart/kubemoot-operator"
DASH_DIR="dashboard/charts/kubemoot-dashboard"
CANDIDATE=""

PLAN_NAMES=()     # image names to publish
PLAN_RCS=()       # their candidate versions

die() { echo "ERROR: $*" >&2; exit 1; }
cleanup() {
  rl_remove_worktrees
  [ -z "$CANDIDATE" ] || rm -rf "$(dirname "$CANDIDATE")"
}
trap cleanup EXIT

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

# candidate_chart VERSION: pulls the candidate chart from Harbor into a scratch
# directory, its path in CANDIDATE, with the dashboard subchart unpacked under
# ${CANDIDATE}/charts/kubemoot-dashboard.
candidate_chart() {
  local dir sub
  dir="$(mktemp -d)"
  helm pull --insecure-skip-tls-verify "oci://${REGISTRY}/kubemoot/charts/kubemoot-operator" \
    --version "$1" --untar --untardir "$dir" >/dev/null \
    || die "cannot read the candidate operator chart $1 from Harbor"
  CANDIDATE="${dir}/kubemoot-operator"
  if [ ! -d "${CANDIDATE}/charts/kubemoot-dashboard" ]; then
    sub=$(find "${CANDIDATE}/charts" -maxdepth 1 -name 'kubemoot-dashboard-*.tgz' 2>/dev/null | head -n 1)
    [ -n "$sub" ] || die "the candidate operator chart $1 has no dashboard subchart"
    tar -xzf "$sub" -C "${CANDIDATE}/charts"
  fi
}

chart_app_version() {
  sed -nE 's/^appVersion: "?([^"]+)"?$/\1/p' "$1/Chart.yaml"
}

# collect_pins CANDIDATE: every release-candidate image the candidate chart pins.
collect_pins() {
  local cand="$1" app name version
  app=$(chart_app_version "$cand")
  rl_is_rc "$app" && plan_add kubemoot-operator "$app"
  while read -r name version; do
    plan_add "$name" "$version"
  done < <(sed -nE 's/^[[:space:]]*[A-Za-z]+: ([a-z0-9-]+):([0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+)[[:space:]]*$/\1 \2/p' \
             "${cand}/values.yaml")
  app=$(chart_app_version "${cand}/charts/kubemoot-dashboard")
  rl_is_rc "$app" && plan_add dashboard "$app"
  return 0
}

# collect_standalone COMMIT: the latest candidate, as of the chart candidate's COMMIT,
# of each image the chart does not pin.
collect_standalone() {
  local point="$1" name prefix rc
  for name in ${STANDALONE_IMAGES}; do
    planned "$name" && continue
    prefix=$(image_prefix "$name")
    rc=$(rl_latest_rc "$prefix" "$point")
    [ -n "$rc" ] || continue
    rl_tag_exists "${prefix}$(rl_final_of "${rc#"$prefix"}")" && continue
    plan_add "$name" "${rc#"$prefix"}"
  done
}

# publish_image NAME RC: copy the candidate by digest to its final tag in the
# release registry and in Harbor.
publish_image() {
  local name="$1" rc="$2" final digest published harbor earlier=""
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
  if ! rl_is_dry; then
    if [ -z "$published" ]; then
      bash "${here}/publish-release-image.sh" "${REGISTRY}/kubemoot/${name}@${digest}" "$final"
    fi
    crane tag --insecure "${REGISTRY}/kubemoot/${name}@${digest}" "$final"
  fi
  # An image an earlier release published (its final tag exists) is signed if it is not
  # yet, and attested only then.
  rl_tag_exists "$(image_prefix "$name")${final}" && earlier=earlier
  rl_sign_artifact "${RELEASE_REGISTRY}/${name}" "$digest" $earlier
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


# finalize_pins FILE: rewrite every candidate pin in FILE to its final version.
finalize_pins() {
  local file="$1" i name rc
  for i in "${!PLAN_NAMES[@]}"; do
    name="${PLAN_NAMES[$i]}"; rc="${PLAN_RCS[$i]}"
    sed -i -E "s|^([[:space:]]*[A-Za-z]+: \"?${name}:)${rc//./\\.}([\"[:space:]]*)$|\1$(rl_final_of "$rc")\2|" "$file"
  done
}

# package_operator_chart WORKTREE CANDIDATE VERSION: the chart at the candidate's commit,
# stamped with the final VERSION and the finals of the images CANDIDATE was packaged with.
package_operator_chart() {
  local wt="$1" cand="$2" version="$3" values="$1/${CHART_DIR}/values.yaml" dash="$1/${DASH_DIR}"
  rl_stamp_images_like "$values" "${cand}/values.yaml" >/dev/null
  rl_stamp_images_like "${dash}/values.yaml" "${cand}/charts/kubemoot-dashboard/values.yaml" >/dev/null
  finalize_pins "$values"
  if grep -nE -- '-rc\.[0-9]+' "$values" "${dash}/values.yaml"; then
    die "the chart still pins a release candidate the release does not cover"
  fi
  rl_stamp_chart "$dash" "$(rl_final_of "$(chart_app_version "${cand}/charts/kubemoot-dashboard")")"
  rl_stamp_chart "${wt}/${CHART_DIR}" "$version" "$(rl_final_of "$(chart_app_version "$cand")")"
  helm package --dependency-update "${wt}/${CHART_DIR}" --destination "${OUT_DIR}"
}

# push_charts: each packaged chart to Harbor and the release registry, then signed by
# the digest the release registry holds.
push_charts() {
  local tgz repo digest
  for tgz in "${OUT_DIR}"/*.tgz; do
    echo "chart $(basename "$tgz")"
    repo="$(rl_chart_repo "$tgz")"
    if rl_is_dry; then
      rl_sign_artifact "$repo" "<digest after the push>"
      continue
    fi
    helm push --insecure-skip-tls-verify "$tgz" "oci://${REGISTRY}/kubemoot/charts"
    bash "${here}/publish-release-chart.sh" "$tgz"
    # publish-release-chart.sh retries the push, so the digest is read from the registry.
    digest=$(crane digest "${repo}:$(helm show chart "$tgz" | sed -n 's/^version: //p')") \
      || die "cannot read the digest of ${repo} after the push"
    rl_sign_artifact "$repo" "$digest"
  done
}

# previous_release COMMIT: the release the notes start from; read before this run
# creates any final tag. Until the first published chart, the last operator release.
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
    rl_notes_document "$prev" "$src" "${CHART_PREFIX}${chart_final}"
  } > "${OUT_DIR}/notes.md"
}

# write_provenance_map TAG ASSET: the GitHub Release TAG carries the provenance of every
# artifact this run signed, as the one asset ASSET.
write_provenance_map() {
  local name digest
  : > "${OUT_DIR}/provenance.tsv"
  while IFS=$'\t' read -r name digest; do
    printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$name" "$digest" >> "${OUT_DIR}/provenance.tsv"
  done < "${OUT_DIR}/subjects.tsv"
}

output() {
  [ -n "${GITHUB_OUTPUT:-}" ] && echo "$1=$2" >> "$GITHUB_OUTPUT"
  return 0
}

main() {
  local point chart_rc chart_final src prev i
  point=$(rl_resolve_point "$RC_TAG")
  chart_rc=$(rl_latest_rc "$CHART_PREFIX" "$point")
  [ -n "$chart_rc" ] || die "no ${CHART_PREFIX}*-rc.* tag at or before ${RC_TAG}"
  chart_final=$(rl_final_of "${chart_rc#"$CHART_PREFIX"}")
  rl_tag_exists "${CHART_PREFIX}${chart_final}" && die "${CHART_PREFIX}${chart_final} is already released"
  src=$(git rev-list -n 1 "$chart_rc")
  prev=$(previous_release "$src")
  echo "Publishing ${chart_rc} (commit ${src}) to operator chart ${chart_final}; dry run: ${DRY_RUN}"
  rl_sign_preflight "${OUT_DIR}"

  candidate_chart "${chart_rc#"$CHART_PREFIX"}"
  collect_pins "$CANDIDATE"
  collect_standalone "$src"

  # Package and check the chart before the first push.
  : > "${OUT_DIR}/releases.tsv"
  rl_checkout_at "$src"
  package_operator_chart "$RL_CHECKOUT" "$CANDIDATE" "$chart_final"

  for i in "${!PLAN_NAMES[@]}"; do
    publish_image "${PLAN_NAMES[$i]}" "${PLAN_RCS[$i]}"
    tag_final "$(image_prefix "${PLAN_NAMES[$i]}")" "${PLAN_RCS[$i]}"
  done
  push_charts

  rl_make_tag "${CHART_PREFIX}${chart_final}" "$chart_rc"
  write_notes "$src" "$chart_final" "$prev"
  rl_push_new_tags
  rl_add_release "${OUT_DIR}" "${CHART_PREFIX}${chart_final}" "Kubemoot ${chart_final}" notes.md
  write_provenance_map "${CHART_PREFIX}${chart_final}" "kubemoot_${chart_final}.intoto.jsonl"
  output chart_version "$chart_final"
  output subjects "$(rl_sign_subjects_json)"
}

main "$@"
