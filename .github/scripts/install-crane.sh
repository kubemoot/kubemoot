#!/usr/bin/env bash
# Install crane from a pinned go-containerregistry release, after checking the tarball's
# sha256 against the one recorded here (taken from the release's checksums.txt). A tarball
# that does not match is never unpacked.
# Usage: install-crane.sh [DEST_DIR]   (default /usr/local/bin; sudo is used when it is not writable)
# Env (the tests): CRANE_URL replaces the download URL; CRANE_SHA256 replaces the pinned sum.
# Tested by test-install-crane.sh.
set -euo pipefail

CRANE_VERSION="v0.20.2"
CRANE_SHA256="${CRANE_SHA256:-c14340087103ba9dadf61d45acd20675490fd0ccbd56ac7901fc1b502137f44b}"
CRANE_URL="${CRANE_URL:-https://github.com/google/go-containerregistry/releases/download/${CRANE_VERSION}/go-containerregistry_Linux_x86_64.tar.gz}"
dest="${1:-/usr/local/bin}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
curl -fsSL -o "${work}/crane.tar.gz" "$CRANE_URL"
got="$(sha256sum "${work}/crane.tar.gz" | cut -d' ' -f1)"
if [ "$got" != "$CRANE_SHA256" ]; then
  echo "ERROR: crane tarball sha256 ${got} does not match the pinned ${CRANE_SHA256}" >&2
  exit 1
fi
tar -xzf "${work}/crane.tar.gz" -C "$work" crane
if [ -w "$dest" ]; then install -m 0755 "${work}/crane" "${dest}/crane"; else sudo install -m 0755 "${work}/crane" "${dest}/crane"; fi
echo "installed crane ${CRANE_VERSION} to ${dest}"
