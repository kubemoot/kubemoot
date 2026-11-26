#!/usr/bin/env bash
# Assemble the files the harness-goose image packages next to image/ (its Procfile and
# buildpacks.env): the statically linked shim, the pinned goose binary (fetch-goose.sh) and
# the shim's goose configuration as goose.json. Run with the Go toolchain of
# harness-shim/go.mod.
#
# Usage: harness-shim/stage-image.sh DEST_DIR
set -euo pipefail

die() { echo "stage-image: $*" >&2; exit 2; }

main() {
  [ $# -eq 1 ] || die "usage: harness-shim/stage-image.sh DEST_DIR"
  local dest here root
  dest="$(mkdir -p "$1" && cd "$1" && pwd)"
  here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  root="$(cd "${here}/.." && pwd)"
  (cd "$here" && "${root}/.github/scripts/go-build-static.sh" "${dest}/harness-shim" ./cmd/harness-shim/)
  "${here}/fetch-goose.sh" "$dest"
  cp "${here}/goose/config.json" "${dest}/goose.json"
  chmod 0755 "${dest}/harness-shim" "${dest}/goose"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
