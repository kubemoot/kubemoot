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
#   images.tsv   "<tarball>\t<Harbor reference>\t<reference the chart uses>" per image
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
# The component images the quickstart can run; the first three are always pinned.
COMPONENTS="kubemoot-operator|agent-runtime|discussion-gateway|crew-liaison|dashboard"
CORE_COMPONENTS="kubemoot-operator agent-runtime discussion-gateway"

# candidate_images: the component images in rendered manifests on stdin, one reference per
# distinct image, as the chart names it (its registry prefix included).
candidate_images() {
  grep -oE "[A-Za-z0-9._:/-]+/(${COMPONENTS}):[A-Za-z0-9._-]+" | sort -u
}

# require_core: fail unless the image list on stdin holds the core components.
require_core() {
  local list core
  list="$(cat)"
  for core in ${CORE_COMPONENTS}; do
    grep -q "/${core}:" <<<"$list" || die "the candidate chart pins no ${core} image"
  done
}

main() {
  local out="${OUT_DIR:-quickstart-candidate}" point chart_rc version rendered ref name tag n=0
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
  candidate_images <<<"$rendered" | tee "${out}/images.txt" | require_core

  : > "${out}/images.tsv"
  while read -r ref; do
    n=$((n + 1))
    name="${ref##*/}"; tag="${name#*:}"; name="${name%%:*}"
    crane pull --insecure --platform linux/amd64 "${REGISTRY}/kubemoot/${name}:${tag}" "${out}/images/${n}.tar" \
      || die "cannot pull ${REGISTRY}/kubemoot/${name}:${tag} from Harbor"
    printf 'images/%s.tar\t%s\t%s\n' "$n" "${REGISTRY}/kubemoot/${name}:${tag}" "$ref" >> "${out}/images.tsv"
  done < "${out}/images.txt"

  output rc_tag "$chart_rc"
  output chart_version "$version"
}

# Sourced (by the tests), it defines the functions and runs nothing.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
