#!/usr/bin/env bash
# Tests for fetch-goose.sh and stage-image.sh: the pinned download is checked, a wrong file
# is refused before anything is written, and the image files agree on where goose lives.
# Usage: bash harness-shim/test-fetch-goose.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source-path=SCRIPTDIR source=fetch-goose.sh
source "${here}/fetch-goose.sh"

failures=0
check() {
  local name="$1" want="$2" got="$3"
  if [ "$want" = "$got" ]; then
    echo "ok   ${name}"
  else
    echo "FAIL ${name}: want [${want}] got [${got}]"
    failures=$((failures + 1))
  fi
}

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# A fake release: a tarball holding ./goose, served from a directory.
mkdir -p "${work}/release" "${work}/pack"
printf '#!/bin/sh\necho "goose 0.0.0-test"\n' >"${work}/pack/goose"
chmod 0755 "${work}/pack/goose"
tar -czf "${work}/release/${GOOSE_ASSET}" -C "${work}/pack" ./goose
export GOOSE_BASE_URL="file://${work}/release"

check "the pinned digest is a sha256" 1 "$(grep -cE '^[0-9a-f]{64}$' <<<"$GOOSE_SHA256")"
check "check_sha256 accepts the matching digest" 0 \
  "$(check_sha256 "${work}/release/${GOOSE_ASSET}" "$(sha256sum "${work}/release/${GOOSE_ASSET}" | cut -d' ' -f1)" && echo 0 || echo 1)"
check "check_sha256 refuses another digest" 1 "$(check_sha256 "${work}/release/${GOOSE_ASSET}" "$GOOSE_SHA256" && echo 0 || echo 1)"

# A file that is not the pinned release is refused and nothing is written.
check "a download that does not match the pin fails" 2 \
  "$( (main "${work}/out-bad" >/dev/null 2>&1) && echo 0 || echo $?)"
check "a refused download leaves no binary" "no" "$([ -e "${work}/out-bad/goose" ] && echo yes || echo no)"

# The same file, once it is the pinned one, is installed executable.
GOOSE_SHA256="$(sha256sum "${work}/release/${GOOSE_ASSET}" | cut -d' ' -f1)"
check "a matching download succeeds" "goose 0.0.0-test" "$(main "${work}/out" 2>&1)"
check "goose is installed executable" "yes" "$([ -x "${work}/out/goose" ] && echo yes || echo no)"
check "a missing download fails" 2 "$( (GOOSE_BASE_URL="file://${work}/none" main "${work}/out2" >/dev/null 2>&1) && echo 0 || echo $?)"
check "no argument fails" 2 "$( (main >/dev/null 2>&1) && echo 0 || echo $?)"

# The Procfile, the goose configuration and the staged file names agree.
check "the Procfile starts the shim from /workspace with the staged configuration" \
  "web: /workspace/harness-shim -config /workspace/goose.json" "$(tr -s ' ' <"${here}/image/Procfile" | head -1)"
check "the goose configuration runs goose from /workspace" "/workspace/goose" \
  "$(grep -o '"/workspace/goose"' "${here}/goose/config.json" | head -1 | tr -d '"')"

if [ "$failures" -gt 0 ]; then
  echo "${failures} check(s) failed"
  exit 1
fi
echo "all fetch-goose tests passed"
