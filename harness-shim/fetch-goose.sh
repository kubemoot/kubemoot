#!/usr/bin/env bash
# Download the goose release binary the harness-goose image carries, check it against the
# pinned sha256, and write it to a directory.
#
# Why a download step and not a buildpack: no Paketo buildpack installs goose, and the one
# way to run extra build code inside a buildpack build (an inline buildpack) is not in the
# builder. The image build packages a prepared directory, like the other Go images; this
# step prepares it, and a sha256 that does not match stops the build before any image.
#
# goose 1.55.0, Apache-2.0, the statically linked (musl) linux amd64 release. The sha256 is
# of the release asset goose-x86_64-unknown-linux-musl.tar.gz: it equals the digest GitHub
# reports for the asset and the one computed on a download of it. Raise both values together.
#
# Usage: fetch-goose.sh DEST_DIR      writes DEST_DIR/goose
# Env: GOOSE_BASE_URL (the GitHub release download URL of the pinned version), for tests.
set -euo pipefail

GOOSE_VERSION=1.55.0
GOOSE_SHA256=fade147bd5ebc020ecea05453b0905397f1ebb01ee3de2f3678570c5c3502ed7
GOOSE_ASSET=goose-x86_64-unknown-linux-musl.tar.gz

die() { echo "fetch-goose: $*" >&2; exit 2; }

# check_sha256 FILE SHA256: true when FILE hashes to SHA256.
check_sha256() {
  echo "$2  $1" | sha256sum -c - >/dev/null 2>&1
}

main() {
  [ $# -eq 1 ] || die "usage: fetch-goose.sh DEST_DIR"
  local dest="$1" base work
  [ "$(uname -m)" = "x86_64" ] || die "the goose release asset is amd64 only"
  base="${GOOSE_BASE_URL:-https://github.com/aaif-goose/goose/releases/download/v${GOOSE_VERSION}}"
  work="$(mktemp -d)"
  # Expanded now: a trap that named the local would act on a caller's variable of that name.
  # shellcheck disable=SC2064
  trap "rm -rf '${work}'" EXIT
  curl -fsSL -o "${work}/${GOOSE_ASSET}" "${base}/${GOOSE_ASSET}" || die "could not download ${GOOSE_ASSET}"
  check_sha256 "${work}/${GOOSE_ASSET}" "$GOOSE_SHA256" || die "${GOOSE_ASSET} does not match the pinned sha256"
  mkdir -p "$dest"
  tar -xzf "${work}/${GOOSE_ASSET}" -C "$work" ./goose
  install -m 0755 "${work}/goose" "${dest}/goose"
  "${dest}/goose" --version
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
