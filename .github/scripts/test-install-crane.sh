#!/usr/bin/env bash
# Tests for install-crane.sh against a local tarball (file:// URL).
# Usage: bash .github/scripts/test-install-crane.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
failures=0
check() {
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want [$2] got [$3]"; failures=$((failures + 1)); fi
}
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "${work}/src" "${work}/dest"
printf '#!/bin/sh\necho fake crane\n' > "${work}/src/crane"
chmod +x "${work}/src/crane"
tar -czf "${work}/crane.tar.gz" -C "${work}/src" crane
sum="$(sha256sum "${work}/crane.tar.gz" | cut -d' ' -f1)"
run() { CRANE_URL="file://${work}/crane.tar.gz" CRANE_SHA256="$1" bash "${here}/install-crane.sh" "${work}/dest" >/dev/null 2>&1 && echo 0 || echo 1; }

check "a tarball with the pinned sum installs" "0" "$(run "$sum")"
check "the installed binary is executable" "fake crane" "$("${work}/dest/crane")"
rm -f "${work}/dest/crane"
check "a tarball with another sum is refused" "1" "$(run "0000000000000000000000000000000000000000000000000000000000000000")"
check "a refused tarball installs nothing" "no" "$([ -e "${work}/dest/crane" ] && echo yes || echo no)"
check "a download that fails is refused" "1" "$(CRANE_URL="file://${work}/missing.tar.gz" CRANE_SHA256="$sum" bash "${here}/install-crane.sh" "${work}/dest" >/dev/null 2>&1 && echo 0 || echo 1)"
check "the pinned sum is a sha256" "64" "$(sed -n 's/^CRANE_SHA256=.*:-\([0-9a-f]*\)}"/\1/p' "${here}/install-crane.sh" | tr -d '\n' | wc -c | tr -d ' ')"

[ "$failures" -eq 0 ] || { echo "${failures} failed"; exit 1; }
echo "all passed"
