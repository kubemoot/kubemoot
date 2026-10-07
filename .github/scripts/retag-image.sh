#!/usr/bin/env bash
# Tag the image main built from a commit with its release candidate version, in Harbor.
# Only main's build pushes :<sha> (build-image-buildpacks.sh), so a release never falls
# back to another image: a missing :<sha> fails the release.
#
# Usage: retag-image.sh IMAGE SHA VERSION
#   IMAGE     the repository, e.g. harbor-homelab.dijure.com/kubemoot/mcp-bridge
#   SHA       the full commit SHA the image was built from
#   VERSION   the version to tag, e.g. 0.12.3-rc.4
# Env: CRANE (crane), already logged in to the registry
set -euo pipefail

die() { echo "retag-image: $*" >&2; exit 2; }

main() {
  [ $# -eq 3 ] || die "usage: retag-image.sh IMAGE SHA VERSION"
  local image="$1" sha="$2" version="$3" crane="${CRANE:-crane}"
  [ -n "$image" ] || die "IMAGE is empty"
  [[ "$sha" =~ ^[0-9a-f]{40}$ ]] || die "SHA must be a full commit SHA, got: ${sha}"
  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$ ]] || die "VERSION is not a version: ${version}"

  if ! "$crane" manifest --insecure "${image}:${sha}" >/dev/null 2>&1; then
    echo "::error::${image}:${sha} not found; the release needs main's build of this commit"
    exit 1
  fi
  echo "Copying ${image}:${sha} -> ${image}:${version}"
  "$crane" copy --insecure "${image}:${sha}" "${image}:${version}"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
