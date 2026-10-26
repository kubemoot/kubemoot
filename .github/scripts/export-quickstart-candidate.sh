#!/usr/bin/env bash
# Export the release candidate Publish Release is about to publish, so the quickstart can run
# it end to end on a hosted runner that cannot reach Harbor.
#
# Publish Release copies the candidate's images by digest to the release registry and
# packages the chart from the same commit, so the candidate in Harbor carries the bytes
# users get. This runs on a homelab runner (it reaches Harbor), resolves the candidate
# exactly as publish-release.sh does, and writes into OUT_DIR:
#   chart.tgz    the candidate operator chart, pulled from Harbor
#   images/N.tar each Kubemoot component image the quickstart runs, pulled from Harbor
#   images.tsv   "<tarball>\t<reference the chart uses>" per image (no Harbor host: the
#                artifact of a public repository is downloadable)
# The hosted job loads each tarball under the reference the chart uses, so the chart runs
# unchanged. The step outputs rc_tag (the candidate's tag) and chart_version. Publish
# Release publishes that rc_tag, not "latest", so a candidate that lands after this check
# is never published untested.
#
# Env:
#   RC_TAG       "latest" (default) or a release-candidate tag on main
#   REGISTRY     Harbor host (images at ${REGISTRY}/kubemoot/<name>)
#   OUT_DIR      where the export lands (default: quickstart-candidate)
#   RELEASE_LIB  release-lib.sh of kubemoot/release-actions (its actions set it)
# Tested by test-export-quickstart-candidate.sh.
set -euo pipefail
shopt -s inherit_errexit

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=/dev/null
source "${RELEASE_LIB:?RELEASE_LIB must point to release-lib.sh from kubemoot/release-actions}"
# shellcheck source=/dev/null
source "${here}/release-chart-lib.sh"

CHART_PREFIX="operator-chart-v"
# shellcheck source=/dev/null
source "${here}/quickstart-images.sh"

# harbor_ref NAME TAG DIGEST: where Harbor holds the image (by digest when the chart pins one).
harbor_ref() {
  if [ -n "$3" ]; then echo "${REGISTRY}/kubemoot/${1}@${3}"; else echo "${REGISTRY}/kubemoot/${1}:${2}"; fi
}

main() {
  local out="${OUT_DIR:-quickstart-candidate}" point chart_rc version rendered ref source_ref n=0
  : "${REGISTRY:?REGISTRY required}"
  mkdir -p "${out}/images"
  out="$(cd "$out" && pwd)"

  point=$(rl_resolve_point "${RC_TAG:-latest}")
  chart_rc=$(rl_latest_rc "$CHART_PREFIX" "$point")
  [ -n "$chart_rc" ] || die "no ${CHART_PREFIX}*-rc.* tag at or before ${RC_TAG:-latest}"
  version="${chart_rc#"$CHART_PREFIX"}"
  echo "Candidate ${chart_rc}"

  helm pull --insecure-skip-tls-verify "oci://${REGISTRY}/kubemoot/charts/kubemoot-operator" \
    --version "$version" --destination "$out" >/dev/null \
    || die "cannot read the candidate operator chart ${version} from Harbor"
  mv "${out}/kubemoot-operator-${version}.tgz" "${out}/chart.tgz"

  # The quickstart's own values, as of the candidate's commit, decide which images run.
  rendered="$(helm template q "${out}/chart.tgz" --values <(git show "${chart_rc}:quickstart/operator-values.yaml"))" \
    || die "cannot render the candidate chart"
  quickstart_images <<<"$rendered" | quickstart_require_core > "${out}/images.txt"

  : > "${out}/images.tsv"
  while read -r ref; do
    n=$((n + 1))
    source_ref="$(harbor_ref "$(image_name "$ref")" "$(image_tag "$ref")" "$(image_digest "$ref")")"
    crane pull --insecure --platform linux/amd64 "$source_ref" "${out}/images/${n}.tar" \
      || die "cannot pull ${source_ref} from Harbor"
    printf 'images/%s.tar\t%s\n' "$n" "$ref" >> "${out}/images.tsv"
  done < "${out}/images.txt"

  output rc_tag "$chart_rc"
  output chart_version "$version"
}

# Sourced (by the tests), it defines the functions and runs nothing.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
